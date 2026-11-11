package daemonupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type legacyFixture struct {
	fail    string
	receipt bool
	calls   []string
}

func (f *legacyFixture) step(s string) error {
	f.calls = append(f.calls, s)
	if f.fail == s {
		return errors.New(s)
	}
	return nil
}
func (f *legacyFixture) Prepare(context.Context, *Journal) error        { return f.step("prepare") }
func (f *legacyFixture) StopOld(context.Context, *Journal) error        { return f.step("stop") }
func (f *legacyFixture) SwapBundles(context.Context, *Journal) error    { return f.step("swap") }
func (f *legacyFixture) StartAndVerify(context.Context, *Journal) error { return f.step("verify") }
func (f *legacyFixture) ReleaseGate(context.Context, *Journal) error    { return f.step("release") }
func (f *legacyFixture) ReceiptAccepted(context.Context, *Journal) (bool, error) {
	return f.receipt, f.step("receipt")
}
func (f *legacyFixture) FinalizeRetention(context.Context, *Journal) error { return f.step("finalize") }
func (f *legacyFixture) RestoreLegacy(context.Context, *Journal) error     { return f.step("restore") }
func fixtureLegacyJournal(phase string) *Journal {
	return &Journal{Phase: phase, Legacy: &LegacyState{InventorySHA256: strings.Repeat("a", 64)}}
}
func TestLegacyTransactionRequiresReceiptBeforeFinalizing(t *testing.T) {
	j := fixtureLegacyJournal("prepared")
	f := &legacyFixture{}
	path := filepath.Join(t.TempDir(), "journal")
	done, err := AdvanceLegacy(context.Background(), path, j, f)
	if err != nil || done || j.Phase != "awaiting_acceptance" {
		t.Fatalf("pending=%v %v %s", done, err, j.Phase)
	}
	f.receipt = true
	done, err = AdvanceLegacy(context.Background(), path, j, f)
	if err != nil || !done || j.Phase != "complete" {
		t.Fatalf("done=%v %v %s", done, err, j.Phase)
	}
}
func TestLegacyTransactionRecordsRestoreAndReleaseUncertainty(t *testing.T) {
	for _, tc := range []struct {
		phase, fail, want string
		release, restore  bool
	}{{"stopping_old", "stop", "restored_legacy", false, true}, {"swapping", "swap", "restored_legacy", false, true}, {"verifying_new", "verify", "restored_legacy", false, true}, {"releasing_gate", "release", "restored_legacy", false, true}, {"releasing_gate", "release", "releasing_gate", true, false}} {
		t.Run(tc.phase+tc.want, func(t *testing.T) {
			j := fixtureLegacyJournal(tc.phase)
			if tc.phase == "stopping_old" {
				j.Legacy.StopIntents = map[int]bool{123: true}
			}
			j.GateReleaseRequested = tc.release
			f := &legacyFixture{fail: tc.fail}
			done, err := AdvanceLegacy(context.Background(), filepath.Join(t.TempDir(), "journal"), j, f)
			if done || err == nil || j.Phase != tc.want || j.Legacy.RecoveryNeedsRedrive != tc.restore {
				t.Fatalf("%v %v %+v", done, err, j)
			}
		})
	}
}

func TestLegacyDrainFailureBeforeStopIntentRemainsResumable(t *testing.T) {
	j := fixtureLegacyJournal("stopping_old")
	f := &legacyFixture{fail: "stop"}
	done, err := AdvanceLegacy(context.Background(), filepath.Join(t.TempDir(), "journal"), j, f)
	if done || err == nil || j.Phase != "stopping_old" || j.Legacy.RecoveryNeedsRedrive {
		t.Fatalf("done=%v err=%v journal=%+v", done, err, j)
	}
	if len(f.calls) != 1 || f.calls[0] != "stop" {
		t.Fatalf("unexpected operation: %v", f.calls)
	}
}

func TestLegacyJournalPhaseDoesNotConflatePendingAndRestored(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "zh-tw-update")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.json")
	token := "11111111-1111-4111-8111-111111111111"
	hash := strings.Repeat("a", 64)
	for _, phase := range []string{"awaiting_acceptance", "restored_legacy"} {
		j := fixtureLegacyJournal(phase)
		j.Version = 1
		j.Token = token
		if err := SaveJournal(path, *j); err != nil {
			t.Fatal(err)
		}
		got, err := LegacyJournalPhase(path, token, hash)
		if err != nil || got != phase {
			t.Fatalf("%s: %s %v", phase, got, err)
		}
		if _, err := LegacyJournalPhase(path, "22222222-2222-4222-8222-222222222222", hash); err == nil {
			t.Fatal("wrong token accepted")
		}
	}
}
