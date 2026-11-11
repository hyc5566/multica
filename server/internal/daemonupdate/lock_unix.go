//go:build unix

package daemonupdate

import (
	"errors"
	"os"
	"syscall"
)

func lockJournal(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	linked, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || !os.SameFile(info, linked) {
		file.Close()
		return nil, errors.New("invalid update lock")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}
