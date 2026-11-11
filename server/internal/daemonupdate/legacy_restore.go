package daemonupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

type legacyRuntime struct {
	ID       string    `json:"id"`
	DaemonID string    `json:"daemon_id"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"last_seen_at"`
}

func legacyRuntimeTimes(ctx context.Context, j *Journal) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for ws, ids := range j.Workspaces {
		raw, err := exec.CommandContext(ctx, bundleCLI(j.OldBundle), "--profile", j.Profile, "--workspace-id", ws, "runtime", "list", "--output", "json").Output()
		if err != nil {
			return nil, err
		}
		var rows []legacyRuntime
		if json.Unmarshal(raw, &rows) != nil {
			return nil, errors.New("invalid runtime response")
		}
		wanted := map[string]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		for _, row := range rows {
			if wanted[row.ID] {
				if _, ok := out[row.ID]; ok || row.DaemonID != j.DaemonID || row.Status != "online" || row.LastSeen.IsZero() {
					return nil, errors.New("restored runtime identity mismatch")
				}
				out[row.ID] = row.LastSeen
			}
		}
	}
	if len(out) != len(j.RuntimeIDs) {
		return nil, errors.New("restored runtime set incomplete")
	}
	return out, nil
}

func approvedLegacyApp(j *Journal, pid int) bool {
	if j.Legacy.RecoveryApp != nil && j.Legacy.RecoveryApp.PID == pid {
		actual, err := inspectLegacy(pid)
		return err == nil && actual == *j.Legacy.RecoveryApp
	}
	for _, old := range j.Legacy.Inventory.Apps {
		if old.PID == pid {
			actual, err := inspectLegacy(pid)
			return err == nil && actual == old
		}
	}
	return false
}
func (a *legacyLocalAdapter) stopCandidates(j *Journal) error {
	if (j.StartRequested && j.NewDaemonPID == 0) || (j.AppStartRequested && j.NewAppPID == 0) {
		return errors.New("candidate launch identity was not recorded")
	}
	if j.Legacy.CandidateStopIntents == nil {
		j.Legacy.CandidateStopIntents = map[int]bool{}
		for _, p := range j.Legacy.CandidateProcesses {
			j.Legacy.CandidateStopIntents[p.PID] = true
		}
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	known := map[int]LegacyProcessIdentity{}
	for _, p := range j.Legacy.CandidateProcesses {
		known[p.PID] = p
	}
	pids, err := enumerateLegacyApps(j.OldBundle)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if _, ok := known[pid]; !ok {
			if !approvedLegacyApp(j, pid) {
				return errors.New("unrecorded candidate App process")
			}
		}
	}
	for _, pid := range []int{j.NewAppPID, j.NewDaemonPID} {
		if pid > 0 {
			if _, ok := known[pid]; !ok {
				if (pid == j.NewDaemonPID && !j.StartRequested) || (pid == j.NewAppPID && !j.AppStartRequested) {
					return errors.New("candidate launch intent missing")
				}
				if _, err := inspectLegacy(pid); !errors.Is(err, ErrLegacyProcessExited) {
					return errors.New("candidate native identity missing")
				}
			}
		}
	}
	var targets []int
	for _, p := range j.Legacy.CandidateProcesses {
		if p.PID != j.NewDaemonPID {
			targets = append(targets, p.PID)
		}
	}
	if _, knownDaemon := known[j.NewDaemonPID]; j.NewDaemonPID > 0 && knownDaemon {
		targets = append(targets, j.NewDaemonPID)
	}
	for _, pid := range targets {
		if pid <= 0 {
			continue
		}
		p, ok := known[pid]
		if !ok || !j.Legacy.CandidateStopIntents[pid] {
			return errors.New("candidate native identity or stop intent missing")
		}
		actual, err := inspectLegacy(pid)
		if errors.Is(err, ErrLegacyProcessExited) {
			continue
		}
		if err != nil || actual != p {
			return errors.New("candidate process identity changed")
		}
		if err := stopLegacy(p); err != nil {
			return err
		}
	}
	pids, err = enumerateLegacyApps(j.OldBundle)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if !approvedLegacyApp(j, pid) {
			return errors.New("candidate App process remains or restarted")
		}
	}
	if j.NewDaemonPID > 0 {
		if _, err := inspectLegacy(j.NewDaemonPID); !errors.Is(err, ErrLegacyProcessExited) {
			return errors.New("candidate daemon remains or PID was reused")
		}
	}
	return nil
}
func (a *legacyLocalAdapter) RestoreLegacy(ctx context.Context, j *Journal) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if j.GateReleaseRequested {
		return errors.New("candidate gate release may have occurred; manual recovery required")
	}
	if err := verifyGate(j); err != nil {
		if _, e := os.Lstat(j.GatePath); !j.Legacy.RecoveryGateReleaseRequested || !os.IsNotExist(e) {
			return err
		}
	}
	if err := a.stopCandidates(j); err != nil {
		return err
	}
	original := j.OldBundle
	if verifyBundleHash(original, j.OldBinarySHA256) != nil {
		original = j.RollbackBundle
		if exists, err := exists(original); err != nil {
			return err
		} else if !exists {
			original = j.StagedBundle
		}
	}
	if err := verifyLegacyOriginalTree(ctx, original, j.Legacy.BackupManifestSHA256); err != nil {
		return err
	}
	if err := a.restoreBundles(j); err != nil {
		return err
	}
	if err := verifyLegacyOriginalTree(ctx, j.OldBundle, j.Legacy.BackupManifestSHA256); err != nil {
		return err
	}
	state, err := legacyCLIEntryState(j.CLIEntry)
	if err != nil || state != j.Legacy.CLIEntryState {
		return errors.New("legacy CLI entry changed")
	}
	v := j.Legacy.Inventory
	var daemon LegacyProcessIdentity
	if j.Legacy.RecoveryDaemon != nil {
		daemon = *j.Legacy.RecoveryDaemon
		actual, e := inspectLegacy(daemon.PID)
		if e != nil || actual != daemon {
			return errors.New("restored daemon identity changed")
		}
	} else {
		actual, e := inspectLegacy(v.Daemon.PID)
		if e == nil && actual == v.Daemon {
			daemon = actual
		} else if !errors.Is(e, ErrLegacyProcessExited) {
			return errors.New("original daemon identity unknown")
		} else {
			if j.Legacy.RecoveryStartRequested {
				return errors.New("restored daemon start identity unavailable")
			}
			j.Legacy.RecoveryStartRequested = true
			if err := SaveJournal(a.journalPath, *j); err != nil {
				return err
			}
			command := exec.Command(bundleCLI(j.OldBundle), "daemon", "start", "--foreground", "--profile", j.Profile, "--no-auto-update", "--no-auto-reload")
			command.Env = desktopEnvironment()
			log, err := os.OpenFile(filepath.Join(filepath.Dir(a.journalPath), "daemon-update.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				return err
			}
			command.Stdout = log
			command.Stderr = log
			if err := command.Start(); err != nil {
				log.Close()
				return err
			}
			log.Close()
			daemon, err = inspectLegacy(command.Process.Pid)
			if err != nil {
				return fmt.Errorf("restored daemon native identity unavailable: %w", err)
			}
			if daemon.ExecutablePath != bundleCLI(j.OldBundle) || daemon.SHA256 != j.OldBinarySHA256 {
				return errors.New("restored daemon executable mismatch")
			}
			j.Legacy.RecoveryDaemon = &daemon
			if err := SaveJournal(a.journalPath, *j); err != nil {
				return err
			}
			if err := command.Process.Release(); err != nil {
				return err
			}
		}
		j.Legacy.RecoveryDaemon = &daemon
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	dir, err := cli.ProfileDir(j.Profile)
	if err != nil {
		return err
	}
	for {
		data, e := os.ReadFile(filepath.Join(dir, "daemon.port"))
		var port struct {
			PID  int `json:"pid"`
			Port int `json:"port"`
		}
		if e == nil && json.Unmarshal(data, &port) == nil && port.PID == daemon.PID && port.Port > 0 && port.Port <= 65535 {
			j.HealthPort = port.Port
			break
		}
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
	}
	expected := j.Legacy.Inventory
	expected.Daemon = daemon
	oldInventory := j.Legacy.Inventory
	j.Legacy.Inventory = expected
	defer func() { j.Legacy.Inventory = oldInventory }()
	if _, err := a.legacyHealth(ctx, j); err != nil {
		return err
	}
	previous, err := legacyRuntimeTimes(ctx, j)
	if err != nil {
		return err
	}
	for i := 0; i < 3; {
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
		actual, err := inspectLegacy(daemon.PID)
		if err != nil || actual != daemon {
			return errors.New("restored daemon changed")
		}
		if _, err := a.legacyHealth(ctx, j); err != nil {
			return err
		}
		now, err := legacyRuntimeTimes(ctx, j)
		if err != nil {
			return err
		}
		progress := true
		for id, last := range previous {
			if !now[id].After(last) {
				progress = false
				break
			}
		}
		if progress {
			previous = now
			i++
		}
	}
	j.Legacy.Inventory = oldInventory
	if !j.Legacy.RecoveryGateReleaseRequested {
		j.Legacy.RecoveryGateReleaseRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(j.GatePath); err == nil {
		if err := releaseGate(j); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if j.Legacy.RecoveryApp != nil {
		actual, err := inspectLegacy(j.Legacy.RecoveryApp.PID)
		if err != nil || actual != *j.Legacy.RecoveryApp {
			return errors.New("restored App identity changed")
		}
		return nil
	}
	if j.Legacy.RecoveryAppOpenRequested {
		return errors.New("restored App launch identity unavailable")
	}
	var main *LegacyProcessIdentity
	for i := range v.Apps {
		if filepath.Dir(v.Apps[i].ExecutablePath) == filepath.Join(j.OldBundle, "Contents", "MacOS") {
			if main != nil {
				return errors.New("multiple legacy main Apps")
			}
			main = &v.Apps[i]
		}
	}
	if main == nil {
		return errors.New("legacy main App missing")
	}
	pids, err := enumerateLegacyApps(j.OldBundle)
	if err != nil {
		return err
	}
	if len(pids) != 0 {
		for _, pid := range pids {
			matched := false
			for _, p := range v.Apps {
				if p.PID == pid {
					actual, e := inspectLegacy(pid)
					matched = e == nil && actual == p
					break
				}
			}
			if !matched {
				return errors.New("unapproved App remains running")
			}
		}
		actual, e := inspectLegacy(main.PID)
		if e != nil || actual != *main {
			return errors.New("unapproved App remains running")
		}
		j.Legacy.RecoveryApp = &actual
		return SaveJournal(a.journalPath, *j)
	}
	j.Legacy.RecoveryAppOpenRequested = true
	if err := SaveJournal(a.journalPath, *j); err != nil {
		return err
	}
	command := exec.Command(main.ExecutablePath)
	if err := command.Start(); err != nil {
		return err
	}
	actual, err := inspectLegacy(command.Process.Pid)
	if err != nil || actual.ExecutablePath != main.ExecutablePath || actual.SHA256 != main.SHA256 {
		return errors.New("restored App executable mismatch")
	}
	j.Legacy.RecoveryApp = &actual
	if err := SaveJournal(a.journalPath, *j); err != nil {
		return err
	}
	if err := command.Process.Release(); err != nil {
		return err
	}
	return nil
}
