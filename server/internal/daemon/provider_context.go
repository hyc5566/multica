package daemon

import (
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const (
	providerUsagePurposePrefix = "provider_usage"
	providerContextMaxAge      = 2 * time.Minute
)

type activeProviderContext struct {
	RuntimeID string
	AgentID   string
	Provider  string
	StartedAt time.Time
	Usage     *agent.ProviderContextUsage
}

func providerUsageAgentID(purpose string) (string, bool) {
	if purpose == providerUsagePurposePrefix {
		return "", true
	}
	prefix := providerUsagePurposePrefix + ":"
	if !strings.HasPrefix(purpose, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(purpose, prefix)), true
}

func (d *Daemon) registerActiveProviderContext(taskID, runtimeID, agentID, provider string, startedAt time.Time) {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	d.providerContextsMu.Lock()
	if d.providerContexts == nil {
		d.providerContexts = make(map[string]activeProviderContext)
	}
	d.providerContexts[taskID] = activeProviderContext{
		RuntimeID: runtimeID,
		AgentID:   agentID,
		Provider:  strings.ToLower(strings.TrimSpace(provider)),
		StartedAt: startedAt.UTC(),
	}
	d.providerContextsMu.Unlock()
}

func (d *Daemon) clearActiveProviderContext(taskID string) {
	d.providerContextsMu.Lock()
	delete(d.providerContexts, taskID)
	d.providerContextsMu.Unlock()
}

func (d *Daemon) updateActiveProviderContext(taskID string, usage agent.ProviderContextUsage) {
	d.providerContextsMu.Lock()
	defer d.providerContextsMu.Unlock()
	active, ok := d.providerContexts[taskID]
	if !ok {
		return
	}
	copy := cloneProviderContextUsage(usage)
	if copy.ObservedAt.IsZero() {
		copy.ObservedAt = time.Now().UTC()
	}
	active.Usage = &copy
	d.providerContexts[taskID] = active
}

func (d *Daemon) providerContextSnapshot(runtimeID, agentID, provider string, now time.Time) *agent.ProviderContextUsageSnapshot {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	if strings.TrimSpace(agentID) == "" {
		return &agent.ProviderContextUsageSnapshot{
			Scope: "active_task", Status: "unavailable", Source: "unavailable",
			Reason: "agent_scope_missing", ObservedAt: now,
			Message: "The usage request did not identify which agent's active task to inspect.",
		}
	}

	d.providerContextsMu.RLock()
	active := make([]activeProviderContext, 0, 1)
	for _, candidate := range d.providerContexts {
		if candidate.RuntimeID == runtimeID && candidate.AgentID == agentID {
			active = append(active, candidate)
		}
	}
	d.providerContextsMu.RUnlock()

	if len(active) == 0 {
		return &agent.ProviderContextUsageSnapshot{
			Scope: "active_task", Status: "unavailable", Source: "unavailable",
			Reason: "idle", ActiveTaskCount: 0, ObservedAt: now,
			Message: "This agent has no active task; completed sessions do not have a current context window.",
		}
	}
	if len(active) > 1 {
		return &agent.ProviderContextUsageSnapshot{
			Scope: "active_task", Status: "partial", Source: "unavailable",
			Reason: "multiple_active_tasks", ActiveTaskCount: len(active), ObservedAt: now,
			Message: "This agent has multiple active tasks; their independent context windows are not merged.",
		}
	}

	current := active[0]
	if current.Usage == nil {
		reason := "telemetry_pending"
		status := "partial"
		message := "The active task has not emitted a context-window snapshot yet."
		if strings.EqualFold(provider, "antigravity") || strings.EqualFold(current.Provider, "antigravity") {
			reason = "provider_unsupported"
			status = "unavailable"
			message = "Antigravity's Multica launch mode does not expose live context-window telemetry."
		}
		return &agent.ProviderContextUsageSnapshot{
			Scope: "active_task", Status: status, Source: "unavailable", Reason: reason,
			ActiveTaskCount: 1, ObservedAt: current.StartedAt, Message: message,
		}
	}

	usage := cloneProviderContextUsage(*current.Usage)
	snapshot := &agent.ProviderContextUsageSnapshot{
		Scope: "active_task", Status: usage.Status, Source: usage.Source, Reason: usage.Reason,
		ActiveTaskCount: 1, UsedTokens: usage.UsedTokens, MaxTokens: usage.MaxTokens,
		RemainingTokens: usage.RemainingTokens, UsedPercent: usage.UsedPercent,
		ObservedAt: usage.ObservedAt, Message: usage.Message,
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = current.StartedAt
	}
	if now.Sub(snapshot.ObservedAt) > providerContextMaxAge {
		snapshot.Status = "partial"
		snapshot.Reason = "stale"
		snapshot.Message = "The active task's last context snapshot is stale; displayed values may lag the provider session."
	}
	return snapshot
}

func cloneProviderContextUsage(usage agent.ProviderContextUsage) agent.ProviderContextUsage {
	cloneInt64 := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		copy := *value
		return &copy
	}
	cloneFloat64 := func(value *float64) *float64 {
		if value == nil {
			return nil
		}
		copy := *value
		return &copy
	}
	usage.UsedTokens = cloneInt64(usage.UsedTokens)
	usage.MaxTokens = cloneInt64(usage.MaxTokens)
	usage.RemainingTokens = cloneInt64(usage.RemainingTokens)
	usage.UsedPercent = cloneFloat64(usage.UsedPercent)
	return usage
}
