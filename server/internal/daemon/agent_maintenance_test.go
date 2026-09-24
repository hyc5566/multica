package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestNextAgentMaintenanceAt(t *testing.T) {
	for _, tc := range []struct{ zone, now, want string }{
		{"Asia/Taipei", "2026-09-24T00:00:00+08:00", "2026-09-24T12:00:00+08:00"},
		{"Asia/Taipei", "2026-09-24T11:59:59+08:00", "2026-09-24T12:00:00+08:00"},
		{"Asia/Taipei", "2026-09-24T12:00:00+08:00", "2026-09-25T00:00:00+08:00"},
		{"America/New_York", "2026-03-08T00:00:00-05:00", "2026-03-08T12:00:00-04:00"},
		{"America/New_York", "2026-11-01T00:00:00-04:00", "2026-11-01T12:00:00-05:00"},
	} {
		loc, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		now, err := time.Parse(time.RFC3339, tc.now)
		if err != nil {
			t.Fatal(err)
		}
		if got := nextAgentMaintenanceAt(now.In(loc)).Format(time.RFC3339); got != tc.want {
			t.Errorf("next(%s) = %s, want %s", tc.now, got, tc.want)
		}
	}
}

func TestAgentMaintenanceDefersAndReleasesBarrier(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	d := &Daemon{logger: slog.Default(), agentDiscoveryKick: make(chan struct{}, 1)}
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte("fake, never executed"), 0o755); err != nil {
		t.Fatal(err)
	}
	d.cfg.Agents = map[string]AgentEntry{"codex": {Path: path}, "claude": {Path: path}, "unknown": {Path: path}}
	var called []string
	update := func(ctx context.Context, provider string, _ AgentEntry) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded update")
		}
		if d.tryEnterClaim() {
			t.Fatal("task claims admitted during update")
		}
		if !d.updating.Load() {
			t.Fatal("self-update not excluded")
		}
		called = append(called, provider)
		switch provider {
		case "claude":
			return errors.New("fixture failure")
		case "unknown":
			return ErrAgentUpdateUnsupported
		}
		return nil
	}
	d.activeTasks.Store(1)
	if d.tryAgentMaintenance(context.Background(), update) {
		t.Fatal("did not defer active task")
	}
	d.activeTasks.Store(0)
	d.claimsInFlight = 1
	if d.tryAgentMaintenance(context.Background(), update) {
		t.Fatal("did not defer in-flight claim")
	}
	d.claimsInFlight = 0
	d.updating.Store(true)
	if d.tryAgentMaintenance(context.Background(), update) {
		t.Fatal("did not defer self-update")
	}
	d.updating.Store(false)
	if len(called) != 0 {
		t.Fatal("updated while busy")
	}
	if !d.tryAgentMaintenance(context.Background(), update) {
		t.Fatal("idle round not completed")
	}
	if !reflect.DeepEqual(called, []string{"claude", "codex", "unknown"}) {
		t.Fatalf("providers = %v", called)
	}
	if d.pauseClaims || d.updating.Load() {
		t.Fatal("barrier leaked after failure")
	}
	status := d.agentMaintenance.Load()
	if status.Providers["claude"] != "failed" || status.Providers["codex"] != "completed" || status.Providers["unknown"] != "unsupported_installation" {
		t.Fatalf("status = %+v", status)
	}
	select {
	case <-d.agentDiscoveryKick:
	default:
		t.Fatal("version convergence not requested")
	}
}

func TestAgentMaintenanceLockAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	one, err := lockAgentMaintenance(path)
	if err != nil || one == nil {
		t.Fatalf("first lock: %v", err)
	}
	two, err := lockAgentMaintenance(path)
	if err != nil || two != nil {
		t.Fatalf("second lock should defer: %v", err)
	}
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	two, err = lockAgentMaintenance(path)
	if err != nil || two == nil {
		t.Fatalf("released lock not reusable: %v", err)
	}
	two.Close()
	d := &Daemon{logger: slog.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d.tryAgentMaintenance(ctx, func(context.Context, string, AgentEntry) error { t.Fatal("update after cancellation"); return nil }) {
		t.Fatal("cancelled round marked completed")
	}
	d.agentMaintenanceLoop(context.Background())
	if status := d.agentMaintenance.Load(); status.Enabled || status.State != "disabled" {
		t.Fatalf("status = %+v", status)
	}
}

