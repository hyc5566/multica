package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// AgentMaintenanceStatus is diagnostic only; the claim barrier owns exclusion.
// Published snapshots are immutable so /health can read them without a lock.
type AgentMaintenanceStatus struct {
	Enabled       bool              `json:"enabled"`
	State         string            `json:"state"`
	LastAttemptAt time.Time         `json:"last_attempt_at,omitempty"`
	NextAttemptAt time.Time         `json:"next_attempt_at,omitempty"`
	Providers     map[string]string `json:"providers,omitempty"`
}

// Use calendar boundaries, not a twelve-hour duration from process startup.
// Date/AddDate also preserve local noon across daylight-saving transitions.
func nextAgentMaintenanceAt(now time.Time) time.Time {
	y, m, day := now.Date()
	noon := time.Date(y, m, day, 12, 0, 0, 0, now.Location())
	if now.Before(noon) {
		return noon
	}
	return time.Date(y, m, day, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
}

func (d *Daemon) agentMaintenanceLoop(ctx context.Context) {
	if !d.cfg.AgentAutoUpdateEnabled {
		d.agentMaintenance.Store(&AgentMaintenanceStatus{State: "disabled"})
		return
	}
	d.logger.Info("agent CLI maintenance enabled", "schedule", "startup and local 00:00/12:00", "timezone", time.Now().Location().String())
	next := time.Now()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if !time.Now().Before(next) {
			if d.tryAgentMaintenance(ctx, d.maintainAgentCLI) {
				next = nextAgentMaintenanceAt(time.Now())
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) maintainAgentCLI(ctx context.Context, provider string, entry AgentEntry) error {
	// Resolve the same installation's stable launcher even when a sibling
	// daemon already updated it and left the pinned release on disk.
	current, err := updatedAgentCLIPath(provider, entry)
	if err != nil {
		return err
	}
	entry.Path = current
	if err := updateAgentCLI(ctx, provider, entry); err != nil {
		return err
	}
	path, err := updatedAgentCLIPath(provider, entry)
	if err != nil {
		return err
	}
	outcome := d.adoptAgentPath(ctx, provider, entry.Command, path, "agent CLI maintenance completed")
	if outcome.failure != nil {
		return outcome.failure
	}
	if outcome.rejected != nil {
		return outcome.rejected
	}
	agent.InvalidateModelCache(provider, agent.Command{Path: path})
	return nil
}

func agentInstallationLockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".multica", "agent-cli-update.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// false means busy: retry at the next minute without losing the scheduled run.
// A completed round (including provider failures) waits until the next slot.
func (d *Daemon) tryAgentMaintenance(ctx context.Context, update func(context.Context, string, AgentEntry) error) bool {
	if ctx.Err() != nil {
		return false
	}
	if !d.updating.CompareAndSwap(false, true) {
		return false
	}
	defer d.updating.Store(false)
	if !d.trySetClaimBarrier() {
		d.agentMaintenance.Store(&AgentMaintenanceStatus{Enabled: true, State: "waiting_for_idle", NextAttemptAt: time.Now().Add(time.Minute)})
		return false
	}
	defer d.releaseClaimBarrier()

	// Profiles and Desktop can share one user's installation. Serialize their
	// installers with a crash-safe OS lock outside any individual profile.
	lockPath, err := agentInstallationLockPath()
	if err != nil {
		d.logger.Warn("agent CLI maintenance cannot prepare lock", "error", err)
		return false
	}
	lock, err := lockAgentMaintenance(lockPath)
	if err != nil || lock == nil {
		d.logger.Debug("agent CLI maintenance lock unavailable", "error", err)
		return false
	}
	defer lock.Close()

	started := time.Now()
	d.agentMaintenance.Store(&AgentMaintenanceStatus{Enabled: true, State: "running", LastAttemptAt: started})
	agents := d.agents()
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	results := make(map[string]string, len(names))
	for _, name := range names {
		if ctx.Err() != nil {
			return false
		}
		entry, _ := d.resolveAgentEntry(ctx, name, agents[name])
		updateCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := update(updateCtx, name, entry)
		cancel()
		switch {
		case errors.Is(err, ErrAgentUpdateUnsupported):
			results[name] = "unsupported_installation"
			d.logger.Info("agent CLI automatic update skipped", "provider", name, "reason", "unsupported installation")
		case err != nil:
			results[name] = "failed"
			d.logger.Warn("agent CLI automatic update failed", "provider", name, "error", err)
		default:
			results[name] = "completed"
			d.logger.Info("agent CLI automatic update completed", "provider", name)
		}
	}
	// A successful installer is not proof of its version. Probe and register
	// the actual executable, and retry previously unusable providers normally.
	d.refreshAgentVersions(ctx)
	d.kickAgentDiscovery()
	d.agentMaintenance.Store(&AgentMaintenanceStatus{
		Enabled: true, State: "completed", LastAttemptAt: started,
		NextAttemptAt: nextAgentMaintenanceAt(time.Now()), Providers: results,
	})
	return true
}
