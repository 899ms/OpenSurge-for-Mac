package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"open-mihomo-gateway/internal/config"
	"open-mihomo-gateway/internal/device"
	"open-mihomo-gateway/internal/mihomo"
	"open-mihomo-gateway/internal/runtime"
)

func observationPolicy() device.PolicySet {
	return device.PolicySet{Devices: []device.ManagedDevice{
		{ID: "phone", Name: "Phone", IPv4: "192.168.1.151", MAC: "aa:bb:cc:dd:ee:51"},
		{ID: "tv", Name: "TV", IPv4: "192.168.1.152", MAC: "aa:bb:cc:dd:ee:52"},
	}}
}

func packetObservation(id, user, ip string) mihomo.Connection {
	return mihomo.Connection{ID: id, Upload: 100, Download: 1000, Metadata: map[string]any{
		"sourceIP": ip, "type": "Tun", "inboundName": config.IPv6PacketListenerName, "inboundUser": user,
	}}
}

func TestConnectionObservationDualStackCountersReconcile(t *testing.T) {
	local := newGatewayLocalIdentity("192.168.1.20", testSystemTUNRuntime())
	snapshot := mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{
		{ID: "v4", Upload: 200, Download: 2000, Metadata: map[string]any{"sourceIP": "::ffff:192.168.1.151"}},
		packetObservation("v6", "device:phone", "fdfe:dcba:9878::51"),
		packetObservation("v6-privacy", "device:phone", "fdfe:dcba:9878::abcd"),
		packetObservation("tv", "device:tv", "fdfe:dcba:9878::52"),
		{ID: "mac", Upload: 300, Download: 3000, Metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "inboundName": mihomo.SystemTUNListenerName}},
		{ID: "observed", Upload: 400, Download: 4000, Metadata: map[string]any{"sourceIP": "192.168.1.177"}},
		packetObservation("unknown", "device:missing", "fdfe:dcba:9878::99"),
	}}
	first := aggregateDeviceTrafficWithPolicy(nil, observationPolicy(), snapshot, local, 24, true)
	if first.Devices[0].DeviceID != "phone" || first.Devices[0].ActiveConnections != 3 || first.Devices[0].Upload != 400 || len(first.Devices[0].Addresses) != 3 {
		t.Fatalf("dual stack device = %#v", first.Devices[0])
	}
	if first.connectionOwners[1] != "device:phone" || first.connectionOwners[2] != "device:phone" || first.connectionOwners[4] != gatewayLocalOwner || first.connectionOwners[6] != unclassifiedOwner {
		t.Fatalf("owners = %#v", first.connectionOwners)
	}
	assertObservationTotals(t, first)
	now := time.Now()
	sampler := newTrafficRateSampler()
	sampler.annotate(&first, snapshot, local, now)
	for index := range snapshot.Connections {
		snapshot.Connections[index].Upload += 200
		snapshot.Connections[index].Download += 400
	}
	second := aggregateDeviceTrafficWithPolicy(nil, observationPolicy(), snapshot, local, 24, true)
	sampler.annotate(&second, snapshot, local, now.Add(2*time.Second))
	if second.Devices[0].UploadRate != 300 || second.Devices[0].DownloadRate != 600 {
		t.Fatalf("dual stack rates = %#v", second.Devices[0])
	}
	if second.GatewayRates.Upload != 700 || second.Unclassified.UploadRate != 100 {
		t.Fatalf("gateway rates = %#v", second)
	}
	assertObservationTotals(t, second)
}

func assertObservationTotals(t *testing.T, response DeviceTrafficResponse) {
	t.Helper()
	got := response.GatewayTotals
	if got.ActiveConnections != response.Totals.ActiveConnections+response.GatewayLocal.ActiveConnections+response.Unclassified.ActiveConnections ||
		got.Upload != response.Totals.Upload+response.GatewayLocal.Upload+response.Unclassified.Upload ||
		got.Download != response.Totals.Download+response.GatewayLocal.Download+response.Unclassified.Download ||
		got.UploadRate != response.Totals.UploadRate+response.GatewayLocal.UploadRate+response.Unclassified.UploadRate ||
		got.DownloadRate != response.Totals.DownloadRate+response.GatewayLocal.DownloadRate+response.Unclassified.DownloadRate {
		t.Fatalf("observation does not reconcile: %#v", response)
	}
}

