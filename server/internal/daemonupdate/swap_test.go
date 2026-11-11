package daemonupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func fixtureBundle(t *testing.T, path, contents string) string {
	t.Helper()
	bin := bundleCLI(path)
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(contents)))
}

func TestSwapPreservesPriorRollbackUntilAcceptance(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "update")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{Profile: "test", Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance"), AppPID: 99999999,
		OldBundle: filepath.Join(root, "Applications", "Multica.app"), StagedBundle: filepath.Join(work, "staging", "Multica.app"),
		RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app")}
	j.OldBinarySHA256 = fixtureBundle(t, j.OldBundle, "current")
	j.NewBinarySHA256 = fixtureBundle(t, j.StagedBundle, "new")
	j.PreviousRollbackSHA256 = fixtureBundle(t, j.RollbackBundle, "previous")
	path := filepath.Join(work, "journal.json")
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	a := newLocalAdapter(path)
	if err := a.SwapBundles(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{j.OldBundle: j.NewBinarySHA256, j.RollbackBundle: j.OldBinarySHA256, j.OlderBundle: j.PreviousRollbackSHA256} {
		if err := verifyBundleHash(path, want); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if err := a.SwapBundles(context.Background(), j); err != nil {
		t.Fatalf("resume completed swap: %v", err)
	}
	if err := a.restoreBundles(j); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundleHash(j.OldBundle, j.OldBinarySHA256); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundleHash(j.RollbackBundle, j.PreviousRollbackSHA256); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundleHash(filepath.Join(work, "failed-"+j.Token+".app"), j.NewBinarySHA256); err != nil {
		t.Fatal(err)
	}
	if err := a.restoreBundles(j); err != nil {
		t.Fatalf("resume restored bundles: %v", err)
	}
}

func TestSwapResumesAfterExchangeBeforeJournalAdvance(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "update")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{Profile: "test", Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance"), AppPID: 99999999, SwapStep: 1,
		OldBundle: filepath.Join(root, "Applications", "Multica.app"), StagedBundle: filepath.Join(work, "staging", "Multica.app"),
		RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app")}
	j.OldBinarySHA256 = fixtureBundle(t, j.OldBundle, "old")
	j.NewBinarySHA256 = fixtureBundle(t, j.StagedBundle, "new")
	_, j.CandidateTreeSHA256, _ = legacyTreeManifest(j.StagedBundle)
	currentInfo, err := os.Lstat(j.OldBundle)
	if err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(j.StagedBundle)
	if err != nil {
		t.Fatal(err)
	}
	j.CurrentIdentity, err = gateIdentity(currentInfo)
	if err != nil {
		t.Fatal(err)
	}
	j.StagedIdentity, err = gateIdentity(stageInfo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "journal.json")
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	if err := exchangeBundles(j.OldBundle, j.StagedBundle); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
		t.Fatal("current path missing after exchange", err)
	}
	if err := newLocalAdapter(path).SwapBundles(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundleHash(j.RollbackBundle, j.OldBinarySHA256); err != nil {
		t.Fatal(err)
	}
	if err := newLocalAdapter(path).restoreBundles(j); err != nil {
		t.Fatal(err)
	}
}

func TestTwoSequentialSwapsKeepImmediatePreviousVersion(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "update")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{Profile: "test", Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance"), AppPID: 99999999,
		OldBundle: filepath.Join(root, "Applications", "Multica.app"), StagedBundle: filepath.Join(work, "staging", "Multica.app"),
		RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app")}
	v1 := fixtureBundle(t, j.OldBundle, "v1")
	v2 := fixtureBundle(t, j.StagedBundle, "v2")
	j.OldBinarySHA256, j.NewBinarySHA256 = v1, v2
	path := filepath.Join(work, "journal.json")
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	a := newLocalAdapter(path)
	if err := a.SwapBundles(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	v3 := fixtureBundle(t, j.StagedBundle, "v3")
	j.OldBinarySHA256, j.NewBinarySHA256, j.PreviousRollbackSHA256 = v2, v3, v1
	j.SwapStep, j.CurrentIdentity, j.StagedIdentity = 0, "", ""
	j.CandidateTreeSHA256 = ""
	if err := a.SwapBundles(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{j.OldBundle: v3, j.RollbackBundle: v2, j.OlderBundle: v1} {
		if err := verifyBundleHash(path, want); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestSwapAndRestoreRefuseIndistinguishableBundles(t *testing.T) {
	j := &Journal{OldBinarySHA256: strings.Repeat("a", 64), NewBinarySHA256: strings.Repeat("a", 64)}
	a := newLocalAdapter(filepath.Join(t.TempDir(), "journal.json"))
	if err := a.SwapBundles(context.Background(), j); err == nil {
		t.Fatal("indistinguishable candidate accepted")
	}
	if err := a.restoreBundles(j); err == nil {
		t.Fatal("indistinguishable rollback accepted")
	}
}
