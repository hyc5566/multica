package daemon

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/cli"
)

type maintenanceGateStatus struct {
	Supported      bool   `json:"supported"`
	Path           string `json:"path"`
	Blocked        bool   `json:"blocked"`
	Ready          bool   `json:"ready"`
	ClaimsInFlight int    `json:"claims_in_flight"`
}

// maintenanceGateLocked reads the external gate while holding claimMu. An
// invalid or inaccessible gate blocks work, but cannot attest readiness.
func (d *Daemon) maintenanceGateLocked() maintenanceGateStatus {
	s := maintenanceGateStatus{Supported: true, Blocked: true, ClaimsInFlight: d.claimsInFlight}
	dir, err := cli.ProfileDir(d.cfg.Profile)
	if err != nil {
		return s
	}
	s.Path = filepath.Join(dir, "daemon.maintenance")
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		s.Blocked = false
		return s
	}
	if err != nil || !info.IsDir() {
		return s
	}
	f, err := os.Open(s.Path)
	if err != nil {
		return s
	}
	opened, statErr := f.Stat()
	searched, searchErr := os.Stat(s.Path + string(os.PathSeparator) + ".")
	_, readErr := f.Readdirnames(1)
	closeErr := f.Close()
	if statErr != nil || searchErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, searched) ||
		(readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil {
		return s
	}
	s.Ready = d.claimsInFlight == 0 && d.activeTasks.Load() == 0 && !d.updating.Load() && !d.pauseClaims
	return s
}

func (d *Daemon) maintenanceGate() maintenanceGateStatus {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	return d.maintenanceGateLocked()
}
