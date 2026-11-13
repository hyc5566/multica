package daemon

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type updateTaskReceipt struct {
	TaskID      string    `json:"task_id"`
	RuntimeID   string    `json:"runtime_id"`
	CompletedAt time.Time `json:"completed_at"`
	AckAt       time.Time `json:"ack_at"`
}

type updateAttestationState struct {
	binarySHA256         string
	startedAt            time.Time
	heartbeatAcks        atomic.Uint64
	mu                   sync.Mutex
	receipts             []updateTaskReceipt
	runtimeHeartbeatAcks map[string]uint64
}

type updateAttestationSnapshot struct {
	BinarySHA256         string              `json:"binary_sha256"`
	ProcessStartedAt     time.Time           `json:"process_started_at"`
	HeartbeatAcks        uint64              `json:"heartbeat_acks"`
	RuntimeHeartbeatAcks map[string]uint64   `json:"runtime_heartbeat_acks"`
	TaskReceipts         []updateTaskReceipt `json:"task_receipts"`
}

func (s *updateAttestationState) recordHeartbeat(runtimeID string) {
	if runtimeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimeHeartbeatAcks == nil {
		s.runtimeHeartbeatAcks = make(map[string]uint64)
	}
	s.runtimeHeartbeatAcks[runtimeID]++
	s.heartbeatAcks.Add(1)
}

func runningBinarySHA256() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "linux" {
		path = "/proc/self/exe"
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return ""
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return ""
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return ""
	}
	pathInfo, err := os.Stat(path)
	if err != nil || !os.SameFile(before, pathInfo) || before.Size() != pathInfo.Size() || !before.ModTime().Equal(pathInfo.ModTime()) {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (s *updateAttestationState) recordReceipt(taskID, runtimeID string, completed time.Time) {
	if taskID == "" || runtimeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, receipt := range s.receipts {
		if receipt.TaskID == taskID {
			return
		}
	}
	s.receipts = append(s.receipts, updateTaskReceipt{taskID, runtimeID, completed, time.Now().UTC()})
	if len(s.receipts) > 32 {
		s.receipts = append([]updateTaskReceipt(nil), s.receipts[len(s.receipts)-32:]...)
	}
}

func (s *updateAttestationState) snapshot(startedAt time.Time) updateAttestationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipts := append([]updateTaskReceipt{}, s.receipts...)
	counts := make(map[string]uint64, len(s.runtimeHeartbeatAcks))
	for runtimeID, count := range s.runtimeHeartbeatAcks {
		counts[runtimeID] = count
	}
	if !s.startedAt.IsZero() {
		startedAt = s.startedAt
	}
	return updateAttestationSnapshot{s.binarySHA256, startedAt.UTC(), s.heartbeatAcks.Load(), counts, receipts}
}
