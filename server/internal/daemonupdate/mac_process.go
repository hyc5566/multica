package daemonupdate

import "errors"

// LegacyProcessIdentity binds a macOS process to its mapped executable.
// Callers must additionally bind the identity to their approved App inventory.
type LegacyProcessIdentity struct {
	PID            int    `json:"pid"`
	UID            int    `json:"uid"`
	StartedAt      string `json:"started_at"`
	ExecutablePath string `json:"executable_path"`
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	SHA256         string `json:"sha256"`
}

var ErrLegacyProcessExited = errors.New("legacy process no longer exists")
