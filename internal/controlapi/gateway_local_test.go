package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"open-mihomo-gateway/internal/config"
	"open-mihomo-gateway/internal/device"
	"open-mihomo-gateway/internal/mihomo"
)

func testSystemTUNRuntime() mihomo.TUNRuntimeState {
	return mihomo.TUNRuntimeState{Enabled: true, Device: "utun123", IPv4Addresses: []string{"198.18.0.1/30"}}
}

func TestGatewayLocalIdentityUsesOnlyEffectiveTUNSources(t *testing.T) {
	for _, mode := range []struct {
		name    string
		enabled bool
		ipv6    string
	}{
		{name: "IPv4 only", enabled: true},
		{name: "fake AAAA without downstream IPv6", enabled: true, ipv6: "fdfe:dcba:9876::1/126"},
		{name: "effective host IPv6 replaces fake AAAA", enabled: true, ipv6: "fdfe:dcba:9877::1/126"},
		{name: "disabled TUN retains old addresses", ipv6: "fdfe:dcba:9877::1/126"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			tun := testSystemTUNRuntime()
			tun.Enabled = mode.enabled
			tun.IPv6Addresses = []string{mode.ipv6, "invalid", "::/126", "ff02::1/128"}
			identity := newGatewayLocalIdentity("192.168.1.20", tun)
			for _, test := range []struct {
				name     string
				metadata map[string]any
				want     bool
			}{
				{name: "TCP without process", metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "network": "tcp", "inboundName": "DEFAULT-TUN"}, want: mode.enabled},
				{name: "UDP without process", metadata: map[string]any{"sourceIP": "::ffff:198.18.0.1", "type": "Tun", "network": "udp", "inboundName": "DEFAULT-TUN"}, want: mode.enabled},
				{name: "fake AAAA", metadata: map[string]any{"sourceIP": "fdfe:dcba:9876::1", "type": "Tun", "inboundName": "DEFAULT-TUN", "process": "Safari"}, want: mode.enabled && mode.ipv6 == "fdfe:dcba:9876::1/126"},
				{name: "host IPv6", metadata: map[string]any{"sourceIP": "fdfe:dcba:9877::1", "type": "Tun", "inboundName": "DEFAULT-TUN"}, want: mode.enabled && mode.ipv6 == "fdfe:dcba:9877::1/126"},
				{name: "IPv4 subnet peer", metadata: map[string]any{"sourceIP": "198.18.0.2", "type": "Tun", "inboundName": "DEFAULT-TUN"}},
				{name: "IPv6 subnet peer", metadata: map[string]any{"sourceIP": "fdfe:dcba:9877::2", "type": "Tun", "inboundName": "DEFAULT-TUN"}},
				{name: "downstream shares TUN inbound IP and port", metadata: map[string]any{"sourceIP": "192.168.1.151", "type": "Tun", "inboundName": "DEFAULT-TUN", "inboundIP": "198.18.0.1", "inboundPort": "62550", "process": "Safari"}},
				{name: "fake destination is not local source", metadata: map[string]any{"sourceIP": "192.168.1.151", "destinationIP": "198.18.0.1", "type": "Tun", "inboundName": "DEFAULT-TUN"}},
				{name: "other listener with same source and process", metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "inboundName": "other-tun", "processPath": "/Applications/Safari"}},
				{name: "missing listener with process", metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "process": "Safari"}},
				{name: "explicit proxy cannot borrow TUN identity", metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Mixed", "process": "Safari"}},
				{name: "packet listener overrides local evidence", metadata: map[string]any{"sourceIP": "fdfe:dcba:9877::1", "type": "Tun", "inboundName": config.IPv6PacketListenerName, "process": "Safari"}},
				{name: "device user overrides local source", metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "inboundName": "DEFAULT-TUN", "inboundUser": "device:phone"}},
				{name: "device user overrides loopback", metadata: map[string]any{"sourceIP": "127.0.0.1", "type": "Mixed", "inboundUser": "device:phone", "process": "Safari"}},
				{name: "downstream IPv6 with process", metadata: map[string]any{"sourceIP": "fdfe:dcba:9878::37", "type": "Tun", "inboundName": "DEFAULT-TUN", "process": "Safari"}},
				{name: "loopback explicit proxy", metadata: map[string]any{"sourceIP": "127.0.0.1", "type": "HTTP"}, want: true},
				{name: "IPv6 loopback proxy", metadata: map[string]any{"sourceIP": "[::1]:1234", "type": "Socks5"}, want: true},
				{name: "gateway explicit proxy", metadata: map[string]any{"sourceIP": "192.168.1.20", "type": "Mixed"}, want: true},
				{name: "missing source with process", metadata: map[string]any{"process": "Safari"}},
			} {
				t.Run(test.name, func(t *testing.T) {
					if got := identity.matches(mihomo.Connection{Metadata: test.metadata}); got != test.want {
						t.Fatalf("local = %t, want %t", got, test.want)
					}
				})
			}
		})
	}
}