func TestConnectionObservationDoesNotGuessPacketIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*mihomo.Connection)
		policy device.PolicySet
		leases []device.Client
	}{
		{name: "unknown device", mutate: func(c *mihomo.Connection) { c.Metadata["inboundUser"] = "device:missing" }},
		{name: "missing prefix", mutate: func(c *mihomo.Connection) { c.Metadata["inboundUser"] = "phone" }},
		{name: "different listener", mutate: func(c *mihomo.Connection) { c.Metadata["inboundName"] = "DEFAULT-TUN" }},
		{name: "explicit proxy user", mutate: func(c *mihomo.Connection) { c.Metadata["type"] = "Socks5" }},
		{name: "invalid source", mutate: func(c *mihomo.Connection) { c.Metadata["sourceIP"] = "invalid" }},
		{name: "IPv4 source on packet listener", mutate: func(c *mihomo.Connection) { c.Metadata["sourceIP"] = "192.168.1.151" }},
		{name: "MAC conflict", leases: []device.Client{{IP: "192.168.1.151", MAC: "aa:bb:cc:dd:ee:99"}}},
		{name: "IP-only registration", policy: device.PolicySet{Devices: []device.ManagedDevice{{ID: "phone", IPv4: "192.168.1.151"}}}},
		{name: "out of LAN", policy: device.PolicySet{Devices: []device.ManagedDevice{{ID: "phone", IPv4: "192.168.2.151", MAC: "aa:bb:cc:dd:ee:51"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := packetObservation("one", "device:phone", "fdfe:dcba:9878::51")
			connection.Metadata["process"] = "Safari"
			if test.mutate != nil {
				test.mutate(&connection)
			}
			policy := test.policy
			if policy.Devices == nil {
				policy = observationPolicy()
			}
			response := aggregateDeviceTrafficWithPolicy(test.leases, policy, mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{connection}}, newGatewayLocalIdentity("192.168.1.20", testSystemTUNRuntime()), 24, true)
			if response.UnclassifiedConnections != 1 || response.GatewayLocal.ActiveConnections != 0 || response.Totals.ActiveConnections != 0 {
				t.Fatalf("guessed identity: %#v", response)
			}
			assertObservationTotals(t, response)
		})
	}
}

func TestConnectionsEndpointSharesSampleAndRecoversFromErrors(t *testing.T) {
	server := newTestServer(t)
	calls := 0
	failed := false
	server.fetchConnections = func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error) {
		calls++
		if failed {
			return mihomo.ConnectionsSnapshot{}, errors.New("core unavailable")
		}
		return mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{{ID: "observed", Upload: int64(calls * 100), Metadata: map[string]any{"sourceIP": "192.168.1.151"}}}}, nil
	}
	read := func(path string) ConnectionsResponse {
		t.Helper()
		response := performAuthorized(server, http.MethodGet, path, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d %s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		var payload ConnectionsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	connections := read("/api/v1/connections")
	traffic := read("/api/v1/device-traffic")
	if calls != 1 || !connections.SampledAt.Equal(traffic.SampledAt) || len(connections.Connections) != 1 || traffic.Connections != nil {
		t.Fatalf("not shared: calls=%d details=%#v traffic=%#v", calls, connections, traffic)
	}
	if connections.Connections[0].OwnerKey != "ip:192.168.1.151" || connections.Connections[0].SourceFamily != "ipv4" {
		t.Fatalf("entry = %#v", connections.Connections[0])
	}
	failed = true
	server.connectionObservation.response.SampledAt = time.Now().Add(-2 * time.Second)
	if payload := read("/api/v1/connections"); payload.ConnectionError == "" || len(payload.Connections) != 0 {
		t.Fatalf("error response = %#v", payload)
	}
	failed = false
	server.connectionObservation.response.SampledAt = time.Now().Add(-2 * time.Second)
	if payload := read("/api/v1/connections"); payload.ConnectionError != "" || payload.GatewayRates.Upload != 0 {
		t.Fatalf("recovery baseline = %#v", payload)
	}
}

func TestConnectionsEndpointLoadsAppliedIdentityForAllTopologies(t *testing.T) {
	for _, topology := range []string{config.GatewayModeSameLAN, config.GatewayModeSameWiFiDHCP, config.GatewayModeIsolatedLAN} {
		t.Run(topology, func(t *testing.T) {
			server := newTestServer(t)
			cfg, err := config.LoadRuntime(server.configPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Gateway.Mode = topology
			cfg.DHCP.Enabled = topology != config.GatewayModeSameLAN
			if topology == config.GatewayModeIsolatedLAN {
				cfg.Gateway.UpstreamInterface = "en7"
			}
			if err := os.WriteFile(server.configPath, []byte(config.Render(cfg)), 0600); err != nil {
				t.Fatal(err)
			}
			var policy device.PolicySet
			if err := json.Unmarshal([]byte(`{"devices":[{"id":"phone","name":"Phone","mac":"aa:bb:cc:dd:ee:51","ipv4":"192.168.1.151","profile":"home","egress_mode":"inherit_global"}],"profiles":[{"id":"home","default_policies":["DIRECT"]}],"templates":[],"rule_sets":[]}`), &policy); err != nil {
				t.Fatal(err)
			}
			installAppliedPolicy(t, server, policy)
			server.fetchConnections = func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error) {
				return mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{packetObservation("one", "device:phone", "fdfe:dcba:9878::51")}}, nil
			}
			response := performAuthorized(server, http.MethodGet, "/api/v1/connections", nil)
			var payload ConnectionsResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || len(payload.Devices) != 1 || payload.Devices[0].DeviceID != "phone" || payload.Devices[0].ActiveConnections != 1 {
				t.Fatalf("topology attribution: %s", response.Body.String())
			}
		})
	}
}

