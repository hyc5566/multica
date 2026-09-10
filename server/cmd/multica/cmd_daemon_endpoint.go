package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Installer-managed defaults use an OS-assigned port. Existing Desktop/named
// profiles retain their established port contracts.
func dynamicDaemonPort(profile string) bool {
	return profile == "" && os.Getenv("MULTICA_DAEMON_DYNAMIC_PORT") == "true"
}

type daemonEndpoint struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

func readDaemonEndpoint(profile string) daemonEndpoint {
	var endpoint daemonEndpoint
	data, err := os.ReadFile(filepath.Join(daemonDirForProfile(profile), "daemon.endpoint.json"))
	if err != nil || json.Unmarshal(data, &endpoint) != nil || endpoint.Port < 1 || endpoint.Port > 65535 || endpoint.PID <= 0 {
		return daemonEndpoint{}
	}
	return endpoint
}

func reserveDaemonEndpoint(profile string) (net.Listener, func(), error) {
	dir := daemonDirForProfile(profile)
	if dir == "" {
		return nil, nil, fmt.Errorf("cannot resolve daemon state directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	lock, err := lockDaemonEndpoint(filepath.Join(dir, "daemon.endpoint.lock"))
	if err != nil {
		return nil, nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		lock.Close()
		return nil, nil, err
	}
	path := filepath.Join(dir, "daemon.endpoint.json")
	data, _ := json.Marshal(daemonEndpoint{Port: listener.Addr().(*net.TCPAddr).Port, PID: os.Getpid()})
	// Publish discovery only after the listener owns its port; rename is atomic.
	err = os.WriteFile(path+".tmp", data, 0600)
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}
	if err != nil {
		listener.Close()
		lock.Close()
		return nil, nil, err
	}
	return listener, func() { listener.Close(); os.Remove(path); lock.Close() }, nil
}