func TestDeviceTrafficAndRefreshShareLiveIdentityWithoutProcesses(t *testing.T) {
	server := newTestServer(t)
	live := testSystemTUNRuntime()
	live.IPv6Addresses = []string{"fdfe:dcba:9876::1/126"}
	runtimeReads := 0
	server.fetchTUNRuntime = func(context.Context, config.Config) (mihomo.TUNRuntimeState, error) {
		runtimeReads++
		return live, nil
	}
	snapshot := mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{
		{ID: "mac-tcp", Upload: 100, Download: 1000, Metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "network": "tcp", "inboundName": "DEFAULT-TUN"}},
		{ID: "mac-udp", Upload: 200, Download: 2000, Metadata: map[string]any{"sourceIP": "fdfe:dcba:9876::1", "type": "Tun", "network": "udp", "inboundName": "DEFAULT-TUN"}},
		{ID: "phone", Upload: 300, Download: 3000, Metadata: map[string]any{"sourceIP": "192.168.1.151", "type": "Tun", "inboundName": "DEFAULT-TUN", "inboundIP": "198.18.0.1"}},
		{ID: "other-listener", Upload: 400, Download: 4000, Metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "inboundName": "other-tun", "process": "Safari"}},
		{ID: "packet-device", Upload: 500, Download: 5000, Metadata: map[string]any{"sourceIP": "fdfe:dcba:9878::37", "type": "Tun", "inboundName": config.IPv6PacketListenerName, "inboundUser": "device:phone", "process": "Safari"}},
	}}
	server.fetchConnections = func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error) { return snapshot, nil }
	response := performAuthorized(server, http.MethodGet, "/api/v1/device-traffic", nil)
	var traffic DeviceTrafficResponse
	if err := json.Unmarshal(response.Body.Bytes(), &traffic); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || traffic.ConnectionError != "" || traffic.GatewayLocal.ActiveConnections != 2 || traffic.GatewayLocal.Upload != 300 || traffic.GatewayLocal.Download != 3000 || traffic.Totals.ActiveConnections != 1 || traffic.UnclassifiedConnections != 2 {
		t.Fatalf("traffic = %#v, status = %d", traffic, response.Code)
	}
	// Exact deltas verify that rates use the same owner as the cumulative row.
	identity := newGatewayLocalIdentity(traffic.GatewayLocal.IP, live)
	now := time.Now()
	sampler := newTrafficRateSampler()
	sampler.annotate(&traffic, snapshot, identity, now)
	for index := range snapshot.Connections {
		snapshot.Connections[index].Upload += 200
		snapshot.Connections[index].Download += 400
	}
	next := aggregateDeviceTrafficWithPolicy(nil, device.PolicySet{}, snapshot, identity, 24, true)
	sampler.annotate(&next, snapshot, identity, now.Add(2*time.Second))
	if next.GatewayLocal.UploadRate != 200 || next.GatewayLocal.DownloadRate != 400 || next.Totals.UploadRate != 100 || next.Totals.DownloadRate != 200 || next.GatewayRates.Upload != 500 || next.GatewayRates.Download != 1000 {
		t.Fatalf("rates = %#v", next)
	}
	var closedIDs []string
	server.closeConnections = func(_ context.Context, _ config.Config, ids []string) (int, error) {
		closedIDs = append([]string(nil), ids...)
		return len(ids), nil
	}
	response = performAuthorized(server, http.MethodPost, "/api/v1/local-routing/connections/refresh", nil)
	if response.Code != http.StatusOK || !reflect.DeepEqual(closedIDs, []string{"mac-tcp", "mac-udp"}) {
		t.Fatalf("refresh status = %d, closed = %v", response.Code, closedIDs)
	}
	if runtimeReads != 2 {
		t.Fatalf("runtime reads = %d, want one per request", runtimeReads)
	}
	// A reload replaces the effective IPv6 identity. Process metadata cannot
	// keep the old fake-AAAA identity alive or seed another listener.
	live.IPv6Addresses = []string{"fdfe:dcba:9877::1/126"}
	response = performAuthorized(server, http.MethodPost, "/api/v1/local-routing/connections/refresh", nil)
	if response.Code != http.StatusOK || !reflect.DeepEqual(closedIDs, []string{"mac-tcp"}) {
		t.Fatalf("refresh after address change: status = %d, closed = %v", response.Code, closedIDs)
	}
}

