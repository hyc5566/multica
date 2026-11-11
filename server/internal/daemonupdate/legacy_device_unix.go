//go:build unix

package daemonupdate

import (
	"errors"
	"os"
	"syscall"
)

func legacyDevice(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("filesystem identity unavailable")
	}
	return uint64(s.Dev), nil
}
