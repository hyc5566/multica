//go:build !unix

package daemonupdate

import "errors"

func legacyDevice(string) (uint64, error) { return 0, errors.New("filesystem identity unavailable") }
