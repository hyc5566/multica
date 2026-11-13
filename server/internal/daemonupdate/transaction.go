package daemonupdate

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
)

var releaseVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)-zh-tw\.(\d+)(?:\+up\.[0-9a-f]{7})?$`)

func compareVersion(next, previous string) (int, error) {
	a, b := releaseVersion.FindStringSubmatch(next), releaseVersion.FindStringSubmatch(previous)
	if a == nil || b == nil {
		return 0, fmt.Errorf("invalid release version")
	}
	for i := 1; i < len(a); i++ {
		left, err := strconv.ParseUint(a[i], 10, 64)
		if err != nil {
			return 0, err
		}
		right, err := strconv.ParseUint(b[i], 10, 64)
		if err != nil {
			return 0, err
		}
		if left < right {
			return -1, nil
		}
		if left > right {
			return 1, nil
		}
	}
	return 0, nil
}

// Operations must reconcile the actual resource with the journal on every
// invocation. An intent is persisted before each call so a killed helper can
// safely retry without assuming that the preceding operation did not happen.
type Operations interface {
	AcquireGate(context.Context, *Journal) error
	DrainAndStopOld(context.Context, *Journal) error
	SwapBundles(context.Context, *Journal) error
	StartAndVerify(context.Context, *Journal) error
	ReleaseGate(context.Context, *Journal) error
	ReceiptAccepted(context.Context, *Journal) (bool, error)
	FinalizeRetention(context.Context, *Journal) error
	Rollback(context.Context, *Journal) error
	RollbackReceiptAccepted(context.Context, *Journal) (bool, error)
	FinalizeRollback(context.Context, *Journal) error
}

// Advance resumes a persisted transaction. A missing receipt is a pending
// result, not a successful acceptance. Failures before gate release require a
// complete rollback; cleanup failures remain retryable and never report done.
func Advance(ctx context.Context, path string, j *Journal, ops Operations) (bool, error) {
	comparison, err := compareVersion(j.NewVersion, j.OldVersion)
	if err != nil || comparison < 0 {
		return false, fmt.Errorf("invalid or older candidate")
	}
	if comparison == 0 {
		if j.Phase == "prepared" {
			j.Phase = "no_change"
			if err := SaveJournal(path, *j); err != nil {
				return false, err
			}
			return true, nil
		}
		if j.Phase == "no_change" {
			return true, nil
		}
		return false, fmt.Errorf("same-version journal cannot resume an update")
	}
	if j.DataCompatibility != "multica-profile-v1" || j.OldDataCompatibility != j.DataCompatibility {
		return false, fmt.Errorf("unverified or incompatible profile data")
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var err error
		switch j.Phase {
		case "prepared":
			j.Phase = "acquiring_gate"
		case "acquiring_gate":
			err = ops.AcquireGate(ctx, j)
			if err == nil {
				j.Phase = "stopping_old"
			}
		case "stopping_old":
			err = ops.DrainAndStopOld(ctx, j)
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
			var accepted bool
			accepted, err = ops.ReceiptAccepted(ctx, j)
			if err == nil && !accepted {
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
			err = ops.Rollback(ctx, j)
			if err == nil {
				j.Phase = "rollback_awaiting_acceptance"
			}
		case "rollback_awaiting_acceptance":
			var accepted bool
			accepted, err = ops.RollbackReceiptAccepted(ctx, j)
			if err == nil && !accepted {
				return false, nil
			}
			if err == nil {
				j.Phase = "rollback_finalizing"
			}
		case "rollback_finalizing":
			err = ops.FinalizeRollback(ctx, j)
			if err == nil {
				j.Phase = "restored"
			}
		case "complete", "restored":
			return j.Phase == "complete", nil
		default:
			return false, fmt.Errorf("unknown transaction phase %q", j.Phase)
		}
		if err != nil {
			if (j.Phase == "acquiring_gate" && j.GateIdentity != "") || j.Phase == "stopping_old" || j.Phase == "swapping" || j.Phase == "verifying_new" || (j.Phase == "releasing_gate" && !j.GateReleaseRequested) {
				j.Phase = "rolling_back"
				if saveErr := SaveJournal(path, *j); saveErr != nil {
					return false, fmt.Errorf("%v; cannot persist rollback: %w", err, saveErr)
				}
				if rollbackErr := ops.Rollback(ctx, j); rollbackErr != nil {
					return false, fmt.Errorf("%v; rollback incomplete: %w", err, rollbackErr)
				}
				j.Phase = "rollback_awaiting_acceptance"
				if saveErr := SaveJournal(path, *j); saveErr != nil {
					return false, fmt.Errorf("%v; cannot persist restored state: %w", err, saveErr)
				}
			}
			return false, err
		}
		if err = SaveJournal(path, *j); err != nil {
			return false, err
		}
	}
}
