package daemonupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func (a *localAdapter) ReleaseGate(ctx context.Context, j *Journal) error {
	h, err := a.candidateHealth(ctx, j)
	if err != nil {
		return err
	}
	if !h.MaintenanceShutdown || h.MaintenanceGate.Path != j.GatePath || !h.MaintenanceGate.Supported {
		return errors.New("new daemon gate capability changed")
	}
	alive, err := processExists(j.NewAppPID)
	if err != nil || !alive {
		return errors.New("new App is not running")
	}
	_, gateErr := os.Lstat(j.GatePath)
	if gateErr == nil {
		if !h.MaintenanceGate.Blocked || !h.MaintenanceGate.Ready || h.MaintenanceGate.ClaimsInFlight != 0 || h.ActiveTasks != 0 {
			return errors.New("new daemon gate is not idle")
		}
		j.GateReleaseRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
		if err := releaseGate(j); err != nil {
			return err
		}
	} else if !os.IsNotExist(gateErr) || !j.GateReleaseRequested {
		return errors.New("gate disappeared without release intent")
	}
	if err := syncDir(filepath.Dir(j.GatePath)); err != nil {
		return err
	}
	// A crash after removing the gate moves the lower bound forward on resume;
	// it can delay acceptance, but can never accept a pre-release task.
	j.GateReleasedAt = time.Now().UTC()
	return SaveJournal(a.journalPath, *j)
}

func (a *localAdapter) FinalizeRetention(ctx context.Context, j *Journal) error {
	accepted, err := a.ReceiptAccepted(ctx, j)
	if err != nil {
		return err
	}
	if !accepted {
		return errors.New("task receipt no longer verifiable")
	}
	if j.DataCompatibility != "multica-profile-v1" || j.OldDataCompatibility != j.DataCompatibility {
		return errors.New("profile data compatibility is unverified")
	}
	if j.AcceptanceEpoch != nil {
		ready, _, err := a.resumeAcceptance(ctx, j, false)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("restarted daemon requires fresh evidence")
		}
	} else if _, err := a.candidateHealth(ctx, j); err != nil {
		return err
	}
	if err := a.validateBundlePaths(j); err != nil {
		return err
	}
	if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
		return err
	}
	if err := verifyBundleHash(j.RollbackBundle, j.OldBinarySHA256); err != nil {
		return err
	}
	older, err := exists(j.OlderBundle)
	if err != nil {
		return err
	}
	if older {
		if err := verifyBundleHash(j.OlderBundle, j.PreviousRollbackSHA256); err != nil {
			return err
		}
		info, err := os.Lstat(j.OlderBundle)
		if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
			return errors.New("untrusted previous rollback bundle")
		}
		_, digest, err := legacyTreeManifest(j.OlderBundle)
		if err != nil || !sha256Hex.MatchString(j.PreviousRollbackTreeSHA256) || digest != j.PreviousRollbackTreeSHA256 {
			return errors.New("previous rollback tree mismatch")
		}
		j.CleanupRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
		if err := os.RemoveAll(j.OlderBundle); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(j.OlderBundle)); err != nil {
			return err
		}
	} else if j.PreviousRollbackSHA256 != "" && !j.CleanupRequested {
		return errors.New("previous rollback disappeared before cleanup")
	}
	previous := map[string]string{"version": j.OldVersion, "binary_sha256": j.OldBinarySHA256}
	if j.OldCommit != "" {
		previous["commit"] = j.OldCommit
	}
	ledger := map[string]any{
		"data_compatibility": j.DataCompatibility,
		"cli_entry":          j.CLIEntry,
		"current":            map[string]string{"version": j.NewVersion, "commit": j.NewCommit, "binary_sha256": j.NewBinarySHA256},
		"previous":           previous,
	}
	if j.Legacy != nil && j.Legacy.PossibleMissedClaims {
		ledger["legacy_possible_missed_claims"] = true
	}
	return saveJSON(filepath.Join(filepath.Dir(a.journalPath), "ledger.json"), ledger)
}

func (a *localAdapter) ReceiptAccepted(ctx context.Context, j *Journal) (bool, error) {
	if j.GateReleasedAt.IsZero() {
		return false, errors.New("gate release time missing")
	}
	if _, err := os.Lstat(j.GatePath); !os.IsNotExist(err) {
		return false, errors.New("maintenance gate still present or inaccessible")
	}
	h, err := a.candidateHealth(ctx, j)
	needsReauth := false
	if err == nil && j.AcceptanceEpoch == nil {
		needsReauth, err = a.stableAcceptanceNeedsReauth(ctx, j, h, false)
	}
	if j.AcceptanceEpoch != nil || needsReauth || (err != nil && (j.Phase == "awaiting_acceptance" || j.Phase == "finalizing")) {
		var ready bool
		ready, h, err = a.resumeAcceptance(ctx, j, false)
		if err != nil || !ready {
			return false, err
		}
	}
	if err != nil {
		return false, err
	}
	if h.MaintenanceGate.Blocked || !h.MaintenanceGate.Supported || h.MaintenanceGate.Path != j.GatePath {
		return false, errors.New("claim gate not open")
	}
	runtimes := make(map[string]bool, len(j.RuntimeIDs))
	for _, id := range j.RuntimeIDs {
		runtimes[id] = true
	}
	lowerBound := j.GateReleasedAt
	if j.AcceptanceEpoch != nil && j.AcceptanceEpoch.Since.After(lowerBound) {
		lowerBound = j.AcceptanceEpoch.Since
	}
	for _, receipt := range h.Attestation.TaskReceipts {
		if receipt.TaskID != "" && runtimes[receipt.RuntimeID] && receipt.CompletedAt.After(lowerBound) && !receipt.AckAt.Before(receipt.CompletedAt) {
			return true, nil
		}
	}
	return false, nil
}

