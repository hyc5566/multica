package daemonupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIEntryRejectsIndependentManagedAndPathCopies(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "App.app")
	fixtureBundle(t, bundle, "app")
	work := filepath.Join(root, "userdata", "zh-tw-update")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	pathDir := filepath.Join(root, "path")
	if err := os.MkdirAll(pathDir, 0700); err != nil {
		t.Fatal(err)
	}
	entry := bundleCLI(bundle)
	if err := verifyCLIEntry(bundle, entry, work, []string{pathDir}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(entry, filepath.Join(pathDir, "multica")); err != nil {
		t.Fatal(err)
	}
	if err := verifyCLIEntry(bundle, filepath.Join(pathDir, "multica"), work, []string{pathDir}); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "userdata", "bin", "multica")
	if err := os.MkdirAll(filepath.Dir(managed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("standalone"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyCLIEntry(bundle, entry, work, []string{pathDir}); err == nil {
		t.Fatal("independent managed binary accepted")
	}
	if err := os.Remove(managed); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(pathDir, "multica")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pathDir, "multica"), []byte("standalone"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyCLIEntry(bundle, entry, work, []string{pathDir}); err == nil {
		t.Fatal("independent PATH binary accepted")
	}
}
