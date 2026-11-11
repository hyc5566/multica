//go:build !unix

package daemon

import "os"

const maintenanceShutdownSupported = false

func maintenanceGateIdentity(os.FileInfo) string     { return "" }
func maintenanceOwnedByCurrentUser(os.FileInfo) bool { return false }
