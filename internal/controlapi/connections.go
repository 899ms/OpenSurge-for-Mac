package controlapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"open-mihomo-gateway/internal/config"
	"open-mihomo-gateway/internal/device"
	"open-mihomo-gateway/internal/mihomo"
	"open-mihomo-gateway/internal/runtime"
)

const connectionObservationInterval = time.Second
const gatewayLocalOwner = "gateway-local"
const unclassifiedOwner = "unclassified"

type connectionObservationCache struct {
	mu       sync.Mutex
	key      string
	response ConnectionsResponse
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	s.handleConnectionObservation(w, r, true)
}

func (s *Server) handleConnectionObservation(w http.ResponseWriter, r *http.Request, details bool) {
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	response := s.observeConnections(r.Context(), cfg)
	w.Header().Set("Cache-Control", "no-store")
	if details {
		writeJSON(w, http.StatusOK, response)
	} else {
		writeJSON(w, http.StatusOK, response.DeviceTrafficResponse)
	}
}

func (s *Server) observeConnections(ctx context.Context, cfg config.Config) ConnectionsResponse {
	cache := &s.connectionObservation
	cache.mu.Lock()
	defer cache.mu.Unlock()
	paths := runtime.NewPaths(cfg)
	revision := fileDigest(s.configPath)
	key := strings.Join([]string{revision, fileDigest(paths.StateFile), fileDigest(paths.DevicePolicyApplied), fileDigest(paths.LeaseFile), fileDigest(cfg.DevicePolicy.File)}, ":")
	if cache.key == key && !cache.response.SampledAt.IsZero() && time.Since(cache.response.SampledAt) < connectionObservationInterval {
		return cache.response
	}
	leases, leaseErr := device.LoadLeases(paths.LeaseFile)
	snapshot, connectionErr := s.fetchConnections(ctx, cfg)
	local := newGatewayLocalIdentity(cfg.Gateway.LANIP, mihomo.TUNRuntimeState{})
	var identityErr error
	if connectionErr == nil {
		local, identityErr = s.fetchGatewayLocalIdentity(ctx, cfg, snapshot)
	} else {
		// A partial failed response must not be presented as a current sample.
		snapshot = mihomo.ConnectionsSnapshot{}
	}
	sampledAt := time.Now().UTC()
	traffic := aggregateDeviceTrafficWithPolicy(leases, loadAppliedTrafficPolicy(paths), snapshot, local, cfg.Gateway.LANPrefixLen, true)
	annotateDeviceTrafficIPv6BlockState(traffic.Devices, cfg.Transparent.TUNIPv6 != config.TUNIPv6Off)
	if traffic.GatewayLocal.Transport == localTransportNone && cfg.Transparent.TUNEnabled() {
		traffic.GatewayLocal.Transport = localTransportTUN
	}
	if connectionErr == nil {
		s.trafficSampler.annotate(&traffic, snapshot, local, sampledAt)
	} else {
		s.trafficSampler.reset()
	}
	var policyErr error
	if cfg.DevicePolicy.File != "" {
		var bundle device.PolicyBundle
		bundle, policyErr = device.LoadPolicyBundle(cfg.DevicePolicy.File)
		if policyErr == nil {
			annotateRegisteredDeviceNames(&traffic, bundle.Policy)
			appendPendingTrafficDevices(&traffic, bundle.Policy, cfg)
		}
	}
	traffic.SchemaVersion = SchemaVersion
	traffic.Revision = revision
	traffic.SampledAt = sampledAt
	traffic.Scope = deviceTrafficScope
	traffic.ConnectionError = errorString(errors.Join(connectionErr, identityErr))
	traffic.InventoryError = errorString(errors.Join(leaseErr, policyErr))
	response := ConnectionsResponse{DeviceTrafficResponse: traffic, Connections: make([]ObservedConnection, 0, len(snapshot.Connections))}
	for index, connection := range snapshot.Connections {
		entry := ObservedConnection{Connection: connection, OwnerKey: traffic.connectionOwners[index], SourceFamily: connectionSourceFamily(connection)}
		entry.Upload = nonnegativeBytes(entry.Upload)
		entry.Download = nonnegativeBytes(entry.Download)
		if index < len(traffic.connectionRates) {
			entry.UploadRate = traffic.connectionRates[index].Upload
			entry.DownloadRate = traffic.connectionRates[index].Download
		}
		response.Connections = append(response.Connections, entry)
	}
	cache.key, cache.response = key, response
	return response
}