func TestConnectionsEndpointRejectsUnauthenticatedReads(t *testing.T) {
	server := newTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:61767/api/v1/connections", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}
}

func TestConnectionsEndpointSerializesConcurrentConsumers(t *testing.T) {
	server := newTestServer(t)
	var calls atomic.Int32
	server.fetchConnections = func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error) {
		calls.Add(1)
		return mihomo.ConnectionsSnapshot{}, nil
	}
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			path := "/api/v1/connections"
			if i%2 == 0 {
				path = "/api/v1/device-traffic"
			}
			if response := performAuthorized(server, http.MethodGet, path, nil); response.Code != http.StatusOK {
				t.Errorf("concurrent status = %d", response.Code)
			}
		}(i)
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent consumers fetched %d snapshots", calls.Load())
	}
}

func TestConnectionObservationKeepsDormantAndPendingDevicesOutOfOwnership(t *testing.T) {
	server := newTestServer(t)
	cfg, err := config.LoadRuntime(server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	policy := device.PolicySet{
		Devices:  []device.ManagedDevice{{ID: "paused", IPv4: "192.168.1.151", Profile: "home"}},
		Profiles: []device.Profile{{ID: "home", DefaultPolicies: []string{"DIRECT"}}},
	}
	bundle, err := device.CompilePolicyBundleForIPOnlyMode(policy, false)
	if err != nil {
		t.Fatal(err)
	}
	paths := runtime.NewPaths(cfg)
	if err := device.WritePolicyBundleSnapshot(paths.DevicePolicyApplied, bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.StateFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{DevicePolicyDigest: bundle.Digest}); err != nil {
		t.Fatal(err)
	}
	policy.Devices = append(policy.Devices, device.ManagedDevice{ID: "new-phone", MAC: "aa:bb:cc:dd:ee:52", IPv4: "192.168.1.152", Profile: "home"})
	snapshot := mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{
		{ID: "v4", Upload: 20, Metadata: map[string]any{"sourceIP": "192.168.1.151"}},
		packetObservation("v6", "device:new-phone", "fdfe:dcba:9878::52"),
	}}
	traffic := aggregateDeviceTrafficWithPolicy(nil, loadAppliedTrafficPolicy(paths), snapshot, newGatewayLocalIdentity(cfg.Gateway.LANIP, mihomo.TUNRuntimeState{}), 24, true)
	appendPendingTrafficDevices(&traffic, policy, cfg)
	if len(traffic.Devices) != 2 || traffic.connectionOwners[0] != "ip:192.168.1.151" || traffic.connectionOwners[1] != unclassifiedOwner {
		t.Fatalf("dormant identity claimed traffic: %#v", traffic)
	}
	for _, row := range traffic.Devices {
		if row.ConfigurationState != "pending" {
			t.Fatalf("configuration state = %#v", row)
		}
	}
	assertObservationTotals(t, traffic)
}
