package daemon

import "os/exec"

// ccxrayLaunch wraps only provider-owned Claude and Codex commands. Custom
// profiles may already be wrappers and must keep their declared argv intact.
// A missing optional binary degrades to the ordinary provider launch.
func ccxrayLaunch(enabled bool, customProfile bool, provider, executable string, prefix []string) (string, []string, bool) {
	if !enabled || customProfile || (provider != "claude" && provider != "codex") {
		return executable, prefix, false
	}
	ccxray, err := exec.LookPath("ccxray")
	if err != nil {
		return executable, prefix, false
	}
	wrappedPrefix := make([]string, 0, len(prefix)+2)
	wrappedPrefix = append(wrappedPrefix, provider, "--no-browser")
	wrappedPrefix = append(wrappedPrefix, prefix...)
	return ccxray, wrappedPrefix, true
}
