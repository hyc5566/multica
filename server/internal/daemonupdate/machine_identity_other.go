//go:build !darwin && !linux

package daemonupdate

import "errors"

func legacyMachineCommand(string, ...string) ([]byte, error) {
	return nil, errors.New("App machine identity requires macOS")
}
