package main

import (
	"strings"
	"testing"
)

func TestRunUpdateRejectsNonPositiveDownloadTimeout(t *testing.T) {
	orig := updateDownloadTimeout
	updateDownloadTimeout = 0
	t.Cleanup(func() { updateDownloadTimeout = orig })

	err := runUpdate(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "download timeout must be greater than zero") {
		t.Fatalf("runUpdate error = %v, want download timeout validation", err)
	}
}

func TestRunTaiwanUpdateRequiresCoordinatedUpdaterBeforeNetwork(t *testing.T) {
	originalVersion := version
	t.Cleanup(func() { version = originalVersion })
	for _, current := range []string{"0.8.1-zh-tw.4", "0.8.1-zh-tw.4-rc.1", "dev"} {
		t.Run(current, func(t *testing.T) {
			version = current
			t.Setenv("MULTICA_ZH_TW_RELEASE_MANIFEST_URL", "https://invalid.example/no-network-expected")
			err := runUpdate(nil, nil)
			if err == nil || !strings.Contains(err.Error(), "multica update 不會替換程式") {
				t.Fatalf("runUpdate error = %v, want coordinated updater guidance", err)
			}
		})
	}
}
