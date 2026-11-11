package daemonupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// acceptanceHealth reads the profile endpoint without weakening the strict
// startup identity check in candidateHealth.
func (a *localAdapter) acceptanceHealth(ctx context.Context, j *Journal, rollback bool) (health, error) {
	dir, err := cli.ProfileDir(j.Profile)
	if err != nil {
		return health{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.port"))
	if err != nil {
		return health{}, err
	}
	var rec struct {
		Port int `json:"port"`
		PID  int `json:"pid"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Port < 1 || rec.Port > 65535 || rec.PID <= 0 {
		return health{}, errors.New("invalid daemon port record")
	}
	expected := *j
	expected.HealthPort = rec.Port
	if rollback {
		expected.NewVersion, expected.NewCommit, expected.NewBinarySHA256 = j.OldVersion, j.OldCommit, j.OldBinarySHA256
	}
	h, err := a.readHealth(ctx, &expected)
	if err != nil {
		return health{}, err
	}
	if h.PID != rec.PID {
		return health{}, errors.New("daemon port identity mismatch")
	}
	if err = validateNewHealth(&expected, h, h.PID, h.Attestation.ProcessStartedAt); err != nil {
		return health{}, err
	}
	if !h.MaintenanceGate.Supported || h.MaintenanceGate.Blocked || h.MaintenanceGate.Path != j.GatePath {
		return health{}, errors.New("claim gate not open")
	}
	j.HealthPort = rec.Port
	return h, nil
}

func (a *localAdapter) verifyResumeAssets(ctx context.Context, j *Journal, rollback bool) error {
	if err := a.validateBundlePaths(j); err != nil {
		return err
	}
	if !sha256Hex.MatchString(j.OldTreeSHA256) || !sha256Hex.MatchString(j.CandidateTreeSHA256) {
		return errors.New("trusted bundle tree evidence missing; manual recovery required")
	}
	oldPath, newPath := j.RollbackBundle, j.OldBundle
	if rollback {
		oldPath = j.OldBundle
		newPath = ""
		for _, p := range []string{j.StagedBundle, filepath.Join(filepath.Dir(a.journalPath), "failed-"+j.Token+".app")} {
			present, err := exists(p)
			if err != nil {
				return err
			}
			if present {
				if newPath != "" {
					return errors.New("ambiguous candidate bundles")
				}
				newPath = p
			}
		}
		if newPath == "" && !j.RollbackCleanupRequested {
			return errors.New("candidate bundle missing")
		}
	}
	for _, entry := range []struct{ path, hash, tree string }{{oldPath, j.OldBinarySHA256, j.OldTreeSHA256}, {newPath, j.NewBinarySHA256, j.CandidateTreeSHA256}} {
		if entry.path == "" {
			continue
		}
		info, err := os.Lstat(entry.path)
		if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
			return errors.New("untrusted bundle")
		}
		if err := verifyBundleHash(entry.path, entry.hash); err != nil {
			return err
		}
		_, digest, err := legacyTreeManifest(entry.path)
		if err != nil || digest != entry.tree {
			return errors.New("bundle tree changed")
		}
		if err := legacyVerifySignature(ctx, entry.path); err != nil {
			return errors.New("bundle signature changed")
		}
	}
	return nil
}

func currentAcceptanceApp(ctx context.Context, j *Journal) (LegacyProcessIdentity, error) {
	executable, err := abortAppExecutable(ctx, j.OldBundle)
	if err != nil || executable == "" || filepath.Base(executable) != executable {
		return LegacyProcessIdentity{}, errors.New("App executable unavailable")
	}
	pids, err := enumerateLegacyApps(j.OldBundle)
	if err != nil {
		return LegacyProcessIdentity{}, err
	}
	path := filepath.Join(j.OldBundle, "Contents", "MacOS", executable)
	var found LegacyProcessIdentity
	for _, pid := range pids {
		identity, err := inspectLegacy(pid)
		if err != nil {
			return found, err
		}
		if identity.ExecutablePath != path {
			continue
		}
		if found.PID != 0 {
			return found, errors.New("ambiguous App processes")
		}
		if identity.PID != pid || identity.UID != os.Getuid() || identity.StartedAt == "" || identity.Inode == 0 {
			return found, errors.New("invalid App process identity")
		}
		found = identity
	}
	if found.PID == 0 {
		return found, errors.New("current App process unavailable")
	}
	return found, nil
}

// resumeAcceptance samples at most one new heartbeat per invocation. Each
// observation and epoch reset is durably saved before it can count.
func (a *localAdapter) resumeAcceptance(ctx context.Context, j *Journal, rollback bool) (bool, health, error) {
	release, intent := j.GateReleasedAt, j.GateReleaseRequested
	priorPID, priorStart, priorNative := j.NewDaemonPID, j.NewDaemonStartedAt, j.CandidateDaemonIdentity
	if !rollback && priorNative == nil && j.Legacy != nil {
		for i := range j.Legacy.CandidateProcesses {
			p := &j.Legacy.CandidateProcesses[i]
			if p.PID == priorPID && p.ExecutablePath == bundleCLI(j.OldBundle) {
				if priorNative != nil {
					return false, health{}, errors.New("ambiguous previous daemon identity")
				}
				priorNative = p
			}
		}
	}
	if rollback {
		release, intent = j.RollbackGateReleasedAt, j.RollbackGateReleaseRequested
		priorPID, priorStart, priorNative = j.RollbackDaemonPID, j.RollbackDaemonStartedAt, j.RollbackDaemonIdentity
	}
	if !intent || release.IsZero() || j.GateIdentity == "" {
		return false, health{}, errors.New("gate release evidence missing")
	}
	if _, err := os.Lstat(j.GatePath); !os.IsNotExist(err) {
		return false, health{}, errors.New("gate present or inaccessible")
	}
	h, err := a.acceptanceHealth(ctx, j, rollback)
	if err != nil {
		return false, health{}, err
	}
	native, err := inspectLegacy(h.PID)
	if err != nil {
		return false, health{}, err
	}
	expectedHash := j.NewBinarySHA256
	if rollback {
		expectedHash = j.OldBinarySHA256
	}
	if native.PID != h.PID || native.UID != os.Getuid() || native.StartedAt == "" || native.Inode == 0 || native.ExecutablePath != bundleCLI(j.OldBundle) || native.SHA256 != expectedHash {
		return false, health{}, errors.New("daemon native identity mismatch")
	}
	if priorPID != h.PID && priorPID > 0 {
		old, e := inspectLegacy(priorPID)
		if e != nil && !errors.Is(e, ErrLegacyProcessExited) {
			return false, health{}, errors.New("prior daemon identity cannot be resolved")
		}
		if e == nil && (priorNative == nil || old == *priorNative || (old.ExecutablePath == bundleCLI(j.OldBundle) && old.SHA256 == expectedHash)) {
			return false, health{}, errors.New("prior daemon may still be running")
		}
	}
	if err := a.verifyResumeAssets(ctx, j, rollback); err != nil {
		return false, health{}, err
	}
	app, err := currentAcceptanceApp(ctx, j)
	if err != nil {
		return false, health{}, err
	}
	// Reinspect after all external reads to detect PID reuse and executable swaps.
	again, err := inspectLegacy(h.PID)
	if err != nil || again != native {
		return false, health{}, errors.New("daemon changed during authentication")
	}
	appAgain, err := inspectLegacy(app.PID)
	if err != nil || appAgain != app {
		return false, health{}, errors.New("App changed during authentication")
	}
	epoch := j.AcceptanceEpoch
	changed := epoch == nil || epoch.Rollback != rollback || epoch.Daemon != native || epoch.StartedAt != h.Attestation.ProcessStartedAt || epoch.App != app || priorPID != h.PID || priorStart != h.Attestation.ProcessStartedAt
	if changed {
		epoch = &AcceptanceEpoch{Rollback: rollback, Daemon: native, App: app, StartedAt: h.Attestation.ProcessStartedAt, Since: time.Now().UTC(), Last: map[string]uint64{}, Observed: map[string]int{}}
		for _, id := range j.RuntimeIDs {
			count, ok := h.Attestation.RuntimeHeartbeatAcks[id]
			if !ok {
				return false, health{}, errors.New("heartbeat baseline missing")
			}
			epoch.Last[id] = count
		}
		j.AcceptanceEpoch = epoch
		j.AcceptanceAppIdentity = &app
		j.NewAppPID = app.PID
		if rollback {
			j.RollbackDaemonPID, j.RollbackDaemonStartedAt, j.RollbackDaemonIdentity = h.PID, h.Attestation.ProcessStartedAt, &native
		} else {
			j.NewDaemonPID, j.NewDaemonStartedAt, j.CandidateDaemonIdentity = h.PID, h.Attestation.ProcessStartedAt, &native
		}
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return false, health{}, err
		}
		return false, h, nil
	}
	if epoch.Since.IsZero() || epoch.Last == nil || epoch.Observed == nil {
		return false, health{}, errors.New("invalid acceptance epoch")
	}
	complete := true
	for _, id := range j.RuntimeIDs {
		count, ok := h.Attestation.RuntimeHeartbeatAcks[id]
		last, known := epoch.Last[id]
		if !ok || !known || count < last {
			return false, health{}, fmt.Errorf("heartbeat regressed: %s", id)
		}
		if count > last {
			epoch.Last[id] = count
			if epoch.Observed[id] < 3 {
				epoch.Observed[id]++
			}
		}
		if epoch.Observed[id] < 3 {
			complete = false
		}
	}
	if err := SaveJournal(a.journalPath, *j); err != nil {
		return false, health{}, err
	}
	return complete, h, nil
}

// Native inspection is required even when a numeric PID was reused.
var acceptanceNativeRequired = func() bool { return runtime.GOOS == "darwin" }

func (a *localAdapter) stableAcceptanceNeedsReauth(ctx context.Context, j *Journal, h health, rollback bool) (bool, error) {
	if !acceptanceNativeRequired() {
		return false, nil
	}
	native, err := inspectLegacy(h.PID)
	if err != nil {
		return false, err
	}
	hash := j.NewBinarySHA256
	previous := j.CandidateDaemonIdentity
	if rollback {
		hash, previous = j.OldBinarySHA256, j.RollbackDaemonIdentity
	}
	if previous == nil && !rollback && j.Legacy != nil {
		for i := range j.Legacy.CandidateProcesses {
			p := &j.Legacy.CandidateProcesses[i]
			if p.PID == h.PID && p.ExecutablePath == bundleCLI(j.OldBundle) {
				if previous != nil {
					return false, errors.New("ambiguous legacy daemon identity")
				}
				previous = p
			}
		}
	}
	if native.PID != h.PID || native.UID != os.Getuid() || native.StartedAt == "" || native.Inode == 0 || native.ExecutablePath != bundleCLI(j.OldBundle) || native.SHA256 != hash {
		return false, errors.New("daemon native identity mismatch")
	}
	if previous == nil || *previous != native {
		return true, nil
	}
	app, err := currentAcceptanceApp(ctx, j)
	if err != nil {
		return false, err
	}
	if j.AcceptanceAppIdentity == nil || *j.AcceptanceAppIdentity != app || j.NewAppPID != app.PID {
		return true, nil
	}
	again, err := inspectLegacy(h.PID)
	if err != nil || again != native {
		return false, errors.New("daemon changed during inspection")
	}
	return false, nil
}
