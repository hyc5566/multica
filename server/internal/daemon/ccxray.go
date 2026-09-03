package daemon

import (
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ccxrayLaunch wraps only provider-owned Claude and Codex commands. Custom
// profiles may already be wrappers and must keep their declared argv intact.
// A missing optional binary degrades to the ordinary provider launch.
func ccxrayLaunch(enabled bool, customProfile bool, provider, executable string, prefix []string) (string, []string, bool) {
	if !enabled || customProfile || (provider != "claude" && provider != "codex") {
		return executable, prefix, false
	}
	ccxray, err := lookPath("ccxray")
	if err != nil {
		return executable, prefix, false
	}
	wrappedPrefix := make([]string, 0, len(prefix)+2)
	wrappedPrefix = append(wrappedPrefix, provider, "--no-browser")
	wrappedPrefix = append(wrappedPrefix, prefix...)
	return ccxray, wrappedPrefix, true
}

func (d *Daemon) ccxrayHealthSummaryForRuntime(runtimeID string) *protocol.CCXRayHealthSummary {
	d.mu.Lock()
	runtime, ok := d.runtimeIndex[runtimeID]
	d.mu.Unlock()
	if !ok || !ccxraySupportsProvider(runtime.Provider) {
		return nil
	}
	summary := ccxrayHealthSummary(d.cfg.CCXRayEnabled, time.Now())
	// ccxray itself supports Grok, so the UI advertises availability, but the
	// current Multica launcher has not yet added a Grok wrapper. Never claim an
	// observing state for a provider whose task argv is still direct.
	if runtime.Provider == "grok" && summary.Status == protocol.CCXRayStatusObserving {
		summary.Status = protocol.CCXRayStatusDegraded
		summary.LastErrorCode = "provider_launch_not_integrated"
	}
	return summary
}

func ccxrayHealthSummary(enabled bool, now time.Time) *protocol.CCXRayHealthSummary {
	_, err := lookPath("ccxray")
	installed := err == nil
	status := protocol.CCXRayStatusDisabled
	errorCode := ""
	if enabled && !installed {
		status = protocol.CCXRayStatusNotInstalled
		errorCode = "executable_not_found"
	} else if enabled {
		status = protocol.CCXRayStatusObserving
	}
	return &protocol.CCXRayHealthSummary{
		Enabled:       enabled,
		Installed:     installed,
		Status:        status,
		Version:       "",
		ObservedAt:    now.UTC().Truncate(time.Minute).Format(time.RFC3339),
		LastErrorCode: errorCode,
	}
}

func ccxraySupportsProvider(provider string) bool {
	switch provider {
	case "claude", "codex", "grok":
		return true
	default:
		return false
	}
}
