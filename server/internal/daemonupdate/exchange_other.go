//go:build !linux && !darwin

package daemonupdate

import "errors"

func exchangeBundles(string, string) error { return errors.New("atomic bundle exchange unsupported") }
