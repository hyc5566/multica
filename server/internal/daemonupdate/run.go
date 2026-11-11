package daemonupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

var transactionToken = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var abortAppExecutable = func(ctx context.Context, bundle string) (string, error) {
	return (macInspector{}).BundleValue(ctx, bundle, "CFBundleExecutable")
}

// RunJournal only accepts a same-user journal in the App's update directory.
// A matching previously accepted ledger is required to adopt the baseline.
func RunJournal(ctx context.Context, path, token string) (bool, error) {
	return runJournal(ctx, path, token, 0)
}

func AbortJournalBeforeHandoff(ctx context.Context, path, token string, appPID int) (bool, error) {
	if appPID <= 0 {
		return false, errors.New("invalid App process")
	}
	return runJournal(ctx, path, token, appPID)
}

func runJournal(ctx context.Context, path, token string, abortAppPID int) (bool, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != "journal.json" || filepath.Base(filepath.Dir(path)) != "zh-tw-update" || !transactionToken.MatchString(token) {
		return false, errors.New("invalid journal location or token")
	}
	unlock, err := lockJournal(filepath.Join(filepath.Dir(path), "journal.lock"))
	if err != nil {
		return false, err
	}
	defer unlock()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Size() > 1<<20 {
		return false, errors.New("untrusted journal")
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, errors.New("journal changed")
	}
	var j Journal
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&j); err != nil {
		return false, err
	}
	if j.Legacy != nil {
		return false, errors.New("legacy journal requires legacy-apply")
	}
	if j.Version != 1 || j.Token != token || !strings.HasPrefix(j.Profile, "desktop-") || filepath.Base(j.Profile) != j.Profile || strings.ContainsAny(j.Profile, `/\\`) || j.DaemonID == "" || !sha256Hex.MatchString(j.ServerURLSHA256) || j.DaemonPID <= 0 || j.AppPID <= 0 {
		return false, errors.New("invalid journal identity")
	}
	a := newLocalAdapter(path)
	if err := a.validateBundlePaths(&j); err != nil {
		return false, err
	}
	if filepath.Clean(j.ReadyFile) != filepath.Join(filepath.Dir(path), "ready-"+token) || filepath.Clean(j.HandoffFile) != filepath.Join(filepath.Dir(path), "handoff-"+token) {
		return false, errors.New("invalid transaction markers")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "ledger.json"))
	if err != nil {
		return false, errors.New("baseline ledger missing; adoption required")
	}
	var ledger struct {
		DataCompatibility string `json:"data_compatibility"`
		CLIEntry          string `json:"cli_entry"`
		Current           struct {
			Version      string `json:"version"`
			Commit       string `json:"commit"`
			BinarySHA256 string `json:"binary_sha256"`
		} `json:"current"`
		Previous struct {
			Version      string `json:"version"`
			Commit       string `json:"commit"`
			BinarySHA256 string `json:"binary_sha256"`
		} `json:"previous"`
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return false, errors.New("invalid baseline ledger")
	}
	baseline := ledger.Current.Version == j.OldVersion && ledger.Current.Commit == j.OldCommit && ledger.Current.BinarySHA256 == j.OldBinarySHA256 && ledger.Previous.BinarySHA256 == j.PreviousRollbackSHA256
	finalized := j.Phase == "finalizing" && ledger.Current.Version == j.NewVersion && ledger.Current.Commit == j.NewCommit && ledger.Current.BinarySHA256 == j.NewBinarySHA256 && ledger.Previous.BinarySHA256 == j.OldBinarySHA256
	if ledger.DataCompatibility != j.OldDataCompatibility || ledger.CLIEntry == "" || ledger.CLIEntry != j.CLIEntry || (!baseline && !finalized) {
		return false, errors.New("baseline ledger mismatch")
	}
	if err := verifyCLIEntry(j.OldBundle, j.CLIEntry, filepath.Dir(path), currentPathDirs()); err != nil {
		return false, err
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	selfHash, err := hashFile(executable)
	if err != nil || (selfHash != j.NewBinarySHA256 && selfHash != j.OldBinarySHA256) {
		return false, errors.New("unverified coordinator binary")
	}
	if j.Phase != "prepared" && j.Phase != "acquiring_gate" && j.GateIdentity == "" && j.Phase != "complete" && j.Phase != "restored" && j.Phase != "no_change" {
		return false, errors.New("gate identity missing")
	}
	if abortAppPID != 0 {
		if err := prepareAbortBeforeHandoff(ctx, path, &j, abortAppPID, a); err != nil {
			return false, err
		}
		return true, nil
	}
	done, err := Advance(ctx, path, &j, a)
	if err != nil {
		return false, fmt.Errorf("coordinated update %s: %w", j.Phase, err)
	}
	return done, nil
}

func prepareAbortBeforeHandoff(ctx context.Context, path string, j *Journal, appPID int, a *localAdapter) error {
	if j.AppPID != appPID || j.NewAppPID != 0 || j.NewDaemonPID != 0 || j.StopRequested || j.SwapStep != 0 {
		return errors.New("handoff already passed App ownership")
	}
	app, err := inspectLegacy(appPID)
	if err != nil {
		return err
	}
	executable, err := abortAppExecutable(ctx, j.OldBundle)
	if err != nil || executable == "" || filepath.Base(executable) != executable || app.ExecutablePath != filepath.Join(j.OldBundle, "Contents", "MacOS", executable) {
		return errors.New("original App identity changed")
	}
	if j.Phase != "prepared" && j.Phase != "acquiring_gate" && j.Phase != "stopping_old" {
		return errors.New("handoff cannot be safely aborted in this phase")
	}
	dir, err := cli.ProfileDir(j.Profile)
	if err != nil || j.GatePath != filepath.Join(dir, "daemon.maintenance") {
		return errors.New("gate profile mismatch")
	}
	if err := verifyBundleHash(j.OldBundle, j.OldBinarySHA256); err != nil {
		return err
	}
	h, err := a.readHealth(ctx, j)
	if err != nil {
		return err
	}
	if err := a.validateOld(j, h); err != nil {
		return err
	}
	_, gateErr := os.Lstat(j.GatePath)
	if os.IsNotExist(gateErr) {
		if j.GateCreationIntent || (j.GateIdentity != "" && !j.AbortBeforeHandoffRequested) || h.MaintenanceGate.Blocked {
			return errors.New("gate state requires operator recovery")
		}
		if j.AbortBeforeHandoffRequested {
			j.Phase = "restored"
		} else {
			j.Phase = "no_change"
		}
		return SaveJournal(path, *j)
	}
	if gateErr != nil {
		return gateErr
	}
	if err := verifyGate(j); err != nil {
		return err
	}
	if !h.MaintenanceGate.Blocked || !h.MaintenanceGate.Ready || h.MaintenanceGate.ClaimsInFlight != 0 || h.ActiveTasks != 0 {
		return errors.New("original daemon gate has not drained")
	}
	j.AbortBeforeHandoffRequested = true
	if err := SaveJournal(path, *j); err != nil {
		return err
	}
	if err := releaseGate(j); err != nil {
		return err
	}
	j.Phase = "restored"
	return SaveJournal(path, *j)
}
