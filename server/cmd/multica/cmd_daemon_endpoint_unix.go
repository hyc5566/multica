//go:build !windows

package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func lockDaemonEndpoint(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another daemon owns this user's state directory: %w", err)
	}
	return file, nil
}
