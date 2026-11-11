//go:build darwin || linux

package daemonupdate

func legacyMachineCommand(name string, args ...string) ([]byte, error) {
	return legacyNativeCommand(name, args...)
}
