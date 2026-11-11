package daemonupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Fixtures use their own processes and must not inspect the developer's host.
func init() { checkSharedBundle = func(string) error { return nil } }

func TestSharedBundleBlocksBeforeGateAndExchange(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	work := filepath.Join(root, "zh-tw-update")
	for _, dir := range []string{profile, work} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	j := &Journal{Profile: "test", Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance"), AppPID: 99999991, OldBundle: filepath.Join(root, "Applications", "Multica.app"), StagedBundle: filepath.Join(work, "staging", "Next.app"), RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app")}
	j.OldBinarySHA256 = fixtureBundle(t, j.OldBundle, "old")
	j.NewBinarySHA256 = fixtureBundle(t, j.StagedBundle, "new")
	previous := checkSharedBundle
	defer func() { checkSharedBundle = previous }()
	calls := 0
	checkSharedBundle = func(bundle string) error {
		calls++
		if bundle != j.OldBundle {
			t.Fatalf("wrong bundle: %s", bundle)
		}
		return errors.New("App bundle is in use by another user")
	}
	path := filepath.Join(work, "journal.json")
	a := newLocalAdapter(path)
	if err := a.AcquireGate(context.Background(), j); err == nil {
		t.Fatal("shared App acquired gate")
	}
	if _, err := os.Stat(j.GatePath); !os.IsNotExist(err) {
		t.Fatal("gate created despite shared App")
	}
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	if err := a.SwapBundles(context.Background(), j); err == nil {
		t.Fatal("shared App exchanged")
	}
	if err := verifyBundleHash(j.OldBundle, j.OldBinarySHA256); err != nil {
		t.Fatal("original App changed", err)
	}
	if err := verifyBundleHash(j.StagedBundle, j.NewBinarySHA256); err != nil {
		t.Fatal("staged App changed", err)
	}
	if calls != 2 {
		t.Fatalf("shared checks = %d, want 2", calls)
	}
}
