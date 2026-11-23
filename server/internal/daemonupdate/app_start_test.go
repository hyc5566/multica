package daemonupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyEarlyAppBindsSealedCandidateAndNativeProcess(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "Multica 繁中版.app")
	executable := "Multica 繁中版"
	path := filepath.Join(bundle, "Contents", "MacOS", executable)
	for file, content := range map[string]string{path: "candidate GUI", bundleCLI(bundle): "candidate CLI"} {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	_, tree, err := legacyTreeManifest(bundle)
	if err != nil {
		t.Fatal(err)
	}
	cliSHA, err := hashFile(bundleCLI(bundle))
	if err != nil {
		t.Fatal(err)
	}
	appSHA, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	j := Journal{OldBundle: bundle, NewAppExecutable: executable, NewVersion: "0.11.1-zh-tw.4+up.5fbd8da", NewBinarySHA256: cliSHA, CandidateTreeSHA256: tree}
	app := LegacyProcessIdentity{PID: 123, UID: os.Getuid(), StartedAt: "original", ExecutablePath: path, Inode: 345, Device: 1, SHA256: appSHA}
	sourceVersion, inspected := j.NewVersion, app
	originalVersion, originalInspect := installedAppVersion, inspectLegacy
	t.Cleanup(func() { installedAppVersion, inspectLegacy = originalVersion, originalInspect })
	installedAppVersion = func(context.Context, string) (string, error) { return sourceVersion, nil }
	inspectLegacy = func(int) (LegacyProcessIdentity, error) { return inspected, nil }
	if err := verifyEarlyApp(context.Background(), &j, app); err != nil {
		t.Fatalf("valid installed App: %v", err)
	}
	wrongImage := app
	wrongImage.SHA256 = strings.Repeat("0", 64)
	if err := verifyEarlyApp(context.Background(), &j, wrongImage); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("wrong mapped image: %v", err)
	}
	wrongPath := app
	wrongPath.ExecutablePath += ".copy"
	if err := verifyEarlyApp(context.Background(), &j, wrongPath); err == nil || !strings.Contains(err.Error(), "path mismatch") {
		t.Fatalf("wrong mapped path: %v", err)
	}
	sourceVersion = "0.11.1-zh-tw.3+up.5fbd8da"
	if err := verifyEarlyApp(context.Background(), &j, app); err == nil || !strings.Contains(err.Error(), "version mismatch") {
		t.Fatalf("wrong installed version: %v", err)
	}
	sourceVersion = j.NewVersion
	inspected.StartedAt = "different"
	if err := verifyEarlyApp(context.Background(), &j, app); err == nil || !strings.Contains(err.Error(), "process changed") {
		t.Fatalf("reused PID: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed GUI"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyEarlyApp(context.Background(), &j, app); err == nil || !strings.Contains(err.Error(), "tree mismatch") {
		t.Fatalf("changed installed tree: %v", err)
	}
}
