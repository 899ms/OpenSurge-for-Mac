package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"open-mihomo-gateway/internal/macosnetwork"
)

type dhcpProgressRunner struct {
	NetworkRunner
	probe func(context.Context, string, string, time.Duration) ([]string, error)
}

func (runner dhcpProgressRunner) ProbeDHCP(ctx context.Context, path, iface string, timeout time.Duration) ([]string, error) {
	return runner.probe(ctx, path, iface, timeout)
}

func TestRouterDHCPChecksTrackPendingRequestsAndOutcomes(t *testing.T) {
	checks := []struct {
		kind, path, stage, nextStage string
		servers                      []string
	}{
		{"dhcp-probe", "/api/v1/network/dhcp-probe", RecoveryMacStatic, RecoveryRouterDHCPDisabledConfirmed, []string{}},
		{"router-dhcp-restored", "/api/v1/recovery/router-restored", RecoveryGatewayStopped, RecoveryRouterDHCPRestored, []string{"192.168.1.1"}},
	}
	for _, check := range checks {
		for _, outcome := range []string{"success", "unexpected-offers", "probe-error", "recovery-write-error"} {
			t.Run(check.kind+"/"+outcome, func(t *testing.T) {
				server, network := newTestServerWithNetwork(t)
				state := RecoveryState{
					Stage: check.stage, Required: true,
					NetworkSnapshot: &macosnetwork.Snapshot{Interface: "en0", NetworkService: "Wi-Fi"},
				}
				if err := server.store.SaveRecovery(state); err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				server.networkRunner = dhcpProgressRunner{NetworkRunner: network, probe: func(ctx context.Context, path, iface string, timeout time.Duration) ([]string, error) {
					if path != server.configPath || iface != "en0" || timeout != 3*time.Second {
						t.Errorf("unexpected probe arguments: %s %s %s", path, iface, timeout)
					}
					close(entered)
					<-release
					if outcome == "probe-error" {
						return nil, errors.New("DHCP probe unavailable")
					}
					network.servers = check.servers
					if outcome == "unexpected-offers" {
						if len(check.servers) == 0 {
							network.servers = []string{"192.168.1.1"}
						} else {
							network.servers = nil
						}
					}
					return network.ProbeDHCP(ctx, path, iface, timeout)
				}}
				request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:61767"+check.path, nil)
				request.Header.Set("Authorization", "Bearer "+server.token)
				request.Header.Set(operationIDHeader, "dhcp-check")
				response := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { server.Handler().ServeHTTP(response, request); close(done) }()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("DHCP probe did not start")
				}
				progress := performAuthorized(server, http.MethodGet, "/api/v1/operations/dhcp-check", nil)
				var running Operation
				if err := json.Unmarshal(progress.Body.Bytes(), &running); err != nil {
					t.Fatal(err)
				}
				if progress.Code != http.StatusOK || running.Kind != check.kind || running.State != "running" || running.Phase != "probing_dhcp" || running.PhaseStartedAt.IsZero() {
					t.Fatalf("in-flight operation: %d %+v", progress.Code, running)
				}
				// Correlation permits status reads, never a second probe with this ID.
				repeated := httptest.NewRecorder()
				server.Handler().ServeHTTP(repeated, request.Clone(context.Background()))
				if repeated.Code != http.StatusConflict {
					t.Fatalf("duplicate check: %d %s", repeated.Code, repeated.Body.String())
				}
				if outcome == "recovery-write-error" {
					recoveryPath := filepath.Join(server.store.Dir(), "recovery.json")
					if err := os.Rename(recoveryPath, recoveryPath+".saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(recoveryPath, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				unblock()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("DHCP check did not finish")
				}
				if response.Header().Get(operationIDHeader) != "dhcp-check" {
					t.Fatal("response lost its correlation ID")
				}
				wantState, wantStatus := "succeeded", http.StatusOK
				switch outcome {
				case "unexpected-offers":
					wantState, wantStatus = "failed", http.StatusConflict
				case "probe-error":
					wantState, wantStatus = "failed", http.StatusBadGateway
				case "recovery-write-error":
					wantState, wantStatus = "failed", http.StatusInternalServerError
				}
				final, err := server.store.Operation("dhcp-check")
				if err != nil || final.State != wantState || final.Phase != "probing_dhcp" || response.Code != wantStatus {
					t.Fatalf("outcome: operation=%+v err=%v response=%d %s", final, err, response.Code, response.Body.String())
				}
				if outcome != "success" && (final.Error == "" || !strings.Contains(response.Body.String(), final.Error)) {
					t.Fatalf("operation lost the response error: %+v; %s", final, response.Body.String())
				}
				if outcome != "recovery-write-error" {
					recovery, err := server.store.Recovery()
					wantStage := check.stage
					if outcome == "success" {
						wantStage = check.nextStage
					}
					if err != nil || recovery.Stage != wantStage {
						t.Fatalf("recovery: %+v, %v", recovery, err)
					}
				}
			})
		}
	}
}
