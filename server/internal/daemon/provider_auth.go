package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

const providerAuthLoginTimeout = 10 * time.Minute

// providerAuthLoginArgs lists only first-party CLI login commands. Custom
// runtime commands and providers without a documented login command fall back
// to manual recovery.
func providerAuthLoginArgs(provider string) ([]string, string) {
	switch provider {
	case "codex":
		return []string{"login"}, "codex login"
	case "claude":
		return []string{"auth", "login"}, "claude auth login"
	default:
		return nil, ""
	}
}

// runProviderAuthLogin only opens GNOME Terminal when the daemon inherited a
// graphical Linux session. Terminal output stays attached to the user's
// terminal; it is never captured in daemon logs or task output.
func runProviderAuthLogin(parent context.Context, provider, executable string, env map[string]string) (attempted bool, err error) {
	args, _ := providerAuthLoginArgs(provider)
	if len(args) == 0 || runtime.GOOS != "linux" || os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" || os.Getenv("SSH_TTY") != "" || (os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "") {
		return false, nil
	}
	terminal, err := exec.LookPath("gnome-terminal")
	if err != nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(parent, providerAuthLoginTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, terminal, append([]string{"--wait", "--", executable}, args...)...)
	cmd.Env = providerAuthEnvironment(provider, env)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return true, fmt.Errorf("provider login was cancelled or timed out")
		}
		return true, fmt.Errorf("provider login was cancelled or could not be completed")
	}
	return true, nil
}

func providerAuthEnvironment(provider string, values map[string]string) []string {
	allowed := map[string]bool{
		"HOME": true, "PATH": true, "USER": true, "SHELL": true,
		"DISPLAY": true, "WAYLAND_DISPLAY": true, "XAUTHORITY": true,
		"DBUS_SESSION_BUS_ADDRESS": true, "XDG_RUNTIME_DIR": true,
		"XDG_CONFIG_HOME": true, "LANG": true, "LC_ALL": true,
	}
	if provider == "codex" {
		allowed["CODEX_HOME"] = true
	}
	merged := make(map[string]string)
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			if allowed[key] {
				merged[key] = value
			}
		}
	}
	for key, value := range values {
		if allowed[key] {
			merged[key] = value
		}
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+merged[key])
	}
	return env
}
