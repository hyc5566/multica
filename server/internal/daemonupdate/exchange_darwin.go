//go:build darwin

package daemonupdate

import "golang.org/x/sys/unix"

func exchangeBundles(a, b string) error { return unix.RenamexNp(a, b, unix.RENAME_SWAP) }
