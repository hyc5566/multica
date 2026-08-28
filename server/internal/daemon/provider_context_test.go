package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestProviderUsageAgentID(t *testing.T) {
	t.Parallel()
	if agentID, ok := providerUsageAgentID("provider_usage:agent-1"); !ok || agentID != "agent-1" {
		t.Fatalf("scoped purpose = %q, %v", agentID, ok)
	}
	if agentID, ok := providerUsageAgentID("provider_usage"); !ok || agentID != "" {
		t.Fatalf("legacy purpose = %q, %v", agentID, ok)
	}
	if _, ok := providerUsageAgentID("models"); ok {
		t.Fatal("model request must not be classified as provider usage")
	}
}

func TestProviderContextSnapshotIdlePendingAndUnsupported(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	d := &Daemon{providerContexts: make(map[string]activeProviderContext)}

	idle := d.providerContextSnapshot("runtime-1", "agent-1", "codex", now)
	if idle.Status != "unavailable" || idle.Reason != "idle" || idle.ActiveTaskCount != 0 || idle.UsedTokens != nil {
		t.Fatalf("idle = %+v", idle)
	}

	d.registerActiveProviderContext("task-1", "runtime-1", "agent-1", "codex", now.Add(-time.Minute))
	pending := d.providerContextSnapshot("runtime-1", "agent-1", "codex", now)
	if pending.Status != "partial" || pending.Reason != "telemetry_pending" || pending.ActiveTaskCount != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	d.clearActiveProviderContext("task-1")

	d.registerActiveProviderContext("task-2", "runtime-1", "agent-1", "antigravity", now.Add(-time.Minute))
	unsupported := d.providerContextSnapshot("runtime-1", "agent-1", "antigravity", now)
	if unsupported.Status != "unavailable" || unsupported.Reason != "provider_unsupported" || unsupported.UsedTokens != nil {
		t.Fatalf("unsupported = %+v", unsupported)
	}
}

func TestProviderContextSnapshotAvailableStaleAndMultiple(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	d := &Daemon{providerContexts: make(map[string]activeProviderContext)}
	d.registerActiveProviderContext("task-1", "runtime-1", "agent-1", "codex", now.Add(-time.Minute))
	used, maxTokens, remaining := int64(125274), int64(258400), int64(133126)
	percent := float64(used) / float64(maxTokens) * 100
	d.updateActiveProviderContext("task-1", agent.ProviderContextUsage{
		Status: "available", Source: "official", UsedTokens: &used, MaxTokens: &maxTokens,
		RemainingTokens: &remaining, UsedPercent: &percent, ObservedAt: now.Add(-time.Minute),
	})

	available := d.providerContextSnapshot("runtime-1", "agent-1", "codex", now)
	if available.Status != "available" || available.Source != "official" || available.UsedTokens == nil || *available.UsedTokens != used {
		t.Fatalf("available = %+v", available)
	}

	stale := d.providerContextSnapshot("runtime-1", "agent-1", "codex", now.Add(providerContextMaxAge+time.Second))
	if stale.Status != "partial" || stale.Reason != "stale" || stale.UsedTokens == nil {
		t.Fatalf("stale = %+v", stale)
	}

	d.registerActiveProviderContext("task-2", "runtime-1", "agent-1", "codex", now)
	multiple := d.providerContextSnapshot("runtime-1", "agent-1", "codex", now)
	if multiple.Status != "partial" || multiple.Reason != "multiple_active_tasks" || multiple.ActiveTaskCount != 2 || multiple.UsedTokens != nil {
		t.Fatalf("multiple = %+v", multiple)
	}
}

func TestProviderContextSnapshotCredentialFreeJSON(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	d := &Daemon{providerContexts: make(map[string]activeProviderContext)}
	d.registerActiveProviderContext("task-secret", "runtime-secret", "agent-secret", "claude", now)
	used := int64(42000)
	d.updateActiveProviderContext("task-secret", agent.ProviderContextUsage{
		Status: "partial", Source: "derived", Reason: "max_unavailable", UsedTokens: &used,
		ObservedAt: now, Message: "maximum unavailable",
	})
	raw, err := json.Marshal(d.providerContextSnapshot("runtime-secret", "agent-secret", "claude", now))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"task-secret", "runtime-secret", "agent-secret", "access_token", "cookie", "api_key"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(forbidden)) {
			t.Fatalf("credential-free snapshot leaked %q: %s", forbidden, raw)
		}
	}
}
