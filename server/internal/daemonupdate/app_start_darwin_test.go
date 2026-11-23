package daemonupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDarwinAdoptsAlreadyRunningCandidateApp(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "Multica 繁中版.app")
	const executable = "Multica 繁中版"
	const version = "0.11.1-zh-tw.4+up.5fbd8da"
	main := filepath.Join(bundle, "Contents", "MacOS", executable)
	sleep, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{
		main:              sleep,
		bundleCLI(bundle): []byte("candidate CLI"),
		filepath.Join(bundle, "Contents", "Info.plist"): []byte(`<?xml version="1.0"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>ai.multica.test.early-app</string>
<key>CFBundleExecutable</key><string>` + executable + `</string>
<key>CFBundleShortVersionString</key><string>` + version + `</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>`),
	} {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if output, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", bundle).CombinedOutput(); err != nil {
		t.Fatalf("sign test bundle: %v: %s", err, output)
	}
	_, tree, err := legacyTreeManifest(bundle)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := hashFile(bundleCLI(bundle))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, main, "15")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	a := localAdapter{journalPath: filepath.Join(root, "journal.json")}
	j := Journal{OldBundle: bundle, NewAppExecutable: executable, NewVersion: version, NewBinarySHA256: sha, CandidateTreeSHA256: tree}
	// Give the kernel time to expose the signed image through libproc/lsof.
	for {
		if _, err := InspectLegacyProcess(child.Process.Pid); err == nil {
			break
		}
		if err := pause(ctx, 100*time.Millisecond); err != nil {
			t.Fatal("native image remained unavailable")
		}
	}
	adopted, err := a.adoptCandidateApp(ctx, &j)
	if err != nil || !adopted || !j.AppStartRequested || j.NewAppPID != child.Process.Pid ||
		j.AcceptanceAppIdentity == nil || j.AcceptanceAppIdentity.ExecutablePath != main {
		t.Fatalf("early native image was not adopted: %v, %+v", err, j)
	}
}
