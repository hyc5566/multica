package daemonupdate

import (
	"context"
	"errors"
	"fmt"
)

// LegacyState belongs to the same durable journal used by Desktop readiness.
// Stop intents precede every signal and retain the approved native identities.
type LegacyState struct {
	InventorySHA256              string                  `json:"inventory_sha256"`
	InventoryPath                string                  `json:"inventory_path"`
	Inventory                    LegacyInventory         `json:"inventory"`
	ManifestPath                 string                  `json:"manifest_path"`
	CompatibilityEvidencePath    string                  `json:"compatibility_evidence_path"`
	StopIntents                  map[int]bool            `json:"stop_intents"`
	CandidateProcesses           []LegacyProcessIdentity `json:"candidate_processes,omitempty"`
	CandidateStopIntents         map[int]bool            `json:"candidate_stop_intents,omitempty"`
	RecoveryDaemon               *LegacyProcessIdentity  `json:"recovery_daemon,omitempty"`
	RecoveryApp                  *LegacyProcessIdentity  `json:"recovery_app,omitempty"`
	RecoveryStartRequested       bool                    `json:"recovery_start_requested,omitempty"`
	RecoveryAppOpenRequested     bool                    `json:"recovery_app_open_requested,omitempty"`
	RecoveryGateReleaseRequested bool                    `json:"recovery_gate_release_requested,omitempty"`
	BackupManifestSHA256         string                  `json:"backup_manifest_sha256,omitempty"`
	CandidateTreeSHA256          string                  `json:"candidate_tree_sha256,omitempty"`
	CLIEntryState                string                  `json:"cli_entry_state,omitempty"`
	CoordinatorSHA256            string                  `json:"coordinator_sha256,omitempty"`
	RecoveryStep                 string                  `json:"recovery_step,omitempty"`
	RecoveryNeedsRedrive         bool                    `json:"recovery_needs_redrive,omitempty"`
	PossibleMissedClaims         bool                    `json:"possible_missed_claims,omitempty"`
	BaselineCleanupRequested     bool                    `json:"baseline_cleanup_requested,omitempty"`
	CoordinatorCleanupRequested  bool                    `json:"coordinator_cleanup_requested,omitempty"`
}

type LegacyOperations interface {
	Prepare(context.Context, *Journal) error
	StopOld(context.Context, *Journal) error
	SwapBundles(context.Context, *Journal) error
	StartAndVerify(context.Context, *Journal) error
	ReleaseGate(context.Context, *Journal) error
	ReceiptAccepted(context.Context, *Journal) (bool, error)
	FinalizeRetention(context.Context, *Journal) error
	RestoreLegacy(context.Context, *Journal) error
}

// AdvanceLegacy persists a single authority for Desktop and the coordinator.
func AdvanceLegacy(ctx context.Context, path string, j *Journal, ops LegacyOperations) (bool, error) {
	if j.Legacy == nil || !sha256Hex.MatchString(j.Legacy.InventorySHA256) {
		return false, errors.New("invalid legacy inventory identity")
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var err error
		switch j.Phase {
		case "prepared":
			j.Phase = "preparing"
		case "preparing":
			err = ops.Prepare(ctx, j)
			if err == nil {
				j.Phase = "stopping_old"
			}
		case "stopping_old":
			err = ops.StopOld(ctx, j)
			if err == nil {
				j.Phase = "swapping"
			}
		case "swapping":
			err = ops.SwapBundles(ctx, j)
			if err == nil {
				j.Phase = "verifying_new"
			}
		case "verifying_new":
			err = ops.StartAndVerify(ctx, j)
			if err == nil {
				j.Phase = "releasing_gate"
			}
		case "releasing_gate":
			err = ops.ReleaseGate(ctx, j)
			if err == nil {
				j.Phase = "awaiting_acceptance"
			}
		case "awaiting_acceptance":
			var ok bool
			ok, err = ops.ReceiptAccepted(ctx, j)
			if err == nil && !ok {
				return false, nil
			}
			if err == nil {
				j.Phase = "finalizing"
			}
		case "finalizing":
			err = ops.FinalizeRetention(ctx, j)
			if err == nil {
				j.Phase = "complete"
			}
		case "rolling_back":
			err = ops.RestoreLegacy(ctx, j)
			if err == nil {
				j.Legacy.RecoveryNeedsRedrive = true
				j.Phase = "restored_legacy"
			}
		case "complete":
			return true, nil
		case "restored_legacy":
			return false, nil
		default:
			return false, errors.New("invalid legacy transaction phase")
		}
		if err != nil {
			if (j.Phase == "stopping_old" && len(j.Legacy.StopIntents) > 0) || j.Phase == "swapping" || j.Phase == "verifying_new" || (j.Phase == "releasing_gate" && !j.GateReleaseRequested) {
				j.Phase = "rolling_back"
				if e := SaveJournal(path, *j); e != nil {
					return false, fmt.Errorf("%v; cannot save rollback: %w", err, e)
				}
				if e := ops.RestoreLegacy(ctx, j); e != nil {
					return false, fmt.Errorf("%v; restore incomplete: %w", err, e)
				}
				j.Legacy.RecoveryNeedsRedrive = true
				j.Phase = "restored_legacy"
				if e := SaveJournal(path, *j); e != nil {
					return false, fmt.Errorf("%v; cannot save restored state: %w", err, e)
				}
			}
			return false, err
		}
		if err := SaveJournal(path, *j); err != nil {
			return false, err
		}
	}
}