// Applied bundles retain the full registration inventory, including dormant
// devices. Only identities actually compiled into this runtime may own traffic.
func loadAppliedTrafficPolicy(paths runtime.Paths) device.PolicySet {
	bundle, ok := loadAppliedDevicePolicyBundle(paths)
	if !ok {
		return device.PolicySet{}
	}
	active := make(map[string]bool, len(bundle.Compiled.Devices))
	for _, managed := range bundle.Compiled.Devices {
		active[managed.ID] = true
	}
	policy := device.PolicySet{}
	for _, managed := range bundle.Policy.Devices {
		if active[managed.ID] {
			policy.Devices = append(policy.Devices, managed)
		}
	}
	return policy
}

func appendPendingTrafficDevices(traffic *DeviceTrafficResponse, policy device.PolicySet, cfg config.Config) {
	for _, managed := range policy.Devices {
		found := false
		for index := range traffic.Devices {
			row := &traffic.Devices[index]
			if row.DeviceID == managed.ID || (row.IP == normalizeTrafficIP(managed.IPv4) && strings.EqualFold(row.MAC, managed.MAC)) {
				if row.DeviceID == "" {
					// This is a navigation/configuration annotation only. Ownership
					// and the stable observation key were already resolved above.
					row.DeviceID = managed.ID
					row.ConfigurationState = "pending"
				}
				found = true
				break
			}
		}
		if found {
			continue
		}
		state := "pending"
		if !sameLANSourceIPv4(managed.IPv4, cfg.Gateway.LANIP, cfg.Gateway.LANPrefixLen) {
			state = "out_of_lan"
		}
		traffic.Devices = append(traffic.Devices, DeviceTraffic{
			Key: "device:" + managed.ID, DeviceID: managed.ID, Name: device.DisplayName(managed), IP: managed.IPv4,
			MAC: strings.ToLower(managed.MAC), Addresses: []string{managed.IPv4}, IdentitySource: identitySourceRegisteredStatic,
			ConfigurationState: state,
		})
	}
	traffic.Totals.Devices = len(traffic.Devices)
}

func connectionSourceFamily(connection mihomo.Connection) string {
	ip := net.ParseIP(normalizeTrafficIP(metadataString(connection.Metadata, "sourceIP")))
	if ip == nil {
		return "unknown"
	}
	if ip.To4() != nil {
		return "ipv4"
	}
	return "ipv6"
}

// A device user alone is insufficient: only our dedicated packet listener
// carries the broker's MAC-bound identity. Never infer ownership from an IPv6
// prefix, process metadata, a destination, or a desired (unapplied) device.
func packetTrafficDevice(connection mihomo.Connection) string {
	if metadataString(connection.Metadata, "inboundName") != config.IPv6PacketListenerName ||
		localConnectionTransport(connection) != localTransportTUN || connectionSourceFamily(connection) != "ipv6" {
		return ""
	}
	user := metadataString(connection.Metadata, "inboundUser")
	if !strings.HasPrefix(user, "device:") {
		return ""
	}
	return strings.TrimPrefix(user, "device:")
}

func trafficDeviceKey(row DeviceTraffic) string {
	if row.DeviceID != "" {
		return "device:" + row.DeviceID
	}
	if row.MAC != "" {
		return "lease:" + row.MAC
	}
	return "ip:" + row.IP
}

func addTrafficAddress(row *DeviceTraffic, address string) {
	if address == "" {
		return
	}
	for _, current := range row.Addresses {
		if current == address {
			return
		}
	}
	row.Addresses = append(row.Addresses, address)
}