func TestAgentInstallationLeaseProtectsSiblingTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	one, err := lockAgentInstallation(path, false)
	if err != nil || one == nil {
		t.Fatalf("first task lease: %v", err)
	}
	two, err := lockAgentInstallation(path, false)
	if err != nil || two == nil {
		t.Fatalf("parallel task lease: %v", err)
	}
	assertUpdateBlocked := func() {
		t.Helper()
		lock, err := lockAgentMaintenance(path)
		if err != nil || lock != nil {
			t.Fatalf("update admitted while sibling task owns lease: %v", err)
		}
	}
	assertUpdateBlocked()
	one.Close()
	assertUpdateBlocked()
	two.Close()
	update, err := lockAgentMaintenance(path)
	if err != nil || update == nil {
		t.Fatalf("idle update lease: %v", err)
	}
	defer update.Close()
	if task, err := lockAgentInstallation(path, false); err != nil || task != nil {
		t.Fatalf("task admitted during sibling update: %v", err)
	}
}

func TestLoadConfigAgentAutoUpdate(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("SHELL", "/usr/bin/fish")
	for _, value := range []string{"", "false", "true"} {
		t.Setenv("MULTICA_AGENT_AUTO_UPDATE", value)
		cfg, err := LoadConfig(Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AgentAutoUpdateEnabled != (value != "false") {
			t.Fatalf("env %q: enabled=%v", value, cfg.AgentAutoUpdateEnabled)
		}
	}
}

func TestAgentMaintenanceLoopRunsAtDesktopStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	d := &Daemon{cfg: Config{AgentAutoUpdateEnabled: true, LaunchedBy: "desktop"}, logger: slog.Default()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	d.agentMaintenanceLoop(ctx)
	status := d.agentMaintenance.Load()
	if status == nil || status.State != "completed" || status.LastAttemptAt.IsZero() {
		t.Fatalf("startup round did not complete before first timer: %+v", status)
	}
	if !status.NextAttemptAt.After(status.LastAttemptAt) {
		t.Fatalf("next local schedule not recorded: %+v", status)
	}
}

func TestMaintainAgentCLIAdoptsRetainedNativeRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX native fixture")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	launcher := filepath.Join(home, ".local", "bin", "claude")
	newPath := updateFixture(t, filepath.Join(versions, "2.2.0"), "#!/bin/sh\necho '2.2.0 (Claude Code)'\n")
	oldPath := updateFixture(t, filepath.Join(versions, "2.1.0"), "#!/bin/sh\nif [ \"$1\" = update ]; then /bin/ln -sfn '"+newPath+"' '"+launcher+"'; else echo '2.1.0 (Claude Code)'; fi\n")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldPath, launcher); err != nil {
		t.Fatal(err)
	}
	d := newSelfHealTestDaemon()
	entry := AgentEntry{Path: oldPath, Command: launcher}
	if err := d.maintainAgentCLI(context.Background(), "claude", entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("fixture must preserve previous version")
	}
	got, version := d.resolveAgentEntry(context.Background(), "claude", entry)
	if got.Path != newPath || version != "2.2.0 (Claude Code)" {
		t.Fatalf("next launch stayed stale: %+v %q", got, version)
	}
}

func TestAgentInstallationReleaseSurvivesInheritedDescriptor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX descriptor inheritance")
	}
	path := filepath.Join(t.TempDir(), "lease")
	lease, err := lockAgentInstallation(path, false)
	if err != nil || lease == nil {
		t.Fatalf("lease: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, "/bin/sh", "-c", "read signal")
	child.ExtraFiles = []*os.File{lease}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); _ = child.Wait() }()
	releaseAgentInstallation(lease)
	update, err := lockAgentMaintenance(path)
	if err != nil || update == nil {
		t.Fatalf("child inherited descriptor retained parent lease: %v", err)
	}
	releaseAgentInstallation(update)
}
