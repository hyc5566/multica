package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// This exercises real PAT auth, handlers and PostgreSQL through an in-process
// router. Redis caches are disabled; desktop storage and cross-node cache
// invalidation are outside this backend regression's scope.
func TestDesktopMachinePATIsolation(t *testing.T) {
	router := chi.NewRouter()
	router.Group(func(r chi.Router) {
		r.Use(middleware.Auth(testHandler.Queries, nil, nil))
		r.Post("/api/tokens", testHandler.CreatePersonalAccessToken)
		r.Post("/api/tokens/current/renew", testHandler.RenewCurrentPersonalAccessToken)
		r.Delete("/api/tokens/{id}", testHandler.RevokePersonalAccessToken)
	})
	router.Group(func(r chi.Router) {
		r.Use(middleware.DaemonAuth(testHandler.Queries, nil, nil, nil))
		r.Post("/api/daemon/register", testHandler.DaemonRegister)
		r.Post("/api/daemon/heartbeat", testHandler.DaemonHeartbeat)
	})
	call := func(method, path, token string, body any) *testutil.Response {
		t.Helper()
		req := testutil.JSONRequest(method, path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		return testutil.Call(t, router.ServeHTTP, req)
	}

	bootstrap, _ := insertTestPAT(t, time.Now().Add(24*time.Hour))
	create := func() CreatePATResponse {
		t.Helper()
		var pat CreatePATResponse
		call(http.MethodPost, "/api/tokens", bootstrap, map[string]any{
			"name": "Multica Desktop", "expires_in_days": 3,
		}).Want(http.StatusCreated).JSON(&pat)
		dbfx.Cleanup(t, `DELETE FROM personal_access_token WHERE id = $1`, pat.ID)
		return pat
	}
	a, b := create(), create()
	if a.ID == b.ID || a.Token == b.Token || a.Name != b.Name {
		t.Fatal("same-name desktop PATs must have distinct IDs and secrets")
	}
	var bExpiry time.Time
	dbfx.QueryRow(t, `SELECT expires_at FROM personal_access_token WHERE id = $1`, b.ID).Scan(&bExpiry)

	register := func(token, daemonID string) string {
		t.Helper()
		dbfx.Cleanup(t, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, daemonID)
		var resp struct {
			Runtimes []AgentRuntimeResponse `json:"runtimes"`
		}
		call(http.MethodPost, "/api/daemon/register", token, map[string]any{
			"workspace_id": testWorkspaceID, "daemon_id": daemonID,
			"device_name": "Same Desktop Name", "launched_by": "desktop",
			"runtimes": []map[string]string{{"name": "codex", "type": "codex", "status": "online"}},
		}).Want(http.StatusOK).JSON(&resp)
		if len(resp.Runtimes) != 1 {
			t.Fatalf("expected one runtime, got %d", len(resp.Runtimes))
		}
		return resp.Runtimes[0].ID
	}
	daemonA, daemonB := uuid.NewString(), uuid.NewString()
	runtimeA, runtimeB := register(a.Token, daemonA), register(b.Token, daemonB)
	if runtimeA == runtimeB {
		t.Fatal("distinct daemon IDs must not merge same-name desktop runtimes")
	}
	heartbeat := func(token, runtimeID string, status int) {
		t.Helper()
		call(http.MethodPost, "/api/daemon/heartbeat", token, map[string]string{
			"runtime_id": runtimeID,
		}).Want(status)
	}
	heartbeat(a.Token, runtimeA, http.StatusOK)
	heartbeat(b.Token, runtimeB, http.StatusOK)
	var renewed RenewPATResponse
	call(http.MethodPost, "/api/tokens/current/renew", a.Token, nil).Want(http.StatusOK).JSON(&renewed)
	if !renewed.Renewed {
		t.Fatal("A should renew inside the renewal window")
	}
	heartbeat(a.Token, runtimeA, http.StatusOK)
	heartbeat(b.Token, runtimeB, http.StatusOK)
	call(http.MethodDelete, "/api/tokens/"+a.ID, a.Token, nil).Want(http.StatusNoContent)
	heartbeat(a.Token, runtimeA, http.StatusUnauthorized)
	heartbeat(b.Token, runtimeB, http.StatusOK)
	if got := register(b.Token, daemonB); got != runtimeB {
		t.Fatalf("B re-registration changed runtime: got %s, want %s", got, runtimeB)
	}

	var actualBExpiry time.Time
	var bRevoked bool
	dbfx.QueryRow(t, `SELECT expires_at, revoked FROM personal_access_token WHERE id = $1`, b.ID).Scan(&actualBExpiry, &bRevoked)
	if bRevoked || !actualBExpiry.Equal(bExpiry) {
		t.Fatal("renewing and revoking A must not change B's expiry or revocation state")
	}
	for runtimeID, daemonID := range map[string]string{runtimeA: daemonA, runtimeB: daemonB} {
		var actualDaemonID, ownerID, status string
		dbfx.QueryRow(t, `SELECT daemon_id, owner_id, status FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&actualDaemonID, &ownerID, &status)
		if actualDaemonID != daemonID || ownerID != testUserID || status != "online" {
			t.Fatalf("runtime %s identity/status changed: daemon=%s owner=%s status=%s", runtimeID, actualDaemonID, ownerID, status)
		}
	}
}
