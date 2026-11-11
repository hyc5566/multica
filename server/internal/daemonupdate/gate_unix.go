//go:build unix

package daemonupdate

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func gateIdentity(info os.FileInfo) (string, error) {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) || !info.IsDir() {
		return "", fmt.Errorf("gate owner or type mismatch")
	}
	return fmt.Sprintf("%d:%d", s.Dev, s.Ino), nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Geteuid())
}

func processExists(pid int) (bool, error) {
	if pid <= 0 {
		return false, fmt.Errorf("invalid pid")
	}
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}
