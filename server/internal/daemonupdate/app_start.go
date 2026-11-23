package daemonupdate

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

var installedAppVersion = func(ctx context.Context, bundle string) (string, error) {
	return (macInspector{}).BundleValue(ctx, bundle, "CFBundleShortVersionString")
}

// adoptCandidateApp accepts one GUI whose running image belongs to the sealed
// candidate tree. The renderer still has to acknowledge the selected PID.
func (a *localAdapter) adoptCandidateApp(ctx context.Context, j *Journal) (bool, error) {
	if runtime.GOOS != "darwin" {
		return false, nil
	}
	app, err := findNativeApp(j.OldBundle, j.NewAppExecutable)
	if err != nil || app.PID == 0 {
		return false, err
	}
	if err := verifyEarlyApp(ctx, j, app); err != nil {
		return false, err
	}
	j.AppStartRequested = true
	j.NewAppPID = app.PID
	j.AcceptanceAppIdentity = &app
	return true, SaveJournal(a.journalPath, *j)
}

func verifyEarlyApp(ctx context.Context, j *Journal, app LegacyProcessIdentity) error {
	if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
		return err
	}
	records, tree, err := legacyTreeManifest(j.OldBundle)
	if err != nil || tree != j.CandidateTreeSHA256 {
		return errors.New("early App candidate tree mismatch")
	}
	version, err := installedAppVersion(ctx, j.OldBundle)
	if err != nil || version != j.NewVersion {
		return errors.New("early App version mismatch")
	}
	relative := filepath.Join("Contents", "MacOS", j.NewAppExecutable)
	if app.ExecutablePath != filepath.Join(j.OldBundle, relative) {
		return errors.New("early App executable path mismatch")
	}
	for _, record := range records {
		if record.Path == relative && strings.HasPrefix(record.Mode, "-") && record.Value == app.SHA256 {
			current, err := inspectLegacy(app.PID)
			if err != nil || current != app {
				return errors.New("early App process changed during verification")
			}
			return nil
		}
	}
	return errors.New("early App executable digest mismatch")
}
