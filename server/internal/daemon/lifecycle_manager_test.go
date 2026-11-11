package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestPinnedDaemonRejectsEveryInternalBinaryRestart(t *testing.T) {
	t.Setenv("MULTICA_ZH_TW_DESKTOP_SELF_RELOAD", "1")
	d, calls := newSelfReloadTestDaemon(t, "old")
	d.cfg.LifecycleManager = LaunchdPinnedManager
	d.cfg.LaunchedBy = "desktop"
	d.cfg.AutoReloadEnabled = true
	d.cfg.AutoUpdateEnabled = true
	previous := fetchLatestRelease
	fetchLatestRelease = func() (*cli.GitHubRelease, error) {
		t.Fatal("pinned daemon queried release metadata")
		return nil, nil
	}
	t.Cleanup(func() { fetchLatestRelease = previous })
	d.trySelfReload(context.Background())
	d.tryAutoUpdate(context.Background())
	d.autoUpdateLoop(context.Background())
	if calls.Load() != 0 || d.zhTWDesktopSelfReload() {
		t.Fatal("pinned daemon must not follow the App or its on-disk binary")
	}
	rec := httptest.NewRecorder()
	d.idleRestartHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/restart/idle", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict || calls.Load() != 0 {
		t.Fatal("trust refresh must not restart pinned daemon")
	}
}

func TestPinnedDaemonReportsServerUpdateRefusal(t *testing.T) {
	d, calls := updateReportDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		var report map[string]any
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		if report["status"] != "failed" || !strings.Contains(report["error"].(string), "pinned") {
			t.Error("server must receive an explicit pinned maintenance refusal")
		}
		w.WriteHeader(http.StatusOK)
	})
	d.cfg.LifecycleManager = LaunchdPinnedManager
	d.runUpdateFn = func(string) (string, error) {
		t.Fatal("server update attempted binary replacement")
		return "", nil
	}
	d.handleUpdate(context.Background(), "runtime-test", &PendingUpdate{ID: "update-test"})
	if atomic.LoadInt32(calls) != 1 || d.updating.Load() || d.pauseClaims {
		t.Fatal("refusal must report without taking the update/claim barrier")
	}
}

func TestPinnedDaemonHealthAdvertisesConnectOnlyProtocol(t *testing.T) {
	d := &Daemon{cfg: Config{LifecycleManager: LaunchdPinnedManager}, workspaces: map[string]*workspaceState{}, logger: slog.Default()}
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var health HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.LifecycleManager != LaunchdPinnedManager || health.DesktopProtocol != 1 || health.ZhTWSelfReload {
		t.Fatalf("unexpected pinned health contract: %s", rec.Body.String())
	}
}

func TestPinnedConfigCannotEnableSelfReplacement(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_DAEMON_SUPERVISOR", LaunchdPinnedManager)
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "true")
	t.Setenv("MULTICA_DAEMON_AUTO_RELOAD", "true")
	cfg, err := LoadConfig(Overrides{ServerURL: "https://api.multica.ai", WorkspacesRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoUpdateEnabled || cfg.AutoReloadEnabled || cfg.LifecycleManager != LaunchdPinnedManager {
		t.Fatal("pinning must take precedence over affirmative update/reload env")
	}
}
