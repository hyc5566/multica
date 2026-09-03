package daemon

import (
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
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

// shouldFailOpenCCXRay permits one direct Codex retry only while the wrapper
// is still infrastructure: no provider session exists and no tool ran. Once a
// session or tool exists, retrying could duplicate user-visible side effects.
func shouldFailOpenCCXRay(enabled bool, provider string, result agent.Result, toolCount int32, executeErr error) bool {
	if !enabled || provider != "codex" || result.SessionID != "" || toolCount != 0 {
		return false
	}
	if executeErr != nil {
		return true
	}
	return result.Status == "failed" &&
		(strings.Contains(result.Error, agent.CodexHandshakeTimeoutMarker) ||
			strings.Contains(result.Error, "codex process exited"))
}
