package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProviderUsage is a normalized, credential-free snapshot of the account
// quota exposed by a runtime's own CLI. Percentages are always in the 0..100
// range; providers that do not publish a value leave it nil rather than
// fabricating zero.
type ProviderUsage struct {
	Provider     string                `json:"provider"`
	AccountScope string                `json:"account_scope,omitempty"`
	Status       string                `json:"status"`
	Source       string                `json:"source"`
	Windows      []ProviderUsageWindow `json:"windows,omitempty"`
	ObservedAt   time.Time             `json:"observed_at"`
	Message      string                `json:"message,omitempty"`
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

// ProbeProviderUsage reads the provider's safest supported local account-quota
// source. It never reads credential files directly and never returns raw CLI
// diagnostics, which can contain account or authentication details.
func ProbeProviderUsage(ctx context.Context, provider string, cmd Command) ProviderUsage {
	now := time.Now().UTC()
	provider = strings.ToLower(strings.TrimSpace(provider))
	var usage ProviderUsage
	var err error
	switch provider {
	case "codex":
		usage, err = probeCodexUsage(ctx, cmd)
	case "antigravity":
		usage, err = probeAntigravityUsage(ctx, cmd)
	case "claude":
		usage, err = probeClaudeUsage()
	default:
		return ProviderUsage{
			Provider:   provider,
			Status:     "unavailable",
			Source:     "unavailable",
			ObservedAt: now,
			Message:    "This runtime does not expose a supported account-quota source.",
		}
	}
	if err != nil {
		status := "error"
		message := "The runtime CLI could not provide an account-quota snapshot."
		if isUsageAuthError(err) {
			status = "auth_required"
			message = "The runtime CLI requires sign-in before account quota can be read."
		}
		return ProviderUsage{
			Provider:   provider,
			Status:     status,
			Source:     "unavailable",
			ObservedAt: now,
			Message:    message,
		}
	}
	usage.Provider = provider
	if usage.ObservedAt.IsZero() {
		usage.ObservedAt = now
	}
	return usage
}

const (
	claudeUsageSnapshotEnv = "MULTICA_CLAUDE_USAGE_SNAPSHOT"
	claudeUsageMaxAge      = 30 * time.Minute
)

// probeClaudeUsage reads a credential-free cache populated by Claude Code's
// statusLine input. Claude Code has no non-interactive /usage command, but its
// documented status-line payload includes the official five-hour and seven-day
// quota windows after the first API response in a subscriber session. A status
// line can copy only those fields to this cache without exposing auth material.
func probeClaudeUsage() (ProviderUsage, error) {
	path := strings.TrimSpace(os.Getenv(claudeUsageSnapshotEnv))
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ProviderUsage{}, err
		}
		path = filepath.Join(home, ".multica", "provider-usage", "claude.json")
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ProviderUsage{
			Status:  "unavailable",
			Source:  "unavailable",
			Message: "No Claude Code status-line quota snapshot is available yet. Run one Claude Code turn with usage capture enabled, then refresh.",
		}, nil
	}
	if err != nil {
		return ProviderUsage{}, err
	}
	return parseClaudeUsage(raw, time.Now().UTC())
}

