package daemonupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type legacyLocalAdapter struct{ *localAdapter }

var inspectLegacy = InspectLegacyProcess
var stopLegacy = StopLegacyProcess
var enumerateLegacyApps = legacyAppPIDs

func (a *legacyLocalAdapter) legacyHealth(ctx context.Context, j *Journal) (bool, error) {
	url, err := a.endpoint(j, "/health")
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, errors.New("legacy daemon health unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return false, errors.New("invalid legacy health")
	}
	return validateLegacyHealth(data, j.Legacy.Inventory)
}

func (a *legacyLocalAdapter) StopOld(ctx context.Context, j *Journal) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := verifyGate(j); err != nil {
		return err
	}
	v := j.Legacy.Inventory
	if len(j.Legacy.StopIntents) == 0 {
		pids, err := enumerateLegacyApps(v.Bundle)
		if err != nil {
			return err
		}
		if err := verifyLegacyProcesses(v, pids, inspectLegacy); err != nil {
			return err
		}
		for {
			idle, err := a.legacyHealth(ctx, j)
			if err != nil {
				return err
			}
			if idle {
				break
			}
			if err := pause(ctx, time.Second); err != nil {
				return err
			}
		}
		// App termination may also stop its child daemon; persist every intent first.
		j.Legacy.StopIntents = make(map[int]bool, len(v.Apps)+1)
		j.Legacy.PossibleMissedClaims = true
		for _, p := range v.Apps {
			j.Legacy.StopIntents[p.PID] = true
		}
		j.Legacy.StopIntents[v.Daemon.PID] = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	expected := make(map[int]bool, len(v.Apps))
	for _, p := range v.Apps {
		expected[p.PID] = true
	}
	pids, err := enumerateLegacyApps(v.Bundle)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if !expected[pid] {
			return errors.New("unapproved App process appeared")
		}
	}
	targets := append(append([]LegacyProcessIdentity{}, v.Apps...), v.Daemon)
	for _, p := range targets {
		if !j.Legacy.StopIntents[p.PID] {
			return errors.New("legacy stop intent missing")
		}
		actual, err := inspectLegacy(p.PID)
		if errors.Is(err, ErrLegacyProcessExited) {
			continue
		}
		if err != nil || actual != p {
			return errors.New("legacy process changed before stop")
		}
		if p.PID == v.Daemon.PID {
			idle, err := a.legacyHealth(ctx, j)
			if err != nil {
				return err
			}
			if !idle {
				return errors.New("legacy daemon has active work")
			}
		}
		if err := stopLegacy(p); err != nil {
			return err
		}
	}
	pids, err = enumerateLegacyApps(v.Bundle)
	if err != nil {
		return err
	}
	if len(pids) != 0 {
		return errors.New("App restarted during legacy handoff")
	}
	return nil
}

// These methods refuse until the corresponding recovery-safe transaction
// steps are implemented. No caller may bypass them by advancing the journal.

func legacyJournalPath(v LegacyInventory) string { return filepath.Join(v.WorkDir, "journal.json") }

func (a *localAdapter) rememberCandidateApps(ctx context.Context, j *Journal) error {
	pids, err := enumerateLegacyApps(j.OldBundle)
	if err != nil {
		return err
	}
	known := map[int]LegacyProcessIdentity{}
	for _, p := range j.Legacy.CandidateProcesses {
		known[p.PID] = p
	}
	for _, pid := range pids {
		actual, err := inspectLegacy(pid)
		if err != nil {
			return err
		}
		if previous, ok := known[pid]; ok {
			if actual != previous {
				return errors.New("candidate App process changed")
			}
			continue
		}
		if !strings.HasPrefix(actual.ExecutablePath, filepath.Join(j.OldBundle, "Contents")+string(os.PathSeparator)) || actual.ExecutablePath == bundleCLI(j.OldBundle) {
			return errors.New("candidate App process outside bundle")
		}
		if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
			return err
		}
		if err := legacyVerifySignature(ctx, j.OldBundle); err != nil {
			return errors.New("candidate App signature invalid")
		}
		j.Legacy.CandidateProcesses = append(j.Legacy.CandidateProcesses, actual)
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	return nil
}

func (a *legacyLocalAdapter) ReleaseGate(ctx context.Context, j *Journal) error {
	if err := verifyCLIEntry(j.OldBundle, j.CLIEntry, filepath.Dir(a.journalPath), currentPathDirs()); err != nil {
		return err
	}
	return a.localAdapter.ReleaseGate(ctx, j)
}
func (a *legacyLocalAdapter) ReceiptAccepted(ctx context.Context, j *Journal) (bool, error) {
	if err := verifyCLIEntry(j.OldBundle, j.CLIEntry, filepath.Dir(a.journalPath), currentPathDirs()); err != nil {
		return false, err
	}
	return a.localAdapter.ReceiptAccepted(ctx, j)
}
func (a *legacyLocalAdapter) FinalizeRetention(ctx context.Context, j *Journal) error {
	if err := verifyCLIEntry(j.OldBundle, j.CLIEntry, filepath.Dir(a.journalPath), currentPathDirs()); err != nil {
		return err
	}
	if err := verifyLegacyOriginalTree(ctx, j.RollbackBundle, j.Legacy.BackupManifestSHA256); err != nil {
		return err
	}
	if err := a.localAdapter.FinalizeRetention(ctx, j); err != nil {
		return err
	}
	return a.cleanupLegacyArtifacts(ctx, j)
}

func (a *legacyLocalAdapter) verifyCandidateTree(ctx context.Context, j *Journal) error {
	if !sha256Hex.MatchString(j.Legacy.CandidateTreeSHA256) {
		return errors.New("candidate tree identity missing")
	}
	candidate := ""
	for _, path := range []string{j.OldBundle, j.StagedBundle} {
		if verifyBundleHash(path, j.NewBinarySHA256) == nil {
			if candidate != "" {
				return errors.New("ambiguous candidate App")
			}
			candidate = path
		}
	}
	if candidate == "" {
		return errors.New("candidate App unavailable")
	}
	_, digest, err := legacyTreeManifest(candidate)
	if err != nil || digest != j.Legacy.CandidateTreeSHA256 {
		return errors.New("candidate App contents changed")
	}
	if err := legacyVerifySignature(ctx, candidate); err != nil {
		return errors.New("candidate App signature invalid")
	}
	return nil
}
func (a *legacyLocalAdapter) SwapBundles(ctx context.Context, j *Journal) error {
	if err := a.verifyCandidateTree(ctx, j); err != nil {
		return err
	}
	return a.localAdapter.SwapBundles(ctx, j)
}
func (a *legacyLocalAdapter) StartAndVerify(ctx context.Context, j *Journal) error {
	if err := a.verifyCandidateTree(ctx, j); err != nil {
		return err
	}
	return a.localAdapter.StartAndVerify(ctx, j)
}
