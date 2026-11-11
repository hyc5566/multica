package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func gateTestDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	cfg := Config{Profile: "test", WorkspacesRoot: filepath.Join(root, "workspaces")}
	return New(cfg, slog.Default()), filepath.Join(root, "profiles", "test", "daemon.maintenance")
}

func TestMaintenanceGateLifecycle(t *testing.T) {
	d, gate := gateTestDaemon(t)
	if !d.tryEnterClaim() {
		t.Fatal("claim refused before gate")
	}
	if err := os.MkdirAll(gate, 0700); err != nil {
		t.Fatal(err)
	}
	if d.tryEnterClaim() {
		t.Fatal("claim admitted after gate")
	}
	if s := d.maintenanceGate(); !s.Blocked || s.Ready || s.ClaimsInFlight != 1 {
		t.Fatalf("in-flight status: %+v", s)
	}
	// The task is counted before the in-flight claim exits.
	d.activeTasks.Add(1)
	d.exitClaim()
	if s := d.maintenanceGate(); s.Ready || s.ClaimsInFlight != 0 {
		t.Fatalf("handoff status: %+v", s)
	}
	d.activeTasks.Add(-1)
	if s := d.maintenanceGate(); !s.Supported || !s.Blocked || !s.Ready || s.Path != gate {
		t.Fatalf("idle status: %+v", s)
	}
	if d.trySetClaimBarrier() {
		t.Fatal("barrier bypassed gate")
	}
	if got := d.tryBeginServerUpdate(context.Background()); got != serverUpdateRuntimeBusy {
		t.Fatalf("update acquired through gate: %v", got)
	}
	if d.updating.Load() {
		t.Fatal("update flag leaked")
	}
	restarted := New(d.cfg, slog.Default())
	if restarted.tryEnterClaim() {
		t.Fatal("new daemon bypassed gate")
	}
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	if s := d.maintenanceGate(); s.Blocked || s.Ready {
		t.Fatalf("removed status: %+v", s)
	}
	if !d.tryEnterClaim() {
		t.Fatal("claim not restored")
	}
	d.exitClaim()
	if !d.trySetClaimBarrier() {
		t.Fatal("barrier not restored")
	}
	d.releaseClaimBarrier()
}

func TestMaintenanceGateFailsClosed(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "parent-file"} {
		t.Run(kind, func(t *testing.T) {
			d, gate := gateTestDaemon(t)
			parent := filepath.Dir(gate)
			if kind == "parent-file" {
				if err := os.MkdirAll(filepath.Dir(parent), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(parent, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "file" {
					if err := os.WriteFile(gate, nil, 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(t.TempDir(), gate); err != nil {
					t.Fatal(err)
				}
			}
			if s := d.maintenanceGate(); !s.Blocked || s.Ready {
				t.Fatalf("invalid gate: %+v", s)
			}
			if d.tryEnterClaim() || d.trySetClaimBarrier() {
				t.Fatal("invalid gate admitted work")
			}
			if d.tryBeginServerUpdate(context.Background()) != serverUpdateRuntimeBusy {
				t.Fatal("invalid gate admitted update")
			}
		})
	}
}

func TestMaintenanceGateHealthAndExistingBarrier(t *testing.T) {
	d, gate := gateTestDaemon(t)
	if !d.trySetClaimBarrier() {
		t.Fatal("barrier refused")
	}
	if err := os.MkdirAll(gate, 0700); err != nil {
		t.Fatal(err)
	}
	if s := d.maintenanceGate(); s.Ready {
		t.Fatal("existing barrier must finish before maintenance")
	}
	d.releaseClaimBarrier()
	d.updating.Store(true)
	if s := d.maintenanceGate(); s.Ready {
		t.Fatal("update in progress reported ready")
	}
	d.updating.Store(false)
	w := httptest.NewRecorder()
	d.healthHandler(time.Now())(w, httptest.NewRequest("GET", "/health", nil))
	var result struct {
		Gate maintenanceGateStatus `json:"maintenance_gate"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Gate.Supported || !result.Gate.Ready || !result.Gate.Blocked || result.Gate.Path != gate {
		t.Fatalf("health gate: %+v", result.Gate)
	}
}
