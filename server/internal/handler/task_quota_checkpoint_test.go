package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAnnotateTaskQuotaWindowsUsesOnlyStableModelIdentity(t *testing.T) {
	snapshot := ProviderUsageSnapshot{Windows: []ProviderUsageWindow{
		{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro"},
		{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
	}}
	annotateTaskQuotaWindows(&snapshot, "antigravity", "gemini-2.5-pro")
	if snapshot.Windows[0].Scope != "model" || snapshot.Windows[0].ModelMatch != "exact" {
		t.Fatalf("exact model annotation = %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].ModelMatch != "unknown" {
		t.Fatalf("non-matching model must remain unknown: %+v", snapshot.Windows[1])
	}
}

func TestSelectAntigravityTaskQuota(t *testing.T) {
	for _, tc := range []struct{ model, want, match string }{
		{"gemini-3.6-flash-high", "gemini-3.6-flash-high", "exact"},
		{"gemini-3.8-flash-high", "gemini-3.8-flash-tiered", "shared"},
		{"gemini-unknown", "", ""},
		{"", "", ""},
	} {
		t.Run(tc.model, func(t *testing.T) {
			snapshot := ProviderUsageSnapshot{Windows: []ProviderUsageWindow{
				{ID: "gemini-3.6-flash-high", Group: "Gemini 3.6 Flash (High)"},
				{ID: "gemini-3.6-flash-tiered"},
				{ID: "gemini-3.8-flash-tiered"},
				{ID: "claude-opus-4-6-thinking"},
			}}
			annotateTaskQuotaWindows(&snapshot, "antigravity", tc.model)
			selectTaskQuotaWindows(&snapshot, "antigravity", tc.model)
			if tc.want == "" {
				if len(snapshot.Windows) != 0 {
					t.Fatal(snapshot.Windows)
				}
				return
			}
			if len(snapshot.Windows) != 1 || snapshot.Windows[0].ID != tc.want || snapshot.Windows[0].ModelMatch != tc.match || snapshot.Windows[0].Label == "" {
				t.Fatal(snapshot.Windows)
			}
		})
	}
}

func TestAnnotateTaskQuotaWindowsMarksSharedAccountWindows(t *testing.T) {
	snapshot := ProviderUsageSnapshot{Windows: []ProviderUsageWindow{{ID: "five_hour", Label: "5 hour"}}}
	annotateTaskQuotaWindows(&snapshot, "claude", "claude-opus")
	if snapshot.Windows[0].Scope != "account" || snapshot.Windows[0].ModelMatch != "shared" {
		t.Fatalf("Claude annotation = %+v", snapshot.Windows[0])
	}
}

func TestSelectAntigravityTaskQuotaProviderPools(t *testing.T) {
	for _, tc := range []struct{ model, pool string }{
		{"gemini-3.8-flash-high", "gemini"},
		{"claude-opus-4-6-thinking", "3p"},
		{"gpt-oss-120b-medium", "3p"},
		{"custom-model", ""},
		{"default", ""},
		{"", ""},
	} {
		t.Run(tc.model, func(t *testing.T) {
			snapshot := ProviderUsageSnapshot{Windows: []ProviderUsageWindow{
				{ID: "gemini-5h", Label: "5h"},
				{ID: "gemini-weekly", Label: "weekly"},
				{ID: "3p-5h", Label: "5h"},
				{ID: "3p-weekly", Label: "weekly"},
			}}
			annotateTaskQuotaWindows(&snapshot, "antigravity", tc.model)
			for _, window := range snapshot.Windows {
				wantMatch := "unknown"
				if tc.pool != "" && (window.ID == tc.pool+"-5h" || window.ID == tc.pool+"-weekly") {
					wantMatch = "shared"
				}
				if window.Scope != "provider" || window.ModelMatch != wantMatch {
					t.Fatalf("pool annotation = %+v, want provider/%s", window, wantMatch)
				}
			}
			selectTaskQuotaWindows(&snapshot, "antigravity", tc.model)
			if tc.pool == "" {
				if len(snapshot.Windows) != 0 {
					t.Fatalf("unknown model must not select a pool: %+v", snapshot.Windows)
				}
				return
			}
			if len(snapshot.Windows) != 2 || snapshot.Windows[0].ID != tc.pool+"-5h" || snapshot.Windows[1].ID != tc.pool+"-weekly" {
				t.Fatalf("expected both %s windows: %+v", tc.pool, snapshot.Windows)
			}
			for _, window := range snapshot.Windows {
				if window.Scope != "provider" || window.ModelMatch != "shared" || window.Label == "" {
					t.Fatalf("selected pool metadata = %+v", window)
				}
			}
		})
	}
}

func TestAnnotateTaskQuotaWindowsDoesNotGuessCodexModel(t *testing.T) {
	snapshot := ProviderUsageSnapshot{Windows: []ProviderUsageWindow{
		{ID: "codex-primary", Group: "Codex", Label: "5 hour"},
		{ID: "model-0-primary", Group: "Spark", Label: "5 hour"},
	}}
	annotateTaskQuotaWindows(&snapshot, "codex", "gpt-5.3-codex-spark")
	if snapshot.Windows[0].Scope != "provider" || snapshot.Windows[0].ModelMatch != "shared" {
		t.Fatalf("base Codex annotation = %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].Scope != "model" || snapshot.Windows[1].ModelMatch != "unknown" {
		t.Fatalf("named bucket must not be guessed exact: %+v", snapshot.Windows[1])
	}
}

func TestChatQuotaCheckpointHydration(t *testing.T) {
	if testHandler == nil {
		t.Skip("requires test database")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "ChatQuotaHydrationAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	runtimeID := handlerTestRuntimeID(t)
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
		VALUES ($1, $2, 'completed', 0, $3)
		RETURNING id
	`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("insert chat task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, task_id)
		VALUES ($1, 'assistant', 'quota reply', $2)
	`, sessionID, taskID); err != nil {
		t.Fatalf("insert chat message fixture: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_quota_checkpoint (
			task_id, workspace_id, runtime_id, phase, boundary_at, provider,
			capture_state, error_code, overlapping_task_count
		) VALUES
			($1, $2, $3, 'before', now() - interval '1 minute', 'codex', 'timeout', 'timeout', 0),
			($1, $2, $3, 'after', now(), 'codex', 'no_snapshot', NULL, 1)
	`, taskID, testWorkspaceID, runtimeID); err != nil {
		t.Fatalf("insert quota checkpoint fixtures: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM task_usage WHERE task_id = $1`, taskID)
		testPool.Exec(ctx, `DELETE FROM task_quota_checkpoint WHERE task_id = $1`, taskID)
		testPool.Exec(ctx, `DELETE FROM chat_message WHERE task_id = $1`, taskID)
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	if err := testHandler.Queries.UpsertTaskUsage(ctx, db.UpsertTaskUsageParams{
		TaskID: parseUUID(taskID), Provider: "codex", Model: "gpt-6-astra",
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5,
	}); err != nil {
		t.Fatal(err)
	}

	pageReq := withURLParam(newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID+"/messages/page", nil), "sessionId", sessionID)
	pageReq = withChatTestWorkspaceCtx(t, pageReq)
	pageW := httptest.NewRecorder()
	testHandler.ListChatMessagesPage(pageW, pageReq)
	if pageW.Code != http.StatusOK {
		t.Fatalf("ListChatMessagesPage status = %d: %s", pageW.Code, pageW.Body.String())
	}
	var page ChatMessagesPageResponse
	if err := json.Unmarshal(pageW.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode message page: %v", err)
	}
	if len(page.Messages) != 1 || len(page.Messages[0].QuotaCheckpoints) != 2 {
		t.Fatalf("message checkpoints = %+v, want two", page.Messages)
	}
	if usage := page.Messages[0].Usage; len(usage) != 1 || usage[0].InputTokens != 100 || usage[0].OutputTokens != 20 || usage[0].CacheReadTokens != 30 || usage[0].CacheWriteTokens != 5 {
		t.Fatalf("message token usage = %+v", usage)
	}
	if page.Messages[0].QuotaCheckpoints[0].TaskID != taskID || page.Messages[0].QuotaCheckpoints[1].OverlappingTaskCount != 1 {
		t.Fatalf("message checkpoint metadata = %+v", page.Messages[0].QuotaCheckpoints)
	}

	sessionReq := withURLParam(newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID, nil), "sessionId", sessionID)
	sessionReq = withChatTestWorkspaceCtx(t, sessionReq)
	sessionW := httptest.NewRecorder()
	testHandler.GetChatSession(sessionW, sessionReq)
	if sessionW.Code != http.StatusOK {
		t.Fatalf("GetChatSession status = %d: %s", sessionW.Code, sessionW.Body.String())
	}
	var session ChatSessionResponse
	if err := json.Unmarshal(sessionW.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if len(session.QuotaCheckpoints) != 2 || session.QuotaCheckpoints[0].TaskID != taskID {
		t.Fatalf("session checkpoints = %+v, want task %s", session.QuotaCheckpoints, taskID)
	}
	if len(session.TaskUsage) != 1 || session.TaskUsage[0].TaskID != taskID || len(session.TaskUsage[0].Usage) != 1 || session.TaskUsage[0].Usage[0].InputTokens != 100 {
		t.Fatalf("session usage = %+v", session.TaskUsage)
	}
}
