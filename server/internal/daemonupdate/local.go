package daemonupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// localAdapter uses only the selected profile's loopback control endpoint.
// The transaction engine remains disabled until every operation is wired.
type localAdapter struct {
	journalPath string
	client      *http.Client
}

func (a *localAdapter) endpoint(j *Journal, path string) (string, error) {
	if j.HealthPort < 1 || j.HealthPort > 65535 {
		return "", errors.New("invalid health port")
	}
	return "http://127.0.0.1:" + strconv.Itoa(j.HealthPort) + path, nil
}

func (a *localAdapter) readHealth(ctx context.Context, j *Journal) (health, error) {
	var result health
	url, err := a.endpoint(j, "/health")
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return result, err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return result, fmt.Errorf("health status %d", res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result); err != nil {
		return result, err
	}
	return result, nil
}

func (a *localAdapter) validateOld(j *Journal, h health) error {
	if h.Status != "running" || h.OS != "darwin" || h.LaunchedBy != "desktop" || h.LifecycleManager != "" || h.Profile != j.Profile ||
		h.DaemonID != j.DaemonID || h.PID != j.DaemonPID || h.PID <= 0 || h.Attestation.ProcessStartedAt != j.DaemonStartedAt ||
		h.Version != j.OldVersion || h.Commit != j.OldCommit || h.Attestation.BinarySHA256 != j.OldBinarySHA256 ||
		!h.MaintenanceShutdown || !h.MaintenanceGate.Supported || h.MaintenanceGate.Path != j.GatePath {
		return errors.New("old daemon is unknown or does not support coordinated shutdown")
	}
	old := *j
	old.NewVersion, old.NewCommit, old.NewBinarySHA256 = j.OldVersion, j.OldCommit, j.OldBinarySHA256
	return validateNewHealth(&old, h, j.DaemonPID, j.DaemonStartedAt)
}

func (a *localAdapter) requestStop(ctx context.Context, j *Journal) error {
	if err := verifyGate(j); err != nil {
		return err
	}
	h, err := a.readHealth(ctx, j)
	if err != nil {
		return err
	}
	if err = a.validateOld(j, h); err != nil {
		return err
	}
	if !h.MaintenanceGate.Blocked || !h.MaintenanceGate.Ready || h.MaintenanceGate.ClaimsInFlight != 0 || h.ActiveTasks != 0 {
		return errors.New("daemon has not drained")
	}
	body, err := json.Marshal(map[string]any{"expected_pid": j.DaemonPID, "process_started_at": j.DaemonStartedAt, "daemon_id": j.DaemonID, "profile": j.Profile, "gate_identity": j.GateIdentity, "gate_token": j.Token})
	if err != nil {
		return err
	}
	url, err := a.endpoint(j, "/shutdown/maintenance")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		return fmt.Errorf("maintenance shutdown refused: %d", res.StatusCode)
	}
	return nil
}

func (a *localAdapter) requestCandidateStop(ctx context.Context, j *Journal) error {
	if err := verifyGate(j); err != nil {
		return err
	}
	h, err := a.readHealth(ctx, j)
	if err != nil {
		return err
	}
	if h.PID != j.NewDaemonPID || h.PID <= 0 || h.Profile != j.Profile || h.DaemonID != j.DaemonID || h.LaunchedBy != "desktop" || h.LifecycleManager != "" || h.Attestation.ProcessStartedAt == "" ||
		(j.NewDaemonStartedAt != "" && h.Attestation.ProcessStartedAt != j.NewDaemonStartedAt) || !h.MaintenanceShutdown ||
		!h.MaintenanceGate.Blocked || !h.MaintenanceGate.Ready || h.MaintenanceGate.ClaimsInFlight != 0 || h.ActiveTasks != 0 || h.MaintenanceGate.Path != j.GatePath {
		return errors.New("candidate process identity or gate mismatch")
	}
	body, err := json.Marshal(map[string]any{"expected_pid": h.PID, "process_started_at": h.Attestation.ProcessStartedAt, "daemon_id": j.DaemonID, "profile": j.Profile, "gate_identity": j.GateIdentity, "gate_token": j.Token})
	if err != nil {
		return err
	}
	url, err := a.endpoint(j, "/shutdown/maintenance")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		return fmt.Errorf("candidate shutdown refused: %d", res.StatusCode)
	}
	return nil
}

