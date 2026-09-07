package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const (
	taskQuotaCacheFreshFor = 30 * time.Second
	taskQuotaProbeTimeout  = 3 * time.Second
	taskQuotaReportTimeout = 2 * time.Second
)

type taskQuotaCachedObservation struct {
	snapshot agent.ProviderUsage
	cachedAt time.Time
}

type taskQuotaProbeResult struct {
	// snapshot is the task-facing last-known-good value. reportSnapshot is the
	// actual attempt result used by the scheduled refresh, so a stale fallback
	// never masquerades as a new success in the Server's LKG record.
	snapshot       agent.ProviderUsage
	reportSnapshot agent.ProviderUsage
	state          string
	errorCode      string
}

type taskQuotaProbeCall struct {
	done   chan struct{}
	result taskQuotaProbeResult
}

func taskQuotaProbeKey(rt Runtime, provider string) string {
	if profileID := strings.TrimSpace(rt.ProfileID); profileID != "" {
		return "profile:" + profileID
	}
	return "builtin:" + strings.ToLower(strings.TrimSpace(provider))
}

func taskQuotaProviderSupported(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "claude", "antigravity":
		return true
	default:
		return false
	}
}

func quotaErrorCode(status string) string {
	switch status {
	case "auth_required", "rate_limited":
		return status
	case "unavailable":
		return "unsupported"
	case "error":
		return "provider_error"
	default:
		return ""
	}
}

func (d *Daemon) observeTaskQuota(ctx context.Context, rt Runtime, provider string) taskQuotaProbeResult {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !taskQuotaProviderSupported(provider) {
		snapshot := agent.ProviderUsage{Provider: provider, Status: "unavailable", Source: "unavailable", ObservedAt: time.Now().UTC()}
		return taskQuotaProbeResult{snapshot: snapshot, reportSnapshot: snapshot, state: "unsupported", errorCode: "unsupported"}
	}
	key := taskQuotaProbeKey(rt, provider)
	now := time.Now().UTC()
	d.taskQuotaMu.Lock()
	if cached, ok := d.taskQuotaCache[key]; ok && now.Sub(cached.cachedAt) <= taskQuotaCacheFreshFor {
		d.taskQuotaMu.Unlock()
		return taskQuotaProbeResult{snapshot: cached.snapshot, reportSnapshot: cached.snapshot, state: "cached"}
	}
	if call, ok := d.taskQuotaInflight[key]; ok {
		d.taskQuotaMu.Unlock()
		select {
		case <-call.done:
			return call.result
		case <-ctx.Done():
			snapshot := agent.ProviderUsage{Provider: provider, Status: "error", Source: "unavailable", ObservedAt: time.Now().UTC()}
			return taskQuotaProbeResult{snapshot: snapshot, reportSnapshot: snapshot, state: "timeout", errorCode: "timeout"}
		}
	}
	call := &taskQuotaProbeCall{done: make(chan struct{})}
	if d.taskQuotaInflight == nil {
		d.taskQuotaInflight = make(map[string]*taskQuotaProbeCall)
	}
	d.taskQuotaInflight[key] = call
	probe := d.taskQuotaProbeFn
	if probe == nil {
		probe = agent.ProbeProviderUsage
	}
	d.taskQuotaMu.Unlock()

	probeCtx, cancel := context.WithTimeout(ctx, taskQuotaProbeTimeout)
	snapshot := probe(probeCtx, provider, agent.Command{})
	probeErr := probeCtx.Err()
	cancel()
	result := taskQuotaProbeResult{snapshot: snapshot, reportSnapshot: snapshot, state: "fresh", errorCode: quotaErrorCode(snapshot.Status)}
	if probeErr != nil {
		result.state = "timeout"
		result.errorCode = "timeout"
	}
	good := snapshot.Status == "available" || snapshot.Status == "partial"
	if !good && probeErr == nil && result.errorCode != "" {
		result.state = result.errorCode
	}

	d.taskQuotaMu.Lock()
	if good {
		if d.taskQuotaCache == nil {
			d.taskQuotaCache = make(map[string]taskQuotaCachedObservation)
		}
		d.taskQuotaCache[key] = taskQuotaCachedObservation{snapshot: snapshot, cachedAt: time.Now().UTC()}
	} else if cached, ok := d.taskQuotaCache[key]; ok {
		result.snapshot = cached.snapshot
		result.state = "stale_cache"
	}
	call.result = result
	delete(d.taskQuotaInflight, key)
	close(call.done)
	d.taskQuotaMu.Unlock()
	return result
}

func (d *Daemon) captureTaskQuotaCheckpoint(ctx context.Context, task Task, rt Runtime, provider, phase string, taskLog interface {
	Warn(string, ...any)
}) {
	// New wires the probe in production. A nil hook keeps deliberately minimal
	// Daemon values used by embedders and legacy tests backward compatible.
	if d.taskQuotaProbeFn == nil || d.client == nil {
		return
	}
	boundaryAt := time.Now().UTC()
	captureCtx := ctx
	if phase == "after" {
		// A daemon shutdown cancels the run context first. Keep this synchronous
		// cleanup attempt alive only under the probe/report deadlines above.
		captureCtx = context.WithoutCancel(ctx)
	}
	result := d.observeTaskQuota(captureCtx, rt, provider)
	if phase == "before" {
		boundaryAt = time.Now().UTC()
	}
	model := ""
	if task.Agent != nil {
		model = task.Agent.Model
	}
	reportCtx, cancel := context.WithTimeout(captureCtx, taskQuotaReportTimeout)
	defer cancel()
	if err := d.client.ReportTaskQuotaCheckpoint(reportCtx, task.ID, phase, model, result.state, result.errorCode, boundaryAt, result.snapshot); err != nil {
		taskLog.Warn("report task quota checkpoint failed", "phase", phase, "error", err)
	}
}