func parseClaudeUsage(raw []byte, now time.Time) (ProviderUsage, error) {
	var snapshot struct {
		ObservedAt time.Time `json:"observed_at"`
		RateLimits struct {
			FiveHour *struct {
				UsedPercentage *float64 `json:"used_percentage"`
				ResetsAt       *int64   `json:"resets_at"`
			} `json:"five_hour"`
			SevenDay *struct {
				UsedPercentage *float64 `json:"used_percentage"`
				ResetsAt       *int64   `json:"resets_at"`
			} `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return ProviderUsage{}, err
	}
	if snapshot.ObservedAt.IsZero() {
		return ProviderUsage{}, errors.New("claude usage snapshot has no observation time")
	}

	usage := ProviderUsage{
		Status:     "available",
		Source:     "official",
		ObservedAt: snapshot.ObservedAt.UTC(),
	}
	appendWindow := func(id, label string, duration int64, usedPercentage *float64, resetsAt *int64) {
		if usedPercentage == nil && resetsAt == nil {
			return
		}
		window := ProviderUsageWindow{
			ID: id, Group: "Claude Code", Label: label, WindowDurationMins: &duration, Unit: "percent",
		}
		if usedPercentage != nil {
			used := clampPercent(*usedPercentage)
			remaining := clampPercent(100 - used)
			window.UsedPercent = &used
			window.RemainingPercent = &remaining
		}
		if resetsAt != nil {
			reset := time.Unix(*resetsAt, 0).UTC()
			window.ResetsAt = &reset
		}
		usage.Windows = append(usage.Windows, window)
	}
	if snapshot.RateLimits.FiveHour != nil {
		appendWindow("five-hour", "5 hour limit", 300, snapshot.RateLimits.FiveHour.UsedPercentage, snapshot.RateLimits.FiveHour.ResetsAt)
	}
	if snapshot.RateLimits.SevenDay != nil {
		appendWindow("seven-day", "Weekly limit", 10080, snapshot.RateLimits.SevenDay.UsedPercentage, snapshot.RateLimits.SevenDay.ResetsAt)
	}
	if len(usage.Windows) == 0 {
		usage.Status = "partial"
		usage.Message = "Claude Code's latest status-line snapshot did not include subscriber quota windows."
		return usage, nil
	}
	if now.Sub(usage.ObservedAt) > claudeUsageMaxAge {
		usage.Status = "partial"
		usage.Message = "This is Claude Code's last official snapshot; start or resume a Claude Code session to refresh it."
	}
	return usage, nil
}

func isUsageAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not logged in") ||
		strings.Contains(s, "login required") ||
		strings.Contains(s, "authentication required") ||
		strings.Contains(s, "unauthorized")
}

func clampPercent(v float64) float64 {
	return math.Max(0, math.Min(100, v))
}

type codexRateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

type codexRateLimitSnapshot struct {
	LimitID   string                `json:"limitId"`
	LimitName *string               `json:"limitName"`
	Primary   *codexRateLimitWindow `json:"primary"`
	Secondary *codexRateLimitWindow `json:"secondary"`
	PlanType  *string               `json:"planType"`
}

func probeCodexUsage(ctx context.Context, command Command) (ProviderUsage, error) {
	if command.Path == "" {
		command.Path = "codex"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := command.exec(probeCtx, "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ProviderUsage{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ProviderUsage{}, err
	}
	cmd.Stderr = newStderrTail(io.Discard, 2048)
	if err := cmd.Start(); err != nil {
		return ProviderUsage{}, err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	write := func(v any) error {
		return json.NewEncoder(stdin).Encode(v)
	}
	if err := write(map[string]any{
		"id":     1,
		"method": "initialize",
		"params": map[string]any{
			"clientInfo": map[string]any{
				"name": "multica-usage-probe", "title": "Multica usage probe", "version": "1",
			},
			"capabilities": map[string]any{"experimentalApi": true},
		},
	}); err != nil {
		return ProviderUsage{}, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	initialized := false
	for scanner.Scan() {
		var envelope struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &envelope) != nil {
			continue
		}
		if envelope.ID == 1 && !initialized {
			if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
				return ProviderUsage{}, fmt.Errorf("codex initialize failed: %s", envelope.Error)
			}
			initialized = true
			if err := write(map[string]any{"method": "initialized"}); err != nil {
				return ProviderUsage{}, err
			}
			if err := write(map[string]any{"id": 2, "method": "account/rateLimits/read", "params": nil}); err != nil {
				return ProviderUsage{}, err
			}
			continue
		}
		if envelope.ID != 2 {
			continue
		}
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			return ProviderUsage{}, fmt.Errorf("codex account quota request failed: %s", envelope.Error)
		}
		return parseCodexUsage(envelope.Result)
	}
	if err := scanner.Err(); err != nil {
		return ProviderUsage{}, err
	}
	if probeCtx.Err() != nil {
		return ProviderUsage{}, probeCtx.Err()
	}
	return ProviderUsage{}, fmt.Errorf("codex account quota response missing")
}

func parseCodexUsage(raw []byte) (ProviderUsage, error) {
	var result struct {
		RateLimits          *codexRateLimitSnapshot           `json:"rateLimits"`
		RateLimitsByLimitID map[string]codexRateLimitSnapshot `json:"rateLimitsByLimitId"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ProviderUsage{}, err
	}
	limits := make([]codexRateLimitSnapshot, 0, len(result.RateLimitsByLimitID)+1)
	seen := map[string]bool{}
	for _, limit := range result.RateLimitsByLimitID {
		limits = append(limits, limit)
		seen[limit.LimitID] = true
	}
	if result.RateLimits != nil && !seen[result.RateLimits.LimitID] {
		limits = append(limits, *result.RateLimits)
	}
	if len(limits) == 0 {
		return ProviderUsage{}, errors.New("codex returned no rate limits")
	}

	usage := ProviderUsage{Status: "available", Source: "official", ObservedAt: time.Now().UTC()}
	for _, limit := range limits {
		if usage.AccountScope == "" && limit.PlanType != nil {
			usage.AccountScope = strings.TrimSpace(*limit.PlanType)
		}
		group := "Codex"
		if limit.LimitName != nil && strings.TrimSpace(*limit.LimitName) != "" {
			group = strings.TrimSpace(*limit.LimitName)
		}
		for index, window := range []*codexRateLimitWindow{limit.Primary, limit.Secondary} {
			if window == nil {
				continue
			}
			used := clampPercent(window.UsedPercent)
			remaining := clampPercent(100 - used)
			label := "Limit"
			if window.WindowDurationMins != nil {
				switch *window.WindowDurationMins {
				case 300:
					label = "5 hour limit"
				case 10080:
					label = "Weekly limit"
				default:
					label = fmt.Sprintf("%d minute limit", *window.WindowDurationMins)
				}
			}
			var reset *time.Time
			if window.ResetsAt != nil {
				t := time.Unix(*window.ResetsAt, 0).UTC()
				reset = &t
			}
			usage.Windows = append(usage.Windows, ProviderUsageWindow{
				ID:                 fmt.Sprintf("%s-%d", limit.LimitID, index),
				Group:              group,
				Label:              label,
				UsedPercent:        &used,
				RemainingPercent:   &remaining,
				WindowDurationMins: window.WindowDurationMins,
				ResetsAt:           reset,
				Unit:               "percent",
			})
		}
	}
	if len(usage.Windows) == 0 {
		usage.Status = "partial"
		usage.Message = "Codex returned an account record without quota windows."
	}
	return usage, nil
}

func probeAntigravityUsage(ctx context.Context, command Command) (ProviderUsage, error) {
	if command.Path == "" {
		command.Path = "agy"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := command.exec(probeCtx, "-p", "/usage", "--output-format", "json")
	stderr := newStderrTail(io.Discard, 2048)
	cmd.Stderr = stderr
	raw, err := cmd.Output()
	if err != nil {
		return ProviderUsage{}, fmt.Errorf("antigravity usage failed: %w: %s", err, stderr.Tail())
	}
	return parseAntigravityUsage(raw)
}

func parseAntigravityUsage(raw []byte) (ProviderUsage, error) {
	var response struct {
		Status  string `json:"status"`
		Command struct {
			Name string `json:"name"`
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						ID                string   `json:"id"`
						Name              string   `json:"name"`
						Window            string   `json:"window"`
						RemainingFraction *float64 `json:"remaining_fraction"`
						ResetTime         string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return ProviderUsage{}, err
	}
	if !strings.EqualFold(response.Status, "SUCCESS") || response.Command.Name != "usage" {
		return ProviderUsage{}, errors.New("antigravity usage command failed")
	}
	usage := ProviderUsage{Status: "available", Source: "official", ObservedAt: time.Now().UTC()}
	for _, group := range response.Command.Data.Groups {
		for _, bucket := range group.Buckets {
			window := ProviderUsageWindow{
				ID: bucket.ID, Group: group.Name, Label: bucket.Name, Unit: "percent",
			}
			if bucket.RemainingFraction != nil {
				remaining := clampPercent(*bucket.RemainingFraction * 100)
				used := clampPercent(100 - remaining)
				window.RemainingPercent = &remaining
				window.UsedPercent = &used
			}
			switch strings.ToLower(bucket.Window) {
			case "5h":
				mins := int64(300)
				window.WindowDurationMins = &mins
			case "weekly":
				mins := int64(10080)
				window.WindowDurationMins = &mins
			}
			if bucket.ResetTime != "" {
				if parsed, err := time.Parse(time.RFC3339, bucket.ResetTime); err == nil {
					parsed = parsed.UTC()
					window.ResetsAt = &parsed
				}
			}
			usage.Windows = append(usage.Windows, window)
		}
	}
	if len(usage.Windows) == 0 {
		usage.Status = "partial"
		usage.Message = "Antigravity returned no quota buckets."
	}
	return usage, nil
}
