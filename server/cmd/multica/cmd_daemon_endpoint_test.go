//go:build !windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDynamicDaemonEndpointIsolation(t *testing.T) {
	t.Setenv("MULTICA_DAEMON_DYNAMIC_PORT", "true")
	t.Setenv("HOME", t.TempDir())
	if got := healthPortForProfile(""); got != 0 {
		t.Fatalf("unstarted port = %d", got)
	}
	listener, release, err := reserveDaemonEndpoint("")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	port := listener.Addr().(*net.TCPAddr).Port
	if got := healthPortForProfile(""); got != port {
		t.Fatalf("discovered %d want %d", got, port)
	}
	if _, _, err := reserveDaemonEndpoint(""); err == nil {
		t.Fatal("duplicate daemon accepted")
	}
	health := map[string]any{"pid": float64(os.Getpid()), "profile": ""}
	if err := daemonIdentityMismatch(health, "", port); err != nil {
		t.Fatal(err)
	}
	health["pid"] = float64(os.Getpid() + 1)
	if err := daemonIdentityMismatch(health, "", port); err == nil {
		t.Fatal("stale port trusted a different PID")
	}
	// Another user's state directory gets a distinct live port, without a profile.
	t.Setenv("HOME", t.TempDir())
	other, closeOther, err := reserveDaemonEndpoint("")
	if err != nil {
		t.Fatal(err)
	}
	defer closeOther()
	if other.Addr().(*net.TCPAddr).Port == port {
		t.Fatal("users share a port")
	}
	if err := os.WriteFile(filepath.Join(daemonDirForProfile(""), "daemon.endpoint.json"), []byte(`{"port":99999,"pid":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if healthPortForProfile("") != 0 {
		t.Fatal("invalid endpoint accepted")
	}
	if dynamicDaemonPort("desktop-host") {
		t.Fatal("Desktop profile port changed")
	}
}
