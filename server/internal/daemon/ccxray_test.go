package daemon

import "testing"

func TestCCXRayLaunchDisabled(t *testing.T) {
	path, prefix, enabled := ccxrayLaunch(false, false, "codex", "/bin/codex", []string{"exec"})
	if enabled || path != "/bin/codex" || len(prefix) != 1 || prefix[0] != "exec" {
		t.Fatalf("ccxrayLaunch disabled = %q %v %v", path, prefix, enabled)
	}
}

func TestCCXRayLaunchSkipsUnsupportedAndCustomRuntimes(t *testing.T) {
	for _, tc := range []struct {
		provider string
		custom   bool
	}{
		{provider: "cursor"},
		{provider: "claude", custom: true},
	} {
		path, _, enabled := ccxrayLaunch(true, tc.custom, tc.provider, "/bin/provider", nil)
		if enabled || path != "/bin/provider" {
			t.Fatalf("ccxrayLaunch(%q, custom=%v) unexpectedly wrapped", tc.provider, tc.custom)
		}
	}
}
