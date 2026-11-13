package daemonupdate

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

type fixtureOps struct {
	calls   []string
	fail    string
	receipt bool
}

func TestVersionComparisonWithUpstreamMetadata(t *testing.T) {
	for _, tc := range []struct {
		next, previous string
		want           int
	}{
		{"0.8.2-zh-tw.1+up.250ecac", "0.8.1-zh-tw.8", 1},
		{"0.8.2-zh-tw.2+up.abcdef0", "0.8.2-zh-tw.1+up.250ecac", 1},
		{"0.8.2-zh-tw.1+up.abcdef0", "0.8.2-zh-tw.1+up.250ecac", 0},
	} {
		got, err := compareVersion(tc.next, tc.previous)
		if err != nil || got != tc.want {
			t.Errorf("compareVersion(%q, %q) = %d, %v; want %d", tc.next, tc.previous, got, err, tc.want)
		}
	}
	if _, err := compareVersion("0.8.2-zh-tw.2+up.invalid", "0.8.2-zh-tw.1+up.250ecac"); err == nil {
		t.Fatal("malformed upstream metadata accepted")
	}
}

func fixtureJournal(phase string) *Journal {
	return &Journal{Phase: phase, OldVersion: "0.8.1-zh-tw.4", NewVersion: "0.8.1-zh-tw.5", DataCompatibility: "multica-profile-v1", OldDataCompatibility: "multica-profile-v1"}
}

func (o *fixtureOps) step(name string) error {
	o.calls = append(o.calls, name)
	if o.fail == name {
		return errors.New(name)
	}
	return nil
}
func (o *fixtureOps) AcquireGate(_ context.Context, _ *Journal) error     { return o.step("gate") }
func (o *fixtureOps) DrainAndStopOld(_ context.Context, _ *Journal) error { return o.step("stop") }
func (o *fixtureOps) SwapBundles(_ context.Context, _ *Journal) error     { return o.step("swap") }
func (o *fixtureOps) StartAndVerify(_ context.Context, _ *Journal) error  { return o.step("verify") }
func (o *fixtureOps) ReleaseGate(_ context.Context, _ *Journal) error     { return o.step("release") }
func (o *fixtureOps) ReceiptAccepted(_ context.Context, _ *Journal) (bool, error) {
	return o.receipt, o.step("receipt")
}
func (o *fixtureOps) FinalizeRetention(_ context.Context, _ *Journal) error { return o.step("cleanup") }
func (o *fixtureOps) Rollback(_ context.Context, _ *Journal) error          { return o.step("rollback") }
func (o *fixtureOps) RollbackReceiptAccepted(_ context.Context, _ *Journal) (bool, error) {
	return o.receipt, o.step("rollback_receipt")
}
func (o *fixtureOps) FinalizeRollback(_ context.Context, _ *Journal) error {
	return o.step("rollback_cleanup")
}

func TestAdvanceDoesNotFinalizeBeforeReceipt(t *testing.T) {
	j := fixtureJournal("prepared")
	o := &fixtureOps{}
	path := filepath.Join(t.TempDir(), "journal.json")
	done, err := Advance(context.Background(), path, j, o)
	if err != nil || done || j.Phase != "awaiting_acceptance" {
		t.Fatalf("%v %v %+v", done, err, j)
	}
	if !reflect.DeepEqual(o.calls, []string{"gate", "stop", "swap", "verify", "release", "receipt"}) {
		t.Fatal(o.calls)
	}
	o.receipt = true
	done, err = Advance(context.Background(), path, j, o)
	if err != nil || !done || j.Phase != "complete" {
		t.Fatalf("%v %v %+v", done, err, j)
	}
}

func TestAdvancePersistsRollbackIntentOnVerificationFailure(t *testing.T) {
	j := fixtureJournal("prepared")
	o := &fixtureOps{fail: "verify"}
	path := filepath.Join(t.TempDir(), "journal.json")
	if _, err := Advance(context.Background(), path, j, o); err == nil || j.Phase != "rollback_awaiting_acceptance" || o.calls[len(o.calls)-1] != "rollback" {
		t.Fatalf("%v %+v", err, j)
	}
	done, err := Advance(context.Background(), path, j, o)
	if err != nil || done || j.Phase != "rollback_awaiting_acceptance" || o.calls[len(o.calls)-1] != "rollback_receipt" {
		t.Fatalf("%v %v %+v %v", done, err, j, o.calls)
	}
}

func TestAdvanceCleanupFailureDoesNotReportSuccess(t *testing.T) {
	j := fixtureJournal("awaiting_acceptance")
	o := &fixtureOps{receipt: true, fail: "cleanup"}
	path := filepath.Join(t.TempDir(), "journal.json")
	done, err := Advance(context.Background(), path, j, o)
	if done || err == nil || j.Phase != "finalizing" {
		t.Fatalf("%v %v %+v", done, err, j)
	}
}

func TestAdvanceReleaseFailureRollsBackOnlyBeforeReleaseIntent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested bool
		phase     string
		calls     []string
	}{
		{"gate still held", false, "rollback_awaiting_acceptance", []string{"release", "rollback"}},
		{"release may have happened", true, "releasing_gate", []string{"release"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixtureJournal("releasing_gate")
			j.GateReleaseRequested = tc.requested
			o := &fixtureOps{fail: "release"}
			done, err := Advance(context.Background(), filepath.Join(t.TempDir(), "journal"), j, o)
			if done || err == nil || j.Phase != tc.phase || !reflect.DeepEqual(o.calls, tc.calls) {
				t.Fatalf("done=%v err=%v phase=%s calls=%v", done, err, j.Phase, o.calls)
			}
		})
	}
}

func TestSameVersionIsNoOpAndOlderCandidateIsRefused(t *testing.T) {
	j := fixtureJournal("prepared")
	j.NewVersion = j.OldVersion
	o := &fixtureOps{}
	path := filepath.Join(t.TempDir(), "journal")
	done, err := Advance(context.Background(), path, j, o)
	if err != nil || !done || j.Phase != "no_change" || len(o.calls) != 0 {
		t.Fatalf("%v %v %v", done, err, o.calls)
	}
	j.NewVersion = "0.8.1-zh-tw.3"
	j.Phase = "prepared"
	if _, err = Advance(context.Background(), path, j, o); err == nil || len(o.calls) != 0 {
		t.Fatalf("older candidate accepted: %v", err)
	}
}

func TestRollbackCleanupFailureRemainsRetryable(t *testing.T) {
	j := fixtureJournal("rollback_awaiting_acceptance")
	o := &fixtureOps{receipt: true, fail: "rollback_cleanup"}
	path := filepath.Join(t.TempDir(), "journal.json")
	done, err := Advance(context.Background(), path, j, o)
	if done || err == nil || j.Phase != "rollback_finalizing" {
		t.Fatalf("cleanup failure: done=%v err=%v phase=%s", done, err, j.Phase)
	}
	o.fail = ""
	done, err = Advance(context.Background(), path, j, o)
	if done || err != nil || j.Phase != "restored" {
		t.Fatalf("cleanup retry: done=%v err=%v phase=%s", done, err, j.Phase)
	}
}
