package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

// ProviderUsage is a normalized, credential-free snapshot of provider account
// quota. Percentages are always in the 0..100 range; a provider that does not
// publish a value leaves it nil rather than fabricating zero.
type ProviderUsage struct {
	Provider          string                `json:"provider"`
	AccountScope      string                `json:"account_scope,omitempty"`
	Status            string                `json:"status"`
	Source            string                `json:"source"`
	Windows           []ProviderUsageWindow `json:"windows,omitempty"`
	ObservedAt        time.Time             `json:"observed_at"`
	Message           string                `json:"message,omitempty"`
	RetryAfterSeconds *int64                `json:"retry_after_seconds,omitempty"`
}

type ProviderUsageWindow struct {
	ID                 string     `json:"id"`
	Group              string     `json:"group,omitempty"`
	Label              string     `json:"label"`
	UsedPercent        *float64   `json:"used_percent,omitempty"`
	RemainingPercent   *float64   `json:"remaining_percent,omitempty"`
	WindowDurationMins *int64     `json:"window_duration_mins,omitempty"`
	ResetsAt           *time.Time `json:"resets_at,omitempty"`
	Unit               string     `json:"unit"`
}

const providerUsageProbeMaxOutput = 2 * 1024 * 1024

//go:embed provider_usage_probe.py
var providerUsageProbeScript string

var providerUsageProbeGroup singleflight.Group

// ProbeProviderUsage runs the bundled deterministic direct-HTTP probe. The
// helper reads the runtime user's local OAuth credential and never starts an
// agent CLI, creates a model turn, or returns credential material.
//
// The Command argument is retained for source compatibility with older daemon
// callers. It is deliberately ignored: provider usage must not depend on an
// agent executable or ask an agent to interpret provider output.
func ProbeProviderUsage(ctx context.Context, provider string, _ Command) ProviderUsage {
	now := time.Now().UTC()
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "codex", "antigravity", "claude":
	default:
		return ProviderUsage{
			Provider:   provider,
			Status:     "unavailable",
			Source:     "unavailable",
			ObservedAt: now,
			Message:    "This runtime does not expose a supported account-quota source.",
		}
	}
	usage, err := coalesceProviderUsage(provider, func() (ProviderUsage, error) {
		return runProviderUsageProbe(ctx, provider)
	})
	if err != nil {
		return ProviderUsage{
			Provider:   provider,
			Status:     "error",
			Source:     "unavailable",
			ObservedAt: now,
			Message:    "The local direct provider usage probe could not run.",
		}
	}
	return usage
}

func coalesceProviderUsage(
	provider string,
	probe func() (ProviderUsage, error),
) (ProviderUsage, error) {
	value, err, _ := providerUsageProbeGroup.Do(provider, func() (any, error) {
		return probe()
	})
	if err != nil {
		return ProviderUsage{}, err
	}
	usage, ok := value.(ProviderUsage)
	if !ok {
		return ProviderUsage{}, errors.New("provider usage probe returned an invalid result")
	}
	return usage, nil
}

func runProviderUsageProbe(ctx context.Context, provider string) (ProviderUsage, error) {
	python, err := providerUsagePython()
	if err != nil {
		return ProviderUsage{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := osexec.CommandContext(probeCtx, python, "-", provider)
	cmd.Stdin = strings.NewReader(providerUsageProbeScript)
	cmd.Stderr = io.Discard
	raw, err := cmd.Output()
	if err != nil {
		return ProviderUsage{}, err
	}
	if len(raw) > providerUsageProbeMaxOutput {
		return ProviderUsage{}, errors.New("provider usage probe output too large")
	}
	return parseProviderUsageProbe(raw, provider)
}

func providerUsagePython() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("MULTICA_PROVIDER_USAGE_PYTHON")); configured != "" {
		return osexec.LookPath(configured)
	}
	if path, err := osexec.LookPath("python3"); err == nil {
		return path, nil
	}
	return osexec.LookPath("python")
}

func parseProviderUsageProbe(raw []byte, provider string) (ProviderUsage, error) {
	var usage ProviderUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return ProviderUsage{}, err
	}
	if usage.Provider != provider {
		return ProviderUsage{}, errors.New("provider usage probe returned a mismatched provider")
	}
	switch usage.Status {
	case "available", "partial", "unavailable", "auth_required", "rate_limited", "error":
	default:
		return ProviderUsage{}, errors.New("provider usage probe returned an invalid status")
	}
	switch usage.Source {
	case "official", "derived", "unavailable":
	default:
		return ProviderUsage{}, errors.New("provider usage probe returned an invalid source")
	}
	if usage.ObservedAt.IsZero() {
		return ProviderUsage{}, errors.New("provider usage probe returned no observation time")
	}
	if usage.RetryAfterSeconds != nil && *usage.RetryAfterSeconds <= 0 {
		return ProviderUsage{}, errors.New("provider usage probe returned an invalid retry interval")
	}
	for _, window := range usage.Windows {
		if strings.TrimSpace(window.ID) == "" || strings.TrimSpace(window.Label) == "" || window.Unit != "percent" {
			return ProviderUsage{}, errors.New("provider usage probe returned an invalid quota window")
		}
		if window.WindowDurationMins != nil && *window.WindowDurationMins <= 0 {
			return ProviderUsage{}, errors.New("provider usage probe returned an invalid quota duration")
		}
		for _, percent := range []*float64{window.UsedPercent, window.RemainingPercent} {
			if percent != nil && (*percent < 0 || *percent > 100 || math.IsNaN(*percent) || math.IsInf(*percent, 0)) {
				return ProviderUsage{}, errors.New("provider usage probe returned an invalid percentage")
			}
		}
	}
	return usage, nil
}
