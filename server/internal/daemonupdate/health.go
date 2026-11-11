package daemonupdate

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"
)

type healthWorkspace struct {
	ID       string   `json:"id"`
	Runtimes []string `json:"runtimes"`
}
type healthAttestation struct {
	BinarySHA256         string            `json:"binary_sha256"`
	ProcessStartedAt     string            `json:"process_started_at"`
	RuntimeHeartbeatAcks map[string]uint64 `json:"runtime_heartbeat_acks"`
	TaskReceipts         []struct {
		TaskID      string    `json:"task_id"`
		RuntimeID   string    `json:"runtime_id"`
		CompletedAt time.Time `json:"completed_at"`
		AckAt       time.Time `json:"ack_at"`
	} `json:"task_receipts"`
}
type health struct {
	Status              string            `json:"status"`
	PID                 int               `json:"pid"`
	OS                  string            `json:"os"`
	Profile             string            `json:"profile"`
	DaemonID            string            `json:"daemon_id"`
	ServerURL           string            `json:"server_url"`
	LaunchedBy          string            `json:"launched_by"`
	LifecycleManager    string            `json:"lifecycle_manager"`
	Version             string            `json:"cli_version"`
	Commit              string            `json:"cli_commit"`
	Workspaces          []healthWorkspace `json:"workspaces"`
	Attestation         healthAttestation `json:"update_attestation"`
	MaintenanceShutdown bool              `json:"maintenance_shutdown"`
	MaintenanceGate     struct {
		Supported      bool   `json:"supported"`
		Blocked        bool   `json:"blocked"`
		Ready          bool   `json:"ready"`
		Path           string `json:"path"`
		ClaimsInFlight int    `json:"claims_in_flight"`
	} `json:"maintenance_gate"`
	ActiveTasks int `json:"active_task_count"`
}

func workspaceSet(workspaces []healthWorkspace) (map[string][]string, error) {
	result := make(map[string][]string, len(workspaces))
	for _, workspace := range workspaces {
		if workspace.ID == "" {
			return nil, errors.New("empty workspace")
		}
		if _, exists := result[workspace.ID]; exists {
			return nil, errors.New("duplicate workspace")
		}
		runtimes := append([]string{}, workspace.Runtimes...)
		sort.Strings(runtimes)
		for i, runtimeID := range runtimes {
			if runtimeID == "" || (i > 0 && runtimeID == runtimes[i-1]) {
				return nil, errors.New("invalid runtime set")
			}
		}
		result[workspace.ID] = runtimes
	}
	return result, nil
}

func validateNewHealth(j *Journal, h health, pid int, start string) error {
	if h.Status != "running" || h.OS != "darwin" || h.LaunchedBy != "desktop" || h.LifecycleManager != "" || h.Profile != j.Profile ||
		h.DaemonID != j.DaemonID || h.PID != pid || pid <= 0 || start == "" || h.Attestation.ProcessStartedAt != start ||
		h.ServerURL == "" || j.ServerURLSHA256 == "" || fmt.Sprintf("%x", sha256.Sum256([]byte(h.ServerURL))) != j.ServerURLSHA256 ||
		h.Version != j.NewVersion || h.Commit != j.NewCommit || h.Attestation.BinarySHA256 != j.NewBinarySHA256 {
		return errors.New("new daemon identity mismatch")
	}
	actual, err := workspaceSet(h.Workspaces)
	if err != nil {
		return err
	}
	expected := make(map[string][]string, len(j.Workspaces))
	for id, runtimes := range j.Workspaces {
		expected[id] = append([]string{}, runtimes...)
		sort.Strings(expected[id])
	}
	if !reflect.DeepEqual(actual, expected) || len(j.RuntimeIDs) == 0 {
		return errors.New("workspace or runtime identity changed")
	}
	runtimeSet := make(map[string]bool)
	for _, runtimes := range actual {
		for _, id := range runtimes {
			runtimeSet[id] = true
		}
	}
	if len(runtimeSet) != len(j.RuntimeIDs) {
		return errors.New("incomplete runtime baseline")
	}
	for _, id := range j.RuntimeIDs {
		if !runtimeSet[id] {
			return errors.New("runtime baseline mismatch")
		}
		delete(runtimeSet, id)
		if _, ok := h.Attestation.RuntimeHeartbeatAcks[id]; !ok {
			return fmt.Errorf("runtime heartbeat unavailable: %s", id)
		}
	}
	return nil
}

func validateHeldGate(j *Journal, h health) error {
	if !h.MaintenanceShutdown || !h.MaintenanceGate.Supported || !h.MaintenanceGate.Blocked || !h.MaintenanceGate.Ready ||
		h.MaintenanceGate.Path != j.GatePath || h.MaintenanceGate.ClaimsInFlight != 0 || h.ActiveTasks != 0 {
		return errors.New("daemon has not confirmed the held idle gate")
	}
	return nil
}

// heartbeatObserver counts only distinct observations in which each runtime
// progressed. A jump of any size between two polls is one observation.
type heartbeatObserver struct {
	last     map[string]uint64
	observed map[string]int
}

func newHeartbeatObserver(runtimeIDs []string, baseline map[string]uint64) (*heartbeatObserver, error) {
	o := &heartbeatObserver{last: make(map[string]uint64), observed: make(map[string]int)}
	for _, id := range runtimeIDs {
		count, ok := baseline[id]
		if !ok || id == "" {
			return nil, errors.New("missing heartbeat baseline")
		}
		if _, exists := o.last[id]; exists {
			return nil, errors.New("duplicate runtime")
		}
		o.last[id] = count
	}
	if len(o.last) == 0 {
		return nil, errors.New("empty runtime set")
	}
	return o, nil
}
func (o *heartbeatObserver) observe(counts map[string]uint64) (bool, error) {
	complete := true
	for id, last := range o.last {
		count, ok := counts[id]
		if !ok || count < last {
			return false, errors.New("heartbeat counter regressed or disappeared")
		}
		if count > last {
			o.last[id] = count
			o.observed[id]++
		}
		if o.observed[id] < 3 {
			complete = false
		}
	}
	return complete, nil
}