func newLocalAdapter(path string) *localAdapter {
	return &localAdapter{journalPath: path, client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func bundleCLI(bundle string) string {
	return filepath.Join(bundle, "Contents", "Resources", "app.asar.unpacked", "resources", "bin", "multica")
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return "", errors.New("binary is not a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("binary changed while hashing")
	}
	pathInfo, err := os.Stat(path)
	if err != nil || !os.SameFile(before, pathInfo) || before.Size() != pathInfo.Size() || !before.ModTime().Equal(pathInfo.ModTime()) {
		return "", errors.New("binary path changed while hashing")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (a *localAdapter) AcquireGate(ctx context.Context, j *Journal) error {
	dir, err := cli.ProfileDir(j.Profile)
	if err != nil || j.Profile == "" {
		return errors.New("profile unresolved")
	}
	if _, err := os.Lstat(filepath.Join(dir, "daemon-management.json")); err == nil || !os.IsNotExist(err) {
		return errors.New("independently managed profile requires operator")
	}
	if err := checkSharedBundle(j.OldBundle); err != nil {
		return err
	}
	oldHash, err := hashFile(bundleCLI(j.OldBundle))
	if err != nil || oldHash != j.OldBinarySHA256 {
		return errors.New("old App binary mismatch")
	}
	// Establish the rollback tree while the original App is still trusted,
	// before any gate creation or shutdown can alter the transaction.
	if acceptanceNativeRequired() {
		if err := legacyVerifySignature(ctx, j.OldBundle); err != nil {
			return errors.New("old App signature invalid")
		}
	}
	_, oldTree, err := legacyTreeManifest(j.OldBundle)
	if err != nil {
		return err
	}
	if j.OldTreeSHA256 != "" && j.OldTreeSHA256 != oldTree {
		return errors.New("original App tree changed")
	}
	if j.OldTreeSHA256 == "" {
		if j.GateCreationIntent || j.GateIdentity != "" {
			return errors.New("trusted original tree missing after gate intent; manual recovery required")
		}
		j.OldTreeSHA256 = oldTree
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	newHash, err := hashFile(bundleCLI(j.StagedBundle))
	if err != nil || newHash != j.NewBinarySHA256 {
		return errors.New("candidate App binary mismatch")
	}
	_, tree, err := legacyTreeManifest(j.StagedBundle)
	if err != nil {
		return err
	}
	if j.CandidateTreeSHA256 != "" && j.CandidateTreeSHA256 != tree {
		return errors.New("candidate App contents changed")
	}
	if j.CandidateTreeSHA256 == "" {
		j.CandidateTreeSHA256 = tree
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	h, err := a.readHealth(ctx, j)
	if err != nil {
		return err
	}
	if err := a.validateOld(j, h); err != nil {
		return err
	}
	if j.GateIdentity == "" && h.MaintenanceGate.Blocked {
		return errors.New("maintenance gate already held")
	}
	if err := acquireGate(a.journalPath, j); err != nil {
		return err
	}
	if err := verifyGate(j); err != nil {
		return err
	}
	if filepath.Clean(j.HandoffFile) != filepath.Join(filepath.Dir(a.journalPath), "handoff-"+j.Token) {
		return errors.New("invalid handoff path")
	}
	return saveJSON(j.HandoffFile, map[string]any{"token": j.Token, "helper_pid": os.Getpid()})
}

func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *localAdapter) DrainAndStopOld(ctx context.Context, j *Journal) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		if err := verifyGate(j); err != nil {
			return err
		}
		alive, err := processExists(j.DaemonPID)
		if err != nil {
			return err
		}
		if !alive {
			if j.StopRequested {
				return nil
			}
			return errors.New("old daemon disappeared before shutdown")
		}
		h, err := a.readHealth(ctx, j)
		if err != nil {
			return err
		}
		if err := a.validateOld(j, h); err != nil {
			return err
		}
		if !h.MaintenanceGate.Blocked {
			return errors.New("gate not active")
		}
		if h.MaintenanceGate.Ready && h.MaintenanceGate.ClaimsInFlight == 0 && h.ActiveTasks == 0 {
			j.StopRequested = true
			if err := SaveJournal(a.journalPath, *j); err != nil {
				return err
			}
			if err := a.requestStop(ctx, j); err != nil {
				return err
			}
			break
		}
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
	}
	for {
		alive, err := processExists(j.DaemonPID)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		if err := pause(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}
