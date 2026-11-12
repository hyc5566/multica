//go:build !darwin && !linux

package daemonupdate

import "errors"

func InspectLegacyProcess(int) (LegacyProcessIdentity, error) {
	return LegacyProcessIdentity{}, errors.New("legacy App process inspection requires macOS")
}

func StopLegacyProcess(LegacyProcessIdentity) error {
	return errors.New("legacy App process stop requires macOS")
}
