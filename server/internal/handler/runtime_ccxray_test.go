package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func validCCXRaySummary(now time.Time) map[string]any {
	return map[string]any{
		"enabled":         true,
		"installed":       true,
		"status":          "observing",
		"version":         "0.1.0",
		"observed_at":     now.UTC().Format(time.RFC3339),
		"last_error_code": "",
	}
}

func TestValidateCCXRayHealthSummaryBounds(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(map[string]any)
	}{
		{"unknown field", func(v map[string]any) { v["raw_prompt"] = "must-not-cross-boundary" }},
		{"invalid status", func(v map[string]any) { v["status"] = "capturing" }},
		{"oversized version", func(v map[string]any) { v["version"] = strings.Repeat("v", 65) }},
		{"oversized payload", func(v map[string]any) { v["last_error_code"] = strings.Repeat("x", 1100) }},
		{"stale observation", func(v map[string]any) { v["observed_at"] = now.Add(-25 * time.Hour).Format(time.RFC3339) }},
		{"future observation", func(v map[string]any) { v["observed_at"] = now.Add(2 * time.Minute).Format(time.RFC3339) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := validCCXRaySummary(now)
			tc.edit(value)
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateCCXRayHealthSummary(raw, now); err == nil {
				t.Fatal("invalid summary was accepted")
			}
		})
	}
}

func TestDaemonHeartbeatCCXRaySummaryIsFailOpenAndLegacyCompatible(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "CCXRay heartbeat runtime", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"provider":     "codex",
	})
	now := time.Now().UTC().Truncate(time.Second)

	beat := func(summary any) {
		body := map[string]any{"runtime_id": runtimeID}
		if summary != nil {
			body["ccxray"] = summary
		}
		req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/heartbeat", body, testWorkspaceID, "ccxray-daemon")
		testutil.Call(t, testHandler.DaemonHeartbeat, req).Want(http.StatusOK)
	}

	beat(validCCXRaySummary(now))
	stored := readRuntimeCCXRayMetadata(t, runtimeID)
	if stored["status"] != "observing" || stored["version"] != "0.1.0" {
		t.Fatalf("stored ccxray summary = %#v", stored)
	}
	if len(stored) != 6 {
		t.Fatalf("stored ccxray allowlist has %d fields, want 6: %#v", len(stored), stored)
	}

	// An old daemon omits the additive field. Its heartbeat still succeeds and
	// leaves the last bounded snapshot available to age out in the UI.
	beat(nil)
	if got := readRuntimeCCXRayMetadata(t, runtimeID); got["observed_at"] != stored["observed_at"] {
		t.Fatalf("legacy heartbeat changed ccxray metadata: before=%#v after=%#v", stored, got)
	}

	// Optional observation failures never fail liveness and never replace the
	// last valid snapshot with unsafe or stale input.
	invalid := validCCXRaySummary(now)
	invalid["raw_response"] = strings.Repeat("secret", 200)
	beat(invalid)
	if got := readRuntimeCCXRayMetadata(t, runtimeID); got["observed_at"] != stored["observed_at"] {
		t.Fatalf("invalid summary replaced valid metadata: before=%#v after=%#v", stored, got)
	}
}

func readRuntimeCCXRayMetadata(t *testing.T, runtimeID string) map[string]any {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->'ccxray' FROM agent_runtime WHERE id = $1`, runtimeID,
	).Scan(&raw); err != nil {
		t.Fatalf("read ccxray metadata: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode ccxray metadata: %v", err)
	}
	return value
}

func TestCCXRayMetadataUsesExistingRuntimeReadPermission(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID, _, plainMemberID := runtimeVisibilityFixture(t)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE agent_runtime SET metadata = jsonb_build_object('ccxray', jsonb_build_object('last_error_code', 'private-marker')) WHERE id = $1`,
		runtimeID,
	); err != nil {
		t.Fatalf("seed private ccxray metadata: %v", err)
	}

	w := testutil.Call(t, testHandler.ListAgentRuntimes,
		newRequestAs(plainMemberID, http.MethodGet, "/api/runtimes", nil),
	).Want(http.StatusOK)
	if strings.Contains(w.Body.String(), "private-marker") {
		t.Fatalf("private runtime ccxray metadata leaked to non-owner: %s", w.Body.String())
	}
}
