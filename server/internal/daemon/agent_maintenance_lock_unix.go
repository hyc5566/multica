//go:build !windows

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockAgentMaintenance(path string) (*os.File, error) {
	return lockAgentInstallation(path, true)
}

func releaseAgentInstallation(f *os.File) {
	if f == nil {
		return
	}
	// A concurrently forked child can inherit this open-file description until
	// exec. Unlock explicitly rather than waiting for its last reference to die.
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	_ = f.Close()
}

func lockAgentInstallation(path string, exclusive bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if err := unix.Flock(int(f.Fd()), mode|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}
