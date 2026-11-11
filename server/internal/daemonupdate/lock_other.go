//go:build !unix

package daemonupdate

import "errors"

func lockJournal(string) (func(), error) { return nil, errors.New("unsupported platform") }
