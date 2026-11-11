//go:build darwin || linux

package daemonupdate

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLegacyProcessNativeMacOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS process evidence")
	}
	command := exec.Command("/bin/sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	defer func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		<-waited
	}()
	identity, err := InspectLegacyProcess(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if identity.PID != command.Process.Pid || identity.UID != os.Getuid() || identity.ExecutablePath != "/bin/sleep" || identity.Inode == 0 || len(identity.SHA256) != 64 {
		t.Fatalf("incomplete native identity: %+v", identity)
	}
	if err := StopLegacyProcess(identity); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyProcessBindsMappedInodeAndRechecksIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Multica 繁中版")
	data := []byte("fixture executable bytes")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	const pid = 987654
	for _, fault := range []string{"", "wrong uid", "missing map", "wrong inode", "changed start", "permission", "duplicate map", "symlink"} {
		t.Run(fault, func(t *testing.T) {
			processReads := 0
			mappedPath := path
			if fault == "symlink" {
				mappedPath = filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(path, mappedPath); err != nil {
					t.Fatal(err)
				}
			}
			run := func(command string, args ...string) ([]byte, error) {
				if command == "/usr/sbin/lsof" {
					if fault == "permission" {
						return nil, errors.New("denied")
					}
					inode := uint64(stat.Ino)
					if fault == "wrong inode" {
						inode++
					}
					mapName := mappedPath
					if fault == "missing map" {
						mapName += " (deleted)"
					}
					output := fmt.Sprintf("p%d\nftxt\nD0x%x\ni%d\nn%s\n", pid, uint64(stat.Dev), inode, mapName)
					if fault == "duplicate map" {
						output += fmt.Sprintf("ftxt\nD0x%x\ni%d\nn%s\n", uint64(stat.Dev), inode, mapName)
					}
					return []byte(output), nil
				}
				if args[len(args)-1] == "comm=" {
					return []byte(mappedPath + "\n"), nil
				}
				processReads++
				uid := os.Getuid()
				if fault == "wrong uid" {
					uid++
				}
				started := "Thu Nov 12 04:00:00 2026"
				if fault == "changed start" && processReads > 1 {
					started = "Thu Nov 12 04:00:01 2026"
				}
				return []byte(strconv.Itoa(uid) + " " + started + "\n"), nil
			}
			identity, err := inspectLegacyMacProcess(pid, os.Getuid(), run)
			if fault == "" {
				if err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf("%x", sha256.Sum256(data))
				if identity.SHA256 != want || identity.ExecutablePath != path || identity.Inode != uint64(stat.Ino) || processReads != 2 {
					t.Fatalf("incomplete identity: %+v", identity)
				}
			} else if err == nil {
				t.Fatalf("accepted %s: %+v", fault, identity)
			}
		})
	}
}

func TestLegacyStopSignalsOnlyExactIdentity(t *testing.T) {
	expected := LegacyProcessIdentity{PID: 987654, UID: os.Getuid(), StartedAt: "Thu Nov 12 04:00:00 2026", ExecutablePath: "/fixture/App", Device: 1, Inode: 2, SHA256: strings.Repeat("a", 64)}
	for _, fault := range []string{"", "changed before", "changed after", "inspection denied", "signal denied", "timeout"} {
		t.Run(fault, func(t *testing.T) {
			calls, signals := 0, 0
			inspect := func(pid int) (LegacyProcessIdentity, error) {
				calls++
				if fault == "inspection denied" {
					return LegacyProcessIdentity{}, errors.New("denied")
				}
				if fault == "changed before" || (fault == "changed after" && calls > 1) {
					changed := expected
					changed.Inode++
					return changed, nil
				}
				if fault != "timeout" && calls > 1 {
					return LegacyProcessIdentity{}, ErrLegacyProcessExited
				}
				return expected, nil
			}
			signal := func(pid int, sig syscall.Signal) error {
				signals++
				if pid != expected.PID || sig != syscall.SIGTERM {
					t.Fatal("wrong signal target")
				}
				if fault == "signal denied" {
					return errors.New("denied")
				}
				return nil
			}
			err := stopLegacyProcess(expected, inspect, signal, time.Millisecond, 2*time.Millisecond)
			if (err == nil) != (fault == "") {
				t.Fatalf("fault %q: %v", fault, err)
			}
			wantSignals := 1
			if fault == "changed before" || fault == "inspection denied" {
				wantSignals = 0
			}
			if signals != wantSignals {
				t.Fatalf("signals=%d, want=%d", signals, wantSignals)
			}
		})
	}
}
