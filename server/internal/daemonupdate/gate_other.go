//go:build !unix

package daemonupdate

import (
	"errors"
	"os"
)

func gateIdentity(os.FileInfo) (string, error) { return "", errors.New("unsupported platform") }
func ownedByCurrentUser(os.FileInfo) bool      { return false }
func processExists(int) (bool, error)          { return false, errors.New("unsupported platform") }
