package daemonupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func desktopEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "MULTICA_LAUNCHED_BY=") && !strings.HasPrefix(item, "MULTICA_ZH_TW_DESKTOP_SELF_RELOAD=") {
			env = append(env, item)
		}
	}
	return append(env, "MULTICA_LAUNCHED_BY=desktop")
}

var errCandidateAttestation = errors.New("candidate attestation mismatch")
var candidateVerificationTimeout = 10 * time.Minute
var requireNativeCandidate = func(j *Journal) bool { return j.Legacy != nil || runtime.GOOS == "darwin" }

func (a *localAdapter) candidateHealth(ctx context.Context, j *Journal) (health, error) {
	dir, err := cli.ProfileDir(j.Profile)
	if err != nil {
		return health{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.port"))
	if err != nil {
		return health{}, err
	}
	var record struct {
		Port int `json:"port"`
		PID  int `json:"pid"`
	}
	if err := json.Unmarshal(data, &record); err != nil || record.Port < 1 || record.Port > 65535 || record.PID <= 0 {
		return health{}, errors.New("invalid daemon port record")
	}
	candidate := *j
	candidate.HealthPort = record.Port
	h, err := a.readHealth(ctx, &candidate)
	if err != nil {
		return health{}, err
	}
	if h.PID != record.PID || h.PID == j.DaemonPID {
		return health{}, errors.New("new daemon port identity mismatch")
	}
	if j.NewDaemonPID != 0 && j.NewDaemonPID == h.PID {
		j.HealthPort = record.Port
	}
	if j.NewDaemonPID != 0 && (j.NewDaemonPID != h.PID || (j.NewDaemonStartedAt != "" && j.NewDaemonStartedAt != h.Attestation.ProcessStartedAt)) {
		return health{}, errors.New("new daemon process changed")
	}
	if err := validateNewHealth(j, h, h.PID, h.Attestation.ProcessStartedAt); err != nil {
		if j.NewDaemonPID != 0 && h.PID == j.NewDaemonPID {
			return health{}, fmt.Errorf("%w: %v", errCandidateAttestation, err)
		}
		return health{}, err
	}
	j.HealthPort = record.Port
	return h, nil
}

func (a *localAdapter) StartAndVerify(ctx context.Context, j *Journal) error {
	ctx, cancel := context.WithTimeout(ctx, candidateVerificationTimeout)
	defer cancel()
	if err := verifyGate(j); err != nil {
		return err
	}
	if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
		return err
	}
	if err := verifyBundleHash(j.RollbackBundle, j.OldBinarySHA256); err != nil {
		return err
	}
	var h health
	var err error
	var daemonDone <-chan error
	if j.StartRequested && j.NewDaemonPID == 0 {
		return errors.New("candidate daemon launch identity was not journalled; manual recovery required")
	}
	if !j.StartRequested && j.NewDaemonPID != 0 {
		return errors.New("candidate daemon launch intent missing")
	}
	if j.NewDaemonPID == 0 {
		if _, probeErr := a.candidateHealth(ctx, j); probeErr == nil {
			return errors.New("unrecorded candidate daemon is already running")
		}
		dir, dirErr := cli.ProfileDir(j.Profile)
		if dirErr != nil {
			return dirErr
		}
		pidBytes, pidErr := os.ReadFile(filepath.Join(dir, "daemon.pid"))
		if pidErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if parseErr != nil || pid <= 0 {
				return errors.New("unverifiable daemon pid")
			}
			alive, aliveErr := processExists(pid)
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
		logFile, logErr := os.OpenFile(filepath.Join(filepath.Dir(a.journalPath), "daemon-update.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if logErr != nil {
			return logErr
		}
		command.Stdout, command.Stderr = logFile, logFile
		j.StartRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			_ = logFile.Close()
			return err
		}
		if err := command.Start(); err != nil {
			logFile.Close()
			j.StartRequested = false
			if saveErr := SaveJournal(a.journalPath, *j); saveErr != nil {
				return fmt.Errorf("%v; cannot persist failed daemon launch: %w", err, saveErr)
			}
			return err
		}
		done := make(chan error, 1)
		daemonDone = done
		go func() { done <- command.Wait() }()
		j.NewDaemonPID = command.Process.Pid
		if requireNativeCandidate(j) {
			identity, identityErr := inspectLegacy(j.NewDaemonPID)
			if identityErr != nil {
				_ = logFile.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				return fmt.Errorf("candidate daemon native identity unavailable: %w", identityErr)
			}
			if identity.ExecutablePath != bundleCLI(j.OldBundle) || identity.SHA256 != j.NewBinarySHA256 {
				_ = logFile.Close()
				return errors.New("candidate daemon executable mismatch")
			}
			if j.Legacy != nil {
				j.Legacy.CandidateProcesses = append(j.Legacy.CandidateProcesses, identity)
			} else {
				j.CandidateDaemonIdentity = &identity
			}
		}
		if err := SaveJournal(a.journalPath, *j); err != nil {
			_ = command.Process.Kill()
			<-done
			_ = logFile.Close()
			return err
		}
		_ = logFile.Close()
	}
	for {
		select {
		case <-daemonDone:
			return errors.New("candidate daemon exited before readiness")
		default:
		}
		h, err = a.candidateHealth(ctx, j)
		if err == nil {
			if err = validateHeldGate(j, h); err != nil {
				return err
			}
			break
		}
		if errors.Is(err, errCandidateAttestation) || (j.NewDaemonPID != 0 && j.NewDaemonStartedAt != "") {
			return err
		}
		if j.NewDaemonPID != 0 {
			alive, aliveErr := processExists(j.NewDaemonPID)
			if aliveErr != nil {
				return aliveErr
			}
			if !alive {
				return errors.New("candidate daemon exited before readiness")
			}
		}
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
	}
	if j.NewDaemonStartedAt == "" {
		j.NewDaemonPID, j.NewDaemonStartedAt = h.PID, h.Attestation.ProcessStartedAt
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	observer, err := newHeartbeatObserver(j.RuntimeIDs, h.Attestation.RuntimeHeartbeatAcks)
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
		h, err = a.candidateHealth(ctx, j)
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
	if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
		return err
	}
	if filepath.Base(j.NewAppExecutable) != j.NewAppExecutable || j.NewAppExecutable == "." || j.NewAppExecutable == "" ||
		filepath.Clean(j.ReadyFile) != filepath.Join(filepath.Dir(a.journalPath), "ready-"+j.Token) {
		return errors.New("invalid App readiness path")
	}
	var appDone <-chan error
	if j.NewAppPID == 0 {
		if j.AppStartRequested {
			return errors.New("App launch identity was not journalled; manual recovery required")
		}
		j.AppStartRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
		command := exec.Command(filepath.Join(j.OldBundle, "Contents", "MacOS", j.NewAppExecutable), "--zh-tw-coordinated-token="+j.Token)
		if err := command.Start(); err != nil {
			j.AppStartRequested = false
			if saveErr := SaveJournal(a.journalPath, *j); saveErr != nil {
				return fmt.Errorf("%v; cannot persist failed App launch: %w", err, saveErr)
			}
			return err
		}
		done := make(chan error, 1)
		appDone = done
		go func() { done <- command.Wait() }()
		j.NewAppPID = command.Process.Pid
		if j.Legacy != nil {
			identity, identityErr := inspectLegacy(j.NewAppPID)
			if identityErr != nil {
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				return fmt.Errorf("candidate App native identity unavailable: %w", identityErr)
			}
			if identity.ExecutablePath != filepath.Join(j.OldBundle, "Contents", "MacOS", j.NewAppExecutable) {
				return errors.New("candidate App executable mismatch")
			}
			j.Legacy.CandidateProcesses = append(j.Legacy.CandidateProcesses, identity)
		}
		if err := SaveJournal(a.journalPath, *j); err != nil {
			_ = command.Process.Kill()
			<-done
			return err
		}
	}
	for {
		select {
		case <-appDone:
			return errors.New("new App exited before readiness")
		default:
		}
		if j.Legacy != nil {
			if err := a.rememberCandidateApps(ctx, j); err != nil {
				return err
			}
		}
		alive, err := processExists(j.NewAppPID)
		if err != nil {
			return err
		}
		if !alive {
			return errors.New("new App exited before readiness")
		}
		data, err := os.ReadFile(j.ReadyFile)
		if err == nil {
			var ready struct {
				Token string `json:"token"`
				PID   int    `json:"pid"`
			}
			if json.Unmarshal(data, &ready) == nil && ready.Token == j.Token && ready.PID == j.NewAppPID {
				return nil
			}
			return fmt.Errorf("App readiness identity mismatch")
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := pause(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}
