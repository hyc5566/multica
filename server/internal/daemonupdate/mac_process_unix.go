//go:build darwin || linux

package daemonupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type legacyCommand func(string, ...string) ([]byte, error)

func legacyNativeCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	return command.Output()
}

// InspectLegacyProcess requires native macOS process and mapped-inode evidence.
func InspectLegacyProcess(pid int) (LegacyProcessIdentity, error) {
	if runtime.GOOS != "darwin" {
		return LegacyProcessIdentity{}, errors.New("legacy App process inspection requires macOS")
	}
	identity, err := inspectLegacyMacProcess(pid, os.Getuid(), legacyNativeCommand)
	if err != nil && syscall.Kill(pid, 0) == syscall.ESRCH {
		return LegacyProcessIdentity{}, ErrLegacyProcessExited
	}
	return identity, err
}

func inspectLegacyMacProcess(pid, uid int, run legacyCommand) (LegacyProcessIdentity, error) {
	var identity LegacyProcessIdentity
	if pid <= 1 || pid == os.Getpid() {
		return identity, errors.New("invalid legacy target PID")
	}
	pidText := strconv.Itoa(pid)
	process := func() (string, error) {
		output, err := run("/bin/ps", "-p", pidText, "-o", "uid=", "-o", "lstart=")
		fields := strings.Fields(string(output))
		if err != nil || len(fields) != 6 || fields[0] != strconv.Itoa(uid) {
			return "", errors.New("legacy process UID or start time unavailable")
		}
		started := strings.Join(fields[1:], " ")
		if _, err := time.Parse("Mon Jan 2 15:04:05 2006", started); err != nil {
			return "", errors.New("invalid legacy process start time")
		}
		return started, nil
	}
	started, err := process()
	if err != nil {
		return identity, err
	}
	commandPath := func() (string, error) {
		output, err := run("/bin/ps", "-p", pidText, "-o", "comm=")
		path := strings.TrimSpace(string(output))
		if err != nil || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
			return "", errors.New("legacy process executable path unavailable")
		}
		return path, nil
	}
	path, err := commandPath()
	if err != nil {
		return identity, err
	}
	output, err := run("/usr/sbin/lsof", "-a", "-p", pidText, "-d", "txt", "-FpfinD")
	if err != nil {
		return identity, errors.New("legacy mapped executable evidence unavailable")
	}
	device, inode, err := legacyMappedInode(string(output), pid, path)
	if err != nil {
		return identity, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return identity, errors.New("cannot open legacy executable without following links")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return identity, errors.New("legacy executable is not a regular file")
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != device || uint64(stat.Ino) != inode {
		return identity, errors.New("legacy executable path differs from mapped inode")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return identity, errors.New("cannot hash legacy executable")
	}
	after, err := f.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, pathInfo) ||
		before.Size() != after.Size() || before.Size() != pathInfo.Size() ||
		!before.ModTime().Equal(after.ModTime()) || !before.ModTime().Equal(pathInfo.ModTime()) {
		return identity, errors.New("legacy executable changed during inspection")
	}
	currentStart, err := process()
	currentPath, pathErr := commandPath()
	if err != nil || pathErr != nil || currentStart != started || currentPath != path {
		return identity, errors.New("legacy process changed during inspection")
	}
	return LegacyProcessIdentity{pid, uid, started, path, device, inode, fmt.Sprintf("%x", h.Sum(nil))}, nil
}

func legacyMappedInode(output string, pid int, path string) (uint64, uint64, error) {
	type record struct{ device, inode, path string }
	var records []record
	var current *record
	seenPID := false
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			if line[1:] != strconv.Itoa(pid) || seenPID {
				return 0, 0, errors.New("unexpected process in executable evidence")
			}
			seenPID = true
		case 'f':
			records = append(records, record{})
			current = &records[len(records)-1]
		case 'D', 'i', 'n':
			if !seenPID || current == nil {
				return 0, 0, errors.New("malformed mapped executable evidence")
			}
			switch line[0] {
			case 'D':
				current.device = line[1:]
			case 'i':
				current.inode = line[1:]
			case 'n':
				current.path = line[1:]
			}
		}
	}
	var match *record
	for i := range records {
		if records[i].path == path {
			if match != nil {
				return 0, 0, errors.New("ambiguous legacy executable mappings")
			}
			match = &records[i]
		}
	}
	if !seenPID || match == nil {
		return 0, 0, errors.New("legacy executable mapping not found")
	}
	device, deviceErr := strconv.ParseUint(match.device, 0, 64)
	inode, inodeErr := strconv.ParseUint(match.inode, 10, 64)
	if deviceErr != nil || inodeErr != nil || inode == 0 {
		return 0, 0, errors.New("invalid legacy mapped inode")
	}
	return device, inode, nil
}

// StopLegacyProcess never escalates to SIGKILL or signals a replacement PID.
func StopLegacyProcess(expected LegacyProcessIdentity) error {
	if runtime.GOOS != "darwin" {
		return errors.New("legacy App process stop requires macOS")
	}
	return stopLegacyProcess(expected, InspectLegacyProcess, syscall.Kill, 100*time.Millisecond, 30*time.Second)
}

func stopLegacyProcess(expected LegacyProcessIdentity, inspect func(int) (LegacyProcessIdentity, error), signal func(int, syscall.Signal) error, poll, timeout time.Duration) error {
	_, digestErr := hex.DecodeString(expected.SHA256)
	if expected.PID <= 1 || expected.PID == os.Getpid() || expected.UID != os.Getuid() ||
		expected.StartedAt == "" || !filepath.IsAbs(expected.ExecutablePath) || expected.Inode == 0 || len(expected.SHA256) != 64 || digestErr != nil {
		return errors.New("incomplete legacy process identity")
	}
	actual, err := inspect(expected.PID)
	if err != nil || actual != expected {
		return errors.New("legacy process changed before SIGTERM")
	}
	if err := signal(expected.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("legacy process SIGTERM failed: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		actual, err := inspect(expected.PID)
		if errors.Is(err, ErrLegacyProcessExited) {
			return nil
		}
		if err != nil || actual != expected {
			return errors.New("cannot confirm original legacy process exit")
		}
		if !time.Now().Before(deadline) {
			return errors.New("legacy process did not exit after SIGTERM")
		}
		time.Sleep(poll)
	}
}
