package daemonupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

var rollbackProcessExists = processExists
var rollbackProcessStarted = func(int) {}

func (a *localAdapter) Rollback(ctx context.Context, j *Journal) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := a.validateBundlePaths(j); err != nil {
		return err
	}
	if err := verifyGate(j); err != nil {
		if _, statErr := os.Lstat(j.GatePath); !j.RollbackGateReleaseRequested || !os.IsNotExist(statErr) {
			return err
		}
		return a.finishRollback(ctx, j, j.RollbackDaemonPID, j.RollbackDaemonStartedAt)
	}
	oldAlive, err := rollbackProcessExists(j.DaemonPID)
	if err != nil {
		return err
	}
	if oldAlive {
		if j.SwapStep != 0 || verifyBundleHash(j.OldBundle, j.OldBinarySHA256) != nil {
			return errors.New("old daemon remains live after bundle change")
		}
		h, err := a.readHealth(ctx, j)
		if err != nil {
			return err
		}
		if err := a.validateOld(j, h); err != nil {
			return err
		}
		if err := a.observeRollbackHeartbeats(ctx, j, h); err != nil {
			return err
		}
		return a.finishRollback(ctx, j, h.PID, h.Attestation.ProcessStartedAt)
	}
	if j.AppStartRequested && j.NewAppPID == 0 {
		return errors.New("App launch identity unavailable; manual recovery required")
	}
	if j.NewAppPID > 0 {
		alive, err := rollbackProcessExists(j.NewAppPID)
		if err != nil {
			return err
		}
		if alive {
			stopPath := filepath.Join(filepath.Dir(a.journalPath), "stop-"+j.Token)
			if err := os.WriteFile(stopPath, []byte(j.Token), 0600); err != nil {
				return err
			}
			waitCtx, stopWait := context.WithTimeout(ctx, 30*time.Second)
			for alive {
				if err := pause(waitCtx, 200*time.Millisecond); err != nil {
					stopWait()
					return errors.New("new App did not stop; manual recovery required")
				}
				alive, err = rollbackProcessExists(j.NewAppPID)
				if err != nil {
					stopWait()
					return err
				}
			}
			stopWait()
		}
	}
	if j.NewDaemonPID > 0 {
		alive, err := rollbackProcessExists(j.NewDaemonPID)
		if err != nil {
			return err
		}
		if alive {
			if err := a.stopCandidateSafely(ctx, j); err != nil {
				return err
			}
			for alive {
				if err := pause(ctx, 200*time.Millisecond); err != nil {
					return err
				}
				alive, err = rollbackProcessExists(j.NewDaemonPID)
				if err != nil {
					return err
				}
			}
		}
	} else if j.StartRequested {
		return errors.New("candidate daemon identity was not journalled; manual recovery required")
	}
	if err := a.restoreBundles(j); err != nil {
		return err
	}
	freshStart := !j.RollbackStartRequested
	if !freshStart && j.RollbackDaemonPID == 0 {
		return errors.New("restored daemon launch identity was not journalled; manual recovery required")
	}
	if freshStart && j.RollbackDaemonPID != 0 {
		return errors.New("restored daemon launch intent missing")
	}
	restored := *j
	restored.NewVersion, restored.NewCommit, restored.NewBinarySHA256 = j.OldVersion, j.OldCommit, j.OldBinarySHA256
	if freshStart {
		dir, err := cli.ProfileDir(j.Profile)
		if err != nil {
			return err
		}
		pidBytes, pidErr := os.ReadFile(filepath.Join(dir, "daemon.pid"))
		if pidErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if parseErr != nil || pid <= 0 {
				return errors.New("unverifiable restored daemon PID")
			}
			alive, aliveErr := rollbackProcessExists(pid)
			if aliveErr != nil {
				return aliveErr
			}
			if alive {
				return errors.New("unrecorded daemon is still running; manual recovery required")
			}
		} else if !os.IsNotExist(pidErr) {
			return pidErr
		}
		command := exec.Command(bundleCLI(j.OldBundle), "daemon", "start", "--foreground", "--profile", j.Profile)
		command.Env = desktopEnvironment()
		logFile, err := os.OpenFile(filepath.Join(filepath.Dir(a.journalPath), "daemon-update.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		command.Stdout, command.Stderr = logFile, logFile
		j.RollbackStartRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			_ = logFile.Close()
			return err
		}
		if err := command.Start(); err != nil {
			logFile.Close()
			j.RollbackStartRequested = false
			if saveErr := SaveJournal(a.journalPath, *j); saveErr != nil {
				return fmt.Errorf("%v; cannot persist failed rollback launch: %w", err, saveErr)
			}
			return err
		}
		rollbackProcessStarted(command.Process.Pid)
		j.RollbackDaemonPID = command.Process.Pid
		if requireNativeCandidate(j) {
			identity, identityErr := inspectLegacy(j.RollbackDaemonPID)
			if identityErr != nil || identity.ExecutablePath != bundleCLI(j.OldBundle) || identity.SHA256 != j.OldBinarySHA256 {
				logFile.Close()
				_ = command.Process.Release()
				return errors.New("restored daemon native identity unavailable")
			}
			j.RollbackDaemonIdentity = &identity
		}
		if err := SaveJournal(a.journalPath, *j); err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			_ = logFile.Close()
			return err
		}
		_ = logFile.Close()
		if err := command.Process.Release(); err != nil {
			return err
		}
	} else if requireNativeCandidate(j) {
		if j.RollbackDaemonIdentity == nil || j.RollbackDaemonIdentity.PID != j.RollbackDaemonPID {
			return errors.New("restored daemon native identity was not journalled")
		}
		actual, err := inspectLegacy(j.RollbackDaemonPID)
		if err != nil || actual != *j.RollbackDaemonIdentity {
			return errors.New("restored daemon native identity changed")
		}
	}
	restored.NewDaemonPID, restored.NewDaemonStartedAt = j.RollbackDaemonPID, j.RollbackDaemonStartedAt
	// The restored daemon must prove the same workspace/runtime identity and
	// three new acknowledgements before claims resume.
	var initial health
	for {
		initial, err = a.candidateHealth(ctx, &restored)
		if err == nil {
			if err = validateHeldGate(j, initial); err != nil {
				return err
			}
			break
		}
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
	}
	restored.NewDaemonPID, restored.NewDaemonStartedAt = initial.PID, initial.Attestation.ProcessStartedAt
	j.RollbackDaemonStartedAt = initial.Attestation.ProcessStartedAt
	if err := SaveJournal(a.journalPath, *j); err != nil {
		return err
	}
	observer, err := newHeartbeatObserver(j.RuntimeIDs, initial.Attestation.RuntimeHeartbeatAcks)
	if err != nil {
		return err
	}
	for {
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
		h, err := a.candidateHealth(ctx, &restored)
		if err != nil {
			return err
		}
		if err := validateHeldGate(j, h); err != nil {
			return err
		}
		done, err := observer.observe(h.Attestation.RuntimeHeartbeatAcks)
		if err != nil {
			return err
		}
		if done {
			break
		}
	}
	j.HealthPort = restored.HealthPort
	return a.finishRollback(ctx, j, initial.PID, initial.Attestation.ProcessStartedAt)
}

