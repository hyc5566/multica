package daemon

// LaunchdPinnedManager delegates every Multica binary change and restart to
// the offline, gate-aware operator. It does not disable agent CLI maintenance.
const LaunchdPinnedManager = "launchd-pinned"

// DesktopProtocol identifies the local health/connect-only contract. It does
// not attest compatibility with arbitrary remote server versions.
const DesktopProtocol = 1

func (d *Daemon) independentlyPinned() bool {
	return d.cfg.LifecycleManager == LaunchdPinnedManager
}
