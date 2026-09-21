package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"open-mihomo-gateway/internal/config"
	"open-mihomo-gateway/internal/runtime"
)

func TestStatusFormatLabelsDNSOnlyMode(t *testing.T) {
	status := Status{
		Gateway:   "running",
		Interface: "en0",
		LANIP:     "192.168.1.20",
		DHCP:      "running",
	}

	got := status.Format()
	if !strings.Contains(got, "DNS: running") {
		t.Fatalf("status did not label DNS-only mode:\n%s", got)
	}
	if strings.Contains(got, "DHCP: running") {
		t.Fatalf("status incorrectly labeled DNS-only mode as DHCP:\n%s", got)
	}
}

func TestStatusFormatLabelsDHCPMode(t *testing.T) {
	status := Status{
		Gateway:     "running",
		Interface:   "en7",
		LANIP:       "192.168.50.1",
		DHCP:        "running",
		DHCPEnabled: true,
	}

	got := status.Format()
	if !strings.Contains(got, "DHCP: running") {
		t.Fatalf("status did not preserve DHCP label:\n%s", got)
	}
}

func TestStatusFormatIncludesTUNInterfaceAndError(t *testing.T) {
	status := Status{
		TUN:          "failed",
		TUNInterface: "utun7",
		TUNError:     "mihomo runtime config reports TUN disabled",
	}
	got := status.Format()
	if !strings.Contains(got, "TUN: failed (utun7): mihomo runtime config reports TUN disabled") {
		t.Fatalf("status did not expose TUN failure details:\n%s", got)
	}
}

func TestDeriveIPv4TakeoverUsesGatewayOwnershipInsteadOfRawForwarding(t *testing.T) {
	tests := []struct {
		name                          string
		gateway, runtime, pf, forward string
		want                          string
	}{
		{name: "active", gateway: "running", runtime: "active", pf: "loaded", forward: "enabled", want: "ready"},
		{name: "stopped with pre-existing forwarding", gateway: "stopped", runtime: "none", pf: "unloaded", forward: "enabled", want: "stopped"},
		{name: "missing pf", gateway: "running", runtime: "active", pf: "unloaded", forward: "enabled", want: "failed"},
		{name: "forwarding unavailable", gateway: "running", runtime: "active", pf: "loaded", forward: "unknown", want: "failed"},
		{name: "degraded gateway", gateway: "degraded", runtime: "active", pf: "loaded", forward: "enabled", want: "failed"},
		{name: "previous boot", gateway: "degraded", runtime: "interrupted", pf: "unloaded", forward: "disabled", want: "interrupted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveIPv4Takeover(tt.gateway, tt.runtime, tt.pf, tt.forward); got != tt.want {
				t.Fatalf("deriveIPv4Takeover() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveIPv6TakeoverReflectsUserspacePacketPath(t *testing.T) {
	tests := []struct {
		name                                        string
		gateway, runtime, requested, packet, reason string
		want                                        string
	}{
		{name: "disabled", gateway: "running", runtime: "active", requested: config.TUNIPv6Off, packet: "disabled", want: "disabled"},
		{name: "configured but gateway stopped", gateway: "stopped", runtime: "none", requested: config.TUNIPv6Always, packet: "stopped", reason: "stopped", want: "stopped"},
		{name: "ready", gateway: "running", runtime: "active", requested: config.TUNIPv6Always, packet: "ready", reason: "forced_userspace_packet_path", want: "ready"},
		{name: "auto waiting for upstream", gateway: "running", runtime: "active", requested: config.TUNIPv6Auto, packet: "stopped", reason: "native_ipv6_unavailable", want: "waiting"},
		{name: "packet path failed", gateway: "running", runtime: "active", requested: config.TUNIPv6Always, packet: "failed", reason: "forced_userspace_packet_path", want: "failed"},
		{name: "inconsistent disabled request with ready broker", gateway: "running", runtime: "active", requested: config.TUNIPv6Off, packet: "ready", reason: "disabled", want: "failed"},
		{name: "gateway degraded despite broker", gateway: "degraded", runtime: "active", requested: config.TUNIPv6Always, packet: "ready", reason: "forced_userspace_packet_path", want: "failed"},
		{name: "previous boot", gateway: "degraded", runtime: "interrupted", requested: config.TUNIPv6Always, packet: "stopped", reason: "forced_userspace_packet_path", want: "interrupted"},
		{name: "disabled before previous boot", gateway: "degraded", runtime: "interrupted", requested: config.TUNIPv6Off, packet: "disabled", reason: "disabled", want: "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveIPv6Takeover(tt.gateway, tt.runtime, tt.requested, tt.packet, tt.reason); got != tt.want {
				t.Fatalf("deriveIPv6Takeover() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusDegradesWhenRunningMihomoReportsTUNDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "v1.19.27", "meta": true})
		case "/configs":
			_ = json.NewEncoder(w).Encode(map[string]any{"tun": map[string]any{"enable": false, "device": "utun123"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	cfg.Mihomo.APIAddr = server.URL
	cfg.Transparent.Mode = config.TransparentModeTUN
	cfg.DHCP.Enabled = true
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDMihomo: os.Getpid(), PIDDNSMasq: os.Getpid(), StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	status, err := New(cfg).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Gateway != "degraded" || status.TUN != "failed" || status.TUNInterface != "utun123" || status.TUNError == "" {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatusKeepsGatewayRunningWhenTUNRuntimeStateIsTemporarilyUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "v1.19.27", "meta": true})
		case "/configs":
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	cfg.Mihomo.APIAddr = server.URL
	cfg.Transparent.Mode = config.TransparentModeTUN
	cfg.DHCP.Enabled = true
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDMihomo: os.Getpid(), PIDDNSMasq: os.Getpid(), StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	status, err := New(cfg).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Gateway != "running" || status.TUN != "unknown" || status.TUNError == "" {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatusExposesControllerRefusalForRunningMihomo(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	apiAddr := server.URL
	server.Close()

	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	cfg.Mihomo.APIAddr = apiAddr
	cfg.Transparent.Mode = config.TransparentModeOff
	cfg.DHCP.Enabled = true
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDMihomo: os.Getpid(), PIDDNSMasq: os.Getpid(), StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	status, err := New(cfg).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Mihomo != "running" || !strings.Contains(status.MihomoError, "connection refused") {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatusMarksPreviousBootRuntimeInterruptedWithoutProbingReusedPID(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "must not probe stale runtime", http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	cfg.Mihomo.APIAddr = server.URL
	cfg.Transparent.Mode = config.TransparentModeTUN
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{
		PIDMihomo:      os.Getpid(),
		PIDDNSMasq:     os.Getpid(),
		BootSessionID:  "previous-boot-session",
		PFAnchorLoaded: true,
		StartedAt:      time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	status, err := New(cfg).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Gateway != "degraded" || status.RuntimeState != "interrupted" || status.Mihomo != "stopped" || status.PFAnchor != "unloaded" || status.IPv4Takeover != "interrupted" || status.IPv6Takeover != "interrupted" {
		t.Fatalf("status = %#v", status)
	}
	if requests != 0 {
		t.Fatalf("stale runtime made %d mihomo API requests", requests)
	}
}
