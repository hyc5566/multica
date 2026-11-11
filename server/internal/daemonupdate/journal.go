package daemonupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Journal is the durable authority for one coordinated App update. Paths and
// identities are populated only after the Desktop has verified the candidate.
type Journal struct {
	Legacy                       *LegacyState           `json:"legacy,omitempty"`
	Version                      int                    `json:"journal_version"`
	Token                        string                 `json:"token"`
	Phase                        string                 `json:"phase"`
	Profile                      string                 `json:"profile"`
	CLIEntry                     string                 `json:"cli_entry"`
	DaemonID                     string                 `json:"daemon_id"`
	ServerURLSHA256              string                 `json:"server_url_sha256"`
	DaemonPID                    int                    `json:"daemon_pid"`
	StopRequested                bool                   `json:"stop_requested"`
	DaemonStartedAt              string                 `json:"daemon_started_at"`
	NewDaemonPID                 int                    `json:"new_daemon_pid"`
	NewDaemonStartedAt           string                 `json:"new_daemon_started_at"`
	CandidateDaemonIdentity      *LegacyProcessIdentity `json:"candidate_daemon_identity,omitempty"`
	NewAppPID                    int                    `json:"new_app_pid"`
	NewAppExecutable             string                 `json:"new_app_executable"`
	ReadyFile                    string                 `json:"ready_file"`
	HandoffFile                  string                 `json:"handoff_file"`
	StartRequested               bool                   `json:"start_requested"`
	AppStartRequested            bool                   `json:"app_start_requested"`
	GateReleaseRequested         bool                   `json:"gate_release_requested"`
	AbortBeforeHandoffRequested  bool                   `json:"abort_before_handoff_requested,omitempty"`
	GateReleasedAt               time.Time              `json:"gate_released_at"`
	CleanupRequested             bool                   `json:"cleanup_requested"`
	RollbackStartRequested       bool                   `json:"rollback_start_requested"`
	RollbackGateReleaseRequested bool                   `json:"rollback_gate_release_requested"`
	RollbackGateReleasedAt       time.Time              `json:"rollback_gate_released_at"`
	RollbackDaemonPID            int                    `json:"rollback_daemon_pid"`
	RollbackDaemonIdentity       *LegacyProcessIdentity `json:"rollback_daemon_identity,omitempty"`
	RollbackDaemonStartedAt      string                 `json:"rollback_daemon_started_at"`
	RollbackReopenRequested      bool                   `json:"rollback_reopen_requested"`
	HealthPort                   int                    `json:"health_port"`
	AppPID                       int                    `json:"app_pid"`
	SwapStep                     int                    `json:"swap_step"`
	CurrentIdentity              string                 `json:"current_identity"`
	StagedIdentity               string                 `json:"staged_identity"`
	PreviousRollbackSHA256       string                 `json:"previous_rollback_sha256"`
	PreviousRollbackTreeSHA256   string                 `json:"previous_rollback_tree_sha256,omitempty"`
	GatePath                     string                 `json:"gate_path"`
	GateCreationIntent           bool                   `json:"gate_creation_intent"`
	GateIdentity                 string                 `json:"gate_identity"`
	OldBundle                    string                 `json:"old_bundle"`
	StagedBundle                 string                 `json:"staged_bundle"`
	RollbackBundle               string                 `json:"rollback_bundle"`
	OlderBundle                  string                 `json:"older_bundle"`
	OldBinarySHA256              string                 `json:"old_binary_sha256"`
	NewBinarySHA256              string                 `json:"new_binary_sha256"`
	OldTreeSHA256                string                 `json:"old_tree_sha256,omitempty"`
	AcceptanceAppIdentity        *LegacyProcessIdentity `json:"acceptance_app_identity,omitempty"`
	AcceptanceEpoch              *AcceptanceEpoch       `json:"acceptance_epoch,omitempty"`
	CandidateTreeSHA256          string                 `json:"candidate_tree_sha256,omitempty"`
	RollbackCleanupRequested     bool                   `json:"rollback_cleanup_requested,omitempty"`
	OldVersion                   string                 `json:"old_version"`
	NewVersion                   string                 `json:"new_version"`
	OldCommit                    string                 `json:"old_commit"`
	NewCommit                    string                 `json:"new_commit"`
	DataCompatibility            string                 `json:"data_compatibility"`
	OldDataCompatibility         string                 `json:"old_data_compatibility"`
	RuntimeIDs                   []string               `json:"runtime_ids"`
	Workspaces                   map[string][]string    `json:"workspaces"`
}

// SaveJournal syncs both the new contents and the directory entry before the
// caller takes the next step. Recovery never relies on a partially written file.
func SaveJournal(path string, j Journal) error {
	return saveJSON(path, j)
}

func saveJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".journal-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return fmt.Errorf("sync journal directory: %w", err)
	}
	return nil
}

// AcceptanceEpoch binds observed evidence to one authenticated daemon process.
type AcceptanceEpoch struct {
	Rollback  bool                  `json:"rollback"`
	Daemon    LegacyProcessIdentity `json:"daemon"`
	App       LegacyProcessIdentity `json:"app"`
	StartedAt string                `json:"started_at"`
	Since     time.Time             `json:"since"`
	Last      map[string]uint64     `json:"last"`
	Observed  map[string]int        `json:"observed"`
}
