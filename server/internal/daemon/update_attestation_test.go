package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateAttestationHealthCountsAcknowledgedHeartbeats(t *testing.T) {
	d, _ := pendingWorkHintDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"runtime_id":"rt-1","status":"ok"}`))
	})
	d.wsHBLastAck = make(map[string]time.Time)
	d.workspaces = map[string]*workspaceState{}
	d.updateAttestation.binarySHA256 = strings.Repeat("a", 64)
	d.runHeartbeatTick(context.Background(), "rt-1")
	d.recordWSHeartbeatAck("rt-1")
	d.runHeartbeatTick(context.Background(), "rt-1") // WS freshness skips HTTP
	start := time.Now().UTC().Truncate(time.Second)
	r := httptest.NewRecorder()
	d.healthHandler(start).ServeHTTP(r, httptest.NewRequest("GET", "/health", nil))
	var health HealthResponse
	if err := json.Unmarshal(r.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.UpdateAttestation.HeartbeatAcks != 2 || health.UpdateAttestation.BinarySHA256 != strings.Repeat("a", 64) || !health.UpdateAttestation.ProcessStartedAt.Equal(start) {
		t.Fatalf("unexpected attestation: %+v", health.UpdateAttestation)
	}
	if health.UpdateAttestation.RuntimeHeartbeatAcks["rt-1"] != 2 {
		t.Fatalf("per-runtime counter = %+v", health.UpdateAttestation.RuntimeHeartbeatAcks)
	}
}

func TestMaintenanceShutdownChecksIdentityAndDrain(t *testing.T) {
	if !maintenanceShutdownSupported {
		t.Skip("unsupported platform")
	}
	d, gate := gateTestDaemon(t)
	d.cfg.LaunchedBy = "desktop"
	d.cfg.DaemonID = "daemon-1"
	if err := os.MkdirAll(gate, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gate, "owner"), []byte(`{"id":"token-1","pid":123}`), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(gate)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{}, 1)
	d.cancelFunc = func() { cancelled <- struct{}{} }
	request := map[string]any{"expected_pid": os.Getpid(), "process_started_at": d.updateAttestation.startedAt, "daemon_id": "daemon-1", "profile": "test", "gate_identity": maintenanceGateIdentity(info), "gate_token": "token-1"}
	call := func() int {
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/shutdown/maintenance", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.maintenanceShutdownHandler()(w, r)
		return w.Code
	}
	request["gate_token"] = "wrong"
	if got := call(); got != http.StatusConflict {
		t.Fatalf("wrong owner: %d", got)
	}
	request["gate_token"] = "token-1"
	d.activeTasks.Add(1)
	if got := call(); got != http.StatusConflict {
		t.Fatalf("busy: %d", got)
	}
	d.activeTasks.Add(-1)
	request["expected_pid"] = os.Getpid() + 1
	if got := call(); got != http.StatusConflict {
		t.Fatalf("wrong pid: %d", got)
	}
	request["expected_pid"] = os.Getpid()
	if got := call(); got != http.StatusAccepted {
		t.Fatalf("valid shutdown: %d", got)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown not triggered")
	}
	if d.tryEnterClaim() {
		t.Fatal("shutdown reopened claims")
	}
}

func TestUpdateAttestationReceiptRequiresSuccessfulCompletion(t *testing.T) {
	d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	d.terminalReportSend = func(context.Context, terminalTaskReport, []time.Duration) error { return nil }
	if !d.reportTaskResult(context.Background(), "task", "", time.Time{}, TaskResult{Status: "completed"}, d.logger) {
		t.Fatal("successful report not accepted")
	}
	if d.reportTaskResult(context.Background(), "task", "", time.Time{}, TaskResult{Status: "failed"}, d.logger) {
		t.Fatal("failure treated as completion")
	}
	start := time.Now().UTC()
	d.updateAttestation.recordReceipt("task", "runtime", start)
	receipts := d.updateAttestation.snapshot(start).TaskReceipts
	if len(receipts) != 1 || receipts[0].TaskID != "task" || receipts[0].RuntimeID != "runtime" || receipts[0].AckAt.Before(start) {
		t.Fatalf("unexpected receipts: %+v", receipts)
	}
}