func (a *localAdapter) RollbackReceiptAccepted(ctx context.Context, j *Journal) (bool, error) {
	if j.RollbackGateReleasedAt.IsZero() || j.RollbackDaemonPID <= 0 || j.RollbackDaemonStartedAt == "" {
		return false, errors.New("rollback acceptance identity missing")
	}
	if _, err := os.Lstat(j.GatePath); !os.IsNotExist(err) {
		return false, errors.New("rollback gate still present or inaccessible")
	}
	h, err := a.acceptanceHealth(ctx, j, true)
	if err == nil && (h.PID != j.RollbackDaemonPID || h.Attestation.ProcessStartedAt != j.RollbackDaemonStartedAt) {
		err = errors.New("rollback daemon process changed")
	}
	needsReauth := false
	if err == nil && j.AcceptanceEpoch == nil {
		needsReauth, err = a.stableAcceptanceNeedsReauth(ctx, j, h, true)
	}
	if j.AcceptanceEpoch != nil || needsReauth || (err != nil && (j.Phase == "rollback_awaiting_acceptance" || j.Phase == "rollback_finalizing")) {
		var ready bool
		ready, h, err = a.resumeAcceptance(ctx, j, true)
		if err != nil || !ready {
			return false, err
		}
	}
	if err != nil {
		return false, err
	}
	if !h.MaintenanceGate.Supported || h.MaintenanceGate.Blocked || h.MaintenanceGate.Path != j.GatePath {
		return false, errors.New("rollback gate is not open")
	}
	runtimes := make(map[string]bool, len(j.RuntimeIDs))
	for _, id := range j.RuntimeIDs {
		runtimes[id] = true
	}
	lowerBound := j.RollbackGateReleasedAt
	if j.AcceptanceEpoch != nil && j.AcceptanceEpoch.Since.After(lowerBound) {
		lowerBound = j.AcceptanceEpoch.Since
	}
	for _, receipt := range h.Attestation.TaskReceipts {
		if receipt.TaskID != "" && runtimes[receipt.RuntimeID] && receipt.CompletedAt.After(lowerBound) && !receipt.AckAt.Before(receipt.CompletedAt) {
			return true, nil
		}
	}
	return false, nil
}

// FinalizeRollback retires only this transaction's attested failed candidate,
// and only after a new receipt proves the restored daemon can do work.
func (a *localAdapter) FinalizeRollback(ctx context.Context, j *Journal) error {
	accepted, err := a.RollbackReceiptAccepted(ctx, j)
	if err != nil {
		return err
	}
	if !accepted {
		return errors.New("rollback task receipt no longer verifiable")
	}
	if err := a.validateBundlePaths(j); err != nil {
		return err
	}
	if err := verifyBundleHash(j.OldBundle, j.OldBinarySHA256); err != nil {
		return err
	}
	if !sha256Hex.MatchString(j.CandidateTreeSHA256) {
		return errors.New("failed candidate tree identity missing")
	}
	failed := filepath.Join(filepath.Dir(a.journalPath), "failed-"+j.Token+".app")
	if filepath.Dir(failed) != filepath.Dir(a.journalPath) {
		return errors.New("unsafe failed candidate path")
	}
	var target string
	for _, path := range []string{j.StagedBundle, failed} {
		info, e := os.Lstat(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || !info.IsDir() || !ownedByCurrentUser(info) {
			return errors.New("untrusted failed candidate path")
		}
		if target != "" {
			return errors.New("ambiguous failed candidate bundles")
		}
		target = path
	}
	if target == "" {
		if !j.RollbackCleanupRequested {
			return errors.New("failed candidate disappeared before cleanup")
		}
		if err := syncDir(filepath.Dir(a.journalPath)); err != nil {
			return err
		}
		return syncDir(filepath.Dir(j.StagedBundle))
	}
	if err := verifyBundleHash(target, j.NewBinarySHA256); err != nil {
		return err
	}
	_, digest, err := legacyTreeManifest(target)
	if err != nil || digest != j.CandidateTreeSHA256 {
		return errors.New("failed candidate tree mismatch")
	}
	if !j.RollbackCleanupRequested {
		j.RollbackCleanupRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}