func TestGatewayLocalIdentityUnavailableReportsErrorAndDoesNotCloseConnections(t *testing.T) {
	for _, test := range []struct {
		name string
		tun  mihomo.TUNRuntimeState
		err  error
	}{
		{name: "runtime unavailable", err: errors.New("runtime unavailable")},
		{name: "runtime missing addresses", tun: mihomo.TUNRuntimeState{Enabled: true}},
		{name: "invalid runtime addresses", tun: mihomo.TUNRuntimeState{Enabled: true, IPv4Addresses: []string{"bad-prefix"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			server.fetchConnections = func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error) {
				return mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{
					{ID: "mac", Upload: 100, Metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "inboundName": "DEFAULT-TUN", "process": "Safari"}},
					{ID: "explicit", Upload: 200, Metadata: map[string]any{"sourceIP": "127.0.0.1", "type": "Mixed"}},
				}}, nil
			}
			server.fetchTUNRuntime = func(context.Context, config.Config) (mihomo.TUNRuntimeState, error) { return test.tun, test.err }
			server.closeConnections = func(context.Context, config.Config, []string) (int, error) {
				t.Fatal("must not partially close connections with unavailable TUN identity")
				return 0, nil
			}
			response := performAuthorized(server, http.MethodGet, "/api/v1/device-traffic", nil)
			var traffic DeviceTrafficResponse
			if err := json.Unmarshal(response.Body.Bytes(), &traffic); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || traffic.ConnectionError == "" || traffic.GatewayLocal.ActiveConnections != 1 || traffic.GatewayLocal.Upload != 200 || traffic.UnclassifiedConnections != 1 {
				t.Fatalf("traffic = %#v, status = %d", traffic, response.Code)
			}
			response = performAuthorized(server, http.MethodPost, "/api/v1/local-routing/connections/refresh", nil)
			if response.Code != http.StatusBadGateway {
				t.Fatalf("refresh status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestGatewayLocalIdentityUsesRuntimeAddressAndSkipsLookupWithoutTUN(t *testing.T) {
	server := newTestServer(t)
	cfg := config.Default()
	server.fetchTUNRuntime = func(context.Context, config.Config) (mihomo.TUNRuntimeState, error) {
		return mihomo.TUNRuntimeState{Enabled: true, IPv4Addresses: []string{"198.19.42.1/30"}}, nil
	}
	connection := mihomo.Connection{Metadata: map[string]any{"sourceIP": "198.19.42.1", "type": "Tun", "inboundName": "DEFAULT-TUN"}}
	identity, err := server.fetchGatewayLocalIdentity(context.Background(), cfg, mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{connection}})
	if err != nil || !identity.matches(connection) {
		t.Fatalf("live identity unavailable: %v", err)
	}
	server.fetchTUNRuntime = func(context.Context, config.Config) (mihomo.TUNRuntimeState, error) {
		t.Fatal("explicit proxy snapshot must not need a TUN runtime lookup")
		return mihomo.TUNRuntimeState{}, nil
	}
	connection.Metadata = map[string]any{"sourceIP": "127.0.0.1", "type": "Mixed"}
	identity, err = server.fetchGatewayLocalIdentity(context.Background(), cfg, mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{connection}})
	if err != nil || !identity.matches(connection) {
		t.Fatalf("explicit proxy identity unavailable: %v", err)
	}
}
