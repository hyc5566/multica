package main

import (
	"errors"
	"os"
	"runtime"
	"strconv"
)

// errSupervisorRestart ends a systemd-managed foreground daemon after an
// update so the service manager restarts it through ExecStart (the zh-tw
// launcher), which now points at the new version. Spawning a successor and
// exiting 0 instead would leave the unit inactive under Restart=on-failure,
// and KillMode=control-group would then kill the successor with the cgroup.
var errSupervisorRestart = errors.New("daemon updated; exiting so systemd restarts it with the new binary")

// supervisedBySystemd reports whether this process is the main process of a
// systemd service. SYSTEMD_EXEC_PID (systemd >= 248) names the PID systemd
// spawned; the launcher execs the binary, so it equals ours only when systemd
// started us directly. A daemon started from a terminal inherits a different
// value. Units written by the zh-tw installer also set
// MULTICA_DAEMON_SUPERVISOR=systemd for older systemd versions.
func supervisedBySystemd(getenv func(string) string, pid int, goos string) bool {
	if goos != "linux" {
		return false
	}
	if execPID := getenv("SYSTEMD_EXEC_PID"); execPID != "" {
		return execPID == strconv.Itoa(pid)
	}
	return getenv("MULTICA_DAEMON_SUPERVISOR") == "systemd" && getenv("INVOCATION_ID") != ""
}

func daemonSupervisedBySystemd() bool {
	return supervisedBySystemd(os.Getenv, os.Getpid(), runtime.GOOS)
}
