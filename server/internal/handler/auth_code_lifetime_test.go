package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestVerificationCodeFifteenMinuteLifetimeAndReuse(t *testing.T) {
	// Use the fixture's existing user so this test creates no account/workspace.
	email := handlerTestEmail
	dbfx.Cleanup(t, `DELETE FROM verification_code WHERE email = $1`, email)
	req := testutil.JSONRequest(http.MethodPost, "/auth/send-code", map[string]string{"email": email})
	testutil.Call(t, testHandler.SendCode, req).Want(http.StatusOK)
	code, err := testHandler.Queries.GetLatestVerificationCode(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	remaining := time.Until(code.ExpiresAt.Time)
	if remaining < 14*time.Minute+55*time.Second || remaining > 15*time.Minute {
		t.Fatalf("issued lifetime=%v, want 15 minutes", remaining)
	}
	verify := func(status int) {
		t.Helper()
		req := testutil.JSONRequest(http.MethodPost, "/auth/verify-code", map[string]string{"email": email, "code": code.Code})
		testutil.Call(t, testHandler.VerifyCode, req).Want(status)
	}
	// Simulate 11 minutes elapsed: a code remains valid past the old 10 minute window.
	_, err = testPool.Exec(context.Background(), `UPDATE verification_code SET created_at=now()-interval '11 minutes', expires_at=now()+interval '4 minutes' WHERE id=$1`, code.ID)
	if err != nil {
		t.Fatal(err)
	}
	verify(http.StatusOK)
	verify(http.StatusBadRequest)
	// A fresh unused code whose 15 minute window has elapsed is rejected.
	_, err = testPool.Exec(context.Background(), `UPDATE verification_code SET used=false, created_at=now()-interval '16 minutes', expires_at=now()-interval '1 minute' WHERE id=$1`, code.ID)
	if err != nil {
		t.Fatal(err)
	}
	verify(http.StatusBadRequest)
}
