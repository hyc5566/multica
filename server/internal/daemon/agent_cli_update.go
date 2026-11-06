package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
)

// ErrAgentUpdateUnsupported means the installed CLI has no verified update path.
var ErrAgentUpdateUnsupported = errors.New("agent CLI installation does not support automatic updates")

// pathInUserHome checks the actual destination, including symlinked ancestors.
func pathInUserHome(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(path) {
		return false
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(home, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func claudeExecutableInHome(path string) bool {
	resolved, err := exec.LookPath(path)
	return err == nil && pathInUserHome(resolved)
}

// updateAgentCLI updates the installation behind Path, never another copy found
// by resolving Command again. Callers serialize this with agent task starts.
func updateAgentCLI(ctx context.Context, provider string, entry AgentEntry) error {
	cmd, err := agentCLIUpdateCommand(provider, entry)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Update output may contain authenticated registry URLs. Discard it rather
	// than retaining unbounded output or exposing credentials in daemon logs.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := processtree.Run(ctx, cmd, 2*time.Second); err != nil {
		return fmt.Errorf("update %s CLI: %w", provider, err)
	}
	return nil
}

func agentCLIUpdateCommand(provider string, entry AgentEntry) (*exec.Cmd, error) {
	unsupported := func() (*exec.Cmd, error) { return nil, fmt.Errorf("%s: %w", provider, ErrAgentUpdateUnsupported) }
	// Windows npm shims and arbitrary wrappers cannot establish the package owner
	// from their path alone. Leave those installations to their existing updater.
	if runtime.GOOS == "windows" || !filepath.IsAbs(entry.Path) {
		return unsupported()
	}
	packageName, commandName := "", ""
	switch provider {
	case "codex":
		packageName, commandName = "@openai/codex", "codex"
	case "claude":
		packageName, commandName = "@anthropic-ai/claude-code", "claude"
	default:
		return unsupported()
	}
	resolved, err := filepath.EvalSymlinks(entry.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve installed %s CLI: %w", provider, err)
	}
	if _, err := exec.LookPath(resolved); err != nil {
		return nil, fmt.Errorf("check installed %s CLI: %w", provider, err)
	}
	if provider == "claude" && !claudeExecutableInHome(resolved) {
		return unsupported()
	}

	// Recognize Homebrew before npm: a formula may itself contain node_modules.
	for dir := filepath.Dir(resolved); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		parent := filepath.Dir(dir)
		kind := filepath.Base(parent)
		if kind != "Cellar" && kind != "Caskroom" {
			continue
		}
		if provider == "claude" {
			return unsupported()
		}
		pkg := filepath.Base(dir)
		if !(provider == "codex" && pkg == "codex" || provider == "claude" && (pkg == "claude-code" || pkg == "claude-code@latest")) {
			return unsupported()
		}
		prefix := filepath.Dir(parent)
		brew := filepath.Join(prefix, "bin", "brew")
		if _, err := exec.LookPath(brew); err != nil {
			return unsupported()
		}
		brewPath, err := filepath.EvalSymlinks(brew)
		if err != nil || !strings.HasPrefix(brewPath, prefix+string(filepath.Separator)) {
			// A launcher into a different Homebrew installation would upgrade
			// that installation instead of the executable discovered here.
			return unsupported()
		}
		relative, _ := filepath.Rel(dir, resolved)
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) < 2 {
			return unsupported()
		}
		receipt := filepath.Join(dir, parts[0], "INSTALL_RECEIPT.json")
		flag := "--formula"
		if kind == "Caskroom" {
			receipt, flag = filepath.Join(dir, ".metadata"), "--cask"
		}
		if _, err := os.Stat(receipt); err != nil {
			return unsupported()
		}
		cmd := exec.Command(brew, "upgrade", flag, pkg)
		cmd.Env = append(os.Environ(), "CI=1", "HOMEBREW_NO_INSTALL_CLEANUP=1", "HOMEBREW_NO_ENV_HINTS=1")
		cmd.Dir = prefix
		return cmd, nil
	}

	// Require the real executable to equal the package's declared bin, and the
	// manifest to live at the global npm prefix, not a repository node_modules.
	for root := filepath.Dir(resolved); root != filepath.Dir(root); root = filepath.Dir(root) {
		suffix := filepath.Join("lib", "node_modules", filepath.FromSlash(packageName))
		if !strings.HasSuffix(root, string(filepath.Separator)+suffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, "package.json"))
		if err != nil {
			return unsupported()
		}
		var manifest struct {
			Name string          `json:"name"`
			Bin  json.RawMessage `json:"bin"`
		}
		if json.Unmarshal(data, &manifest) != nil || manifest.Name != packageName {
			return unsupported()
		}
		var bins map[string]string
		var bin string
		if json.Unmarshal(manifest.Bin, &bins) == nil {
			bin = bins[commandName]
		} else {
			_ = json.Unmarshal(manifest.Bin, &bin)
		}
		if bin == "" {
			return unsupported()
		}
		declared, err := filepath.EvalSymlinks(filepath.Join(root, bin))
		if err != nil || declared != resolved {
			return unsupported()
		}
		prefix := strings.TrimSuffix(root, string(filepath.Separator)+suffix)
		if prefix == "" {
			prefix = string(filepath.Separator)
		}
		if provider == "claude" && (!pathInUserHome(prefix) || !pathInUserHome(filepath.Join(prefix, "bin"))) {
			return unsupported()
		}
		// Bind npm's --prefix explicitly. A daemon's PATH may select a different
		// node version or global prefix than the CLI originally discovered.
		npm := filepath.Join(prefix, "bin", "npm")
		if _, err := exec.LookPath(npm); err != nil {
			npm, err = exec.LookPath("npm")
			if err != nil {
				return unsupported()
			}
		}
		cmd := exec.Command(npm, "install", "--global", "--prefix", prefix, "--no-audit", "--no-fund", packageName+"@latest")
		cmd.Dir = prefix
		cmd.Env = append(os.Environ(), "CI=1", "npm_config_yes=true", "PATH="+filepath.Join(prefix, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
		return cmd, nil
	}

	if provider == "codex" {
		home, err := os.UserHomeDir()
		if err != nil {
			return unsupported()
		}
		release := filepath.Dir(filepath.Dir(resolved))
		standalone := filepath.Dir(filepath.Dir(release))
		if filepath.Base(resolved) != "codex" || filepath.Base(filepath.Dir(resolved)) != "bin" ||
			filepath.Base(filepath.Dir(release)) != "releases" || filepath.Base(standalone) != "standalone" ||
			filepath.Base(filepath.Dir(standalone)) != "packages" {
			return unsupported()
		}
		data, err := os.ReadFile(filepath.Join(release, "codex-package.json"))
		var manifest struct {
			LayoutVersion int    `json:"layoutVersion"`
			Version       string `json:"version"`
			Target        string `json:"target"`
			Variant       string `json:"variant"`
			Entrypoint    string `json:"entrypoint"`
		}
		if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.LayoutVersion != 1 ||
			manifest.Variant != "codex" || manifest.Entrypoint != "bin/codex" ||
			manifest.Version == "" || manifest.Target == "" || filepath.Base(release) != manifest.Version+"-"+manifest.Target {
			return unsupported()
		}
		current, err := filepath.EvalSymlinks(filepath.Join(standalone, "current", "bin", "codex"))
		if err != nil || current != resolved {
			return unsupported()
		}
		binDir := filepath.Join(home, ".local", "bin")
		launcher, err := filepath.EvalSymlinks(filepath.Join(binDir, "codex"))
		if err != nil || launcher != resolved {
			return unsupported()
		}
		cmd := exec.Command(resolved, "update")
		cmd.Dir = home
		// The standalone updater honors these environment variables. Derive its
		// target from the installed package, not a task's isolated CODEX_HOME or
		// inherited npm-wrapper hints, which could update a different copy.
		for _, env := range os.Environ() {
			key, _, _ := strings.Cut(env, "=")
			if key == "CODEX_HOME" || key == "CODEX_RELEASE" || key == "CODEX_NON_INTERACTIVE" ||
				strings.HasPrefix(key, "CODEX_INSTALL") || strings.HasPrefix(key, "CODEX_MANAGED_BY_") {
				continue
			}
			cmd.Env = append(cmd.Env, env)
		}
		cmd.Env = append(cmd.Env, "CI=1", "CODEX_NON_INTERACTIVE=1", "CODEX_HOME="+filepath.Dir(filepath.Dir(standalone)), "CODEX_INSTALL_DIR="+binDir,
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		return cmd, nil
	}

	if provider == "claude" {
		home, err := os.UserHomeDir()
		if err != nil {
			return unsupported()
		}
		versions := filepath.Join(home, ".local", "share", "claude", "versions")
		versions, err = filepath.EvalSymlinks(versions)
		if err != nil || filepath.Dir(resolved) != versions {
			return unsupported()
		}
		launcher, err := filepath.EvalSymlinks(filepath.Join(home, ".local", "bin", "claude"))
		if err != nil || launcher != resolved || !pathInUserHome(filepath.Join(home, ".local", "bin")) {
			return unsupported()
		}
		cmd := exec.Command(resolved, "update")
		cmd.Dir = home
		cmd.Env = append(os.Environ(), "CI=1")
		return cmd, nil
	}
	return unsupported()
}

// updatedAgentCLIPath follows the stable launcher of the installation just
// updated, including when its previous release still exists. It must be called
// only after updateAgentCLI succeeds while the caller still holds its lock.
// entry.Path must be the canonical path captured BEFORE starting the updater,
// so a changed launcher cannot substitute another installation's provenance.
func updatedAgentCLIPath(provider string, entry AgentEntry) (string, error) {
	if provider != "codex" && provider != "claude" || !filepath.IsAbs(entry.Path) {
		return "", ErrAgentUpdateUnsupported
	}
	// Do not resolve the old path again: its file may have been removed or
	// replaced by a link during the update. Its original owner is still fixed.
	previous := filepath.Clean(entry.Path)
	// Default discovery retains the private launcher so rediscovery never
	// falls back to PATH. Resolve that launcher before identifying its owner.
	if provider == "claude" {
		if home, err := os.UserHomeDir(); err == nil && previous == filepath.Join(home, ".local", "bin", "claude") {
			if _, err := agentCLIUpdateCommand(provider, entry); err != nil {
				return "", err
			}
			var err error
			previous, err = filepath.EvalSymlinks(previous)
			if err != nil {
				return "", err
			}
		}
	}
	owner, launcher := "", ""
	packageName := "@openai/codex"
	if provider == "claude" {
		packageName = "@anthropic-ai/claude-code"
	}
	for dir := filepath.Dir(previous); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		parent := filepath.Dir(dir)
		kind := filepath.Base(parent)
		if kind == "Cellar" || kind == "Caskroom" {
			if provider == "claude" {
				return "", ErrAgentUpdateUnsupported
			}
			owner, launcher = dir, filepath.Join(filepath.Dir(parent), "bin", provider)
			break
		}
		suffix := string(filepath.Separator) + filepath.Join("lib", "node_modules", filepath.FromSlash(packageName))
		if strings.HasSuffix(dir, suffix) {
			prefix := strings.TrimSuffix(dir, suffix)
			if prefix == "" {
				prefix = string(filepath.Separator)
			}
			owner, launcher = dir, filepath.Join(prefix, "bin", provider)
			break
		}
	}
	if owner == "" && provider == "codex" {
		release := filepath.Dir(filepath.Dir(previous))
		standalone := filepath.Dir(filepath.Dir(release))
		if filepath.Base(previous) == "codex" && filepath.Base(filepath.Dir(previous)) == "bin" &&
			filepath.Base(filepath.Dir(release)) == "releases" && filepath.Base(standalone) == "standalone" &&
			filepath.Base(filepath.Dir(standalone)) == "packages" {
			owner, launcher = filepath.Join(standalone, "releases"), filepath.Join(standalone, "current", "bin", "codex")
		}
	}
	if owner == "" && provider == "claude" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		versions, err := filepath.EvalSymlinks(filepath.Join(home, ".local", "share", "claude", "versions"))
		if err == nil && filepath.Dir(previous) == versions {
			owner, launcher = versions, filepath.Join(home, ".local", "bin", "claude")
		}
	}
	if launcher == "" {
		return "", ErrAgentUpdateUnsupported
	}
	if provider == "claude" && !pathInUserHome(owner) {
		return "", ErrAgentUpdateUnsupported
	}
	resolved, err := filepath.EvalSymlinks(launcher)
	if err != nil {
		return "", fmt.Errorf("resolve updated %s CLI: %w", provider, err)
	}
	if !strings.HasPrefix(resolved, owner+string(filepath.Separator)) {
		return "", fmt.Errorf("updated %s launcher points outside original installation: %w", provider, ErrAgentUpdateUnsupported)
	}
	// Reuse the full ownership/manifest/executable checks for the new version.
	if _, err := agentCLIUpdateCommand(provider, AgentEntry{Path: launcher}); err != nil {
		return "", err
	}
	return resolved, nil
}