func (a *localAdapter) stopCandidateSafely(ctx context.Context, j *Journal) error {
	if err := a.requestCandidateStop(ctx, j); err != nil {
		if j.CandidateDaemonIdentity == nil || j.CandidateDaemonIdentity.PID != j.NewDaemonPID {
			return fmt.Errorf("cannot safely stop candidate daemon: %w", err)
		}
		if gateErr := verifyGate(j); gateErr != nil {
			return fmt.Errorf("cannot safely stop candidate daemon: %w", gateErr)
		}
		actual, inspectErr := inspectLegacy(j.NewDaemonPID)
		if inspectErr != nil || actual != *j.CandidateDaemonIdentity {
			return fmt.Errorf("candidate native identity changed: %w", err)
		}
		if stopErr := stopLegacy(actual); stopErr != nil {
			return fmt.Errorf("cannot stop verified candidate daemon: %w", stopErr)
		}
	}
	return nil
}

var reopenBundle = func(bundle string) error { return exec.Command("/usr/bin/open", "--", bundle).Start() }

func (a *localAdapter) observeRollbackHeartbeats(ctx context.Context, j *Journal, initial health) error {
	observer, err := newHeartbeatObserver(j.RuntimeIDs, initial.Attestation.RuntimeHeartbeatAcks)
	if err != nil {
		return err
	}
	for {
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
		if err := verifyGate(j); err != nil {
			return err
		}
		h, err := a.readHealth(ctx, j)
		if err != nil {
			return err
		}
		if err := a.validateOld(j, h); err != nil {
			return err
		}
		if !h.MaintenanceGate.Blocked {
			return errors.New("rollback gate is not held")
		}
		done, err := observer.observe(h.Attestation.RuntimeHeartbeatAcks)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func (a *localAdapter) finishRollback(ctx context.Context, j *Journal, pid int, started string) error {
	if pid <= 0 || started == "" {
		return errors.New("restored daemon identity unavailable")
	}
	j.RollbackDaemonPID, j.RollbackDaemonStartedAt = pid, started
	j.RollbackGateReleaseRequested = true
	if err := SaveJournal(a.journalPath, *j); err != nil {
		return err
	}
	if _, err := os.Lstat(j.GatePath); err == nil {
		if err := releaseGate(j); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := syncDir(filepath.Dir(j.GatePath)); err != nil {
		return err
	}
	if j.RollbackGateReleasedAt.IsZero() {
		j.RollbackGateReleasedAt = time.Now().UTC()
	}
	if err := SaveJournal(a.journalPath, *j); err != nil {
		return err
	}
	stopPath := filepath.Join(filepath.Dir(a.journalPath), "stop-"+j.Token)
	if data, err := os.ReadFile(stopPath); err == nil && string(data) == j.Token {
		if err := os.Remove(stopPath); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	alive, err := rollbackProcessExists(j.AppPID)
	if err != nil {
		return err
	}
	if !alive && !j.RollbackReopenRequested {
		j.RollbackReopenRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
		if err := reopenBundle(j.OldBundle); err != nil {
			j.RollbackReopenRequested = false
			if saveErr := SaveJournal(a.journalPath, *j); saveErr != nil {
				return fmt.Errorf("%v; cannot persist reopen failure: %w", err, saveErr)
			}
			return err
		}
	}
	return ctx.Err()
}

func (a *localAdapter) restoreBundles(j *Journal) error {
	if j.OldBinarySHA256 == j.NewBinarySHA256 {
		return errors.New("identical old and candidate hashes require manual bundle recovery")
	}
	current, err := exists(j.OldBundle)
	if err != nil || !current {
		return errors.New("current bundle missing; manual recovery required")
	}
	rollback, err := exists(j.RollbackBundle)
	if err != nil {
		return err
	}
	if verifyBundleHash(j.OldBundle, j.OldBinarySHA256) != nil {
		if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
			return err
		}
		oldPath := j.RollbackBundle
		if !rollback {
			oldPath = j.StagedBundle
		}
		if err := verifyBundleHash(oldPath, j.OldBinarySHA256); err != nil {
			return err
		}
		currentInfo, err := os.Lstat(j.OldBundle)
		if err != nil {
			return err
		}
		oldInfo, err := os.Lstat(oldPath)
		if err != nil {
			return err
		}
		currentID, err := gateIdentity(currentInfo)
		if err != nil {
			return err
		}
		oldID, err := gateIdentity(oldInfo)
		if err != nil {
			return err
		}
		if currentID != j.StagedIdentity || oldID != j.CurrentIdentity {
			return errors.New("rollback bundle inode mismatch")
		}
		if err := exchangeBundles(j.OldBundle, oldPath); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(j.OldBundle)); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(oldPath)); err != nil {
			return err
		}
	}
	if rollback && verifyBundleHash(j.RollbackBundle, j.NewBinarySHA256) == nil {
		failed := filepath.Join(filepath.Dir(a.journalPath), "failed-"+j.Token+".app")
		if present, err := exists(failed); err != nil || present {
			return errors.New("failed bundle destination occupied")
		}
		if err := moveBundle(j.RollbackBundle, failed); err != nil {
			return err
		}
		rollback = false
	}
	older, err := exists(j.OlderBundle)
	if err != nil {
		return err
	}
	if older {
		if rollback {
			return errors.New("previous rollback destination occupied")
		}
		if err := verifyBundleHash(j.OlderBundle, j.PreviousRollbackSHA256); err != nil {
			return err
		}
		_, digest, err := legacyTreeManifest(j.OlderBundle)
		if err != nil || !sha256Hex.MatchString(j.PreviousRollbackTreeSHA256) || digest != j.PreviousRollbackTreeSHA256 {
			return errors.New("previous rollback tree mismatch")
		}
		if err := moveBundle(j.OlderBundle, j.RollbackBundle); err != nil {
			return err
		}
	}
	if err := verifyBundleHash(j.OldBundle, j.OldBinarySHA256); err != nil {
		return err
	}
	return nil
}
