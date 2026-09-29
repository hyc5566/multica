package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func updateFixture(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func updateNPMFixture(t *testing.T, provider, pkg string) (string, AgentEntry) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix installation layouts")
	}
	prefix := t.TempDir()
	if provider == "claude" {
		t.Setenv("HOME", prefix)
	}
	root := filepath.Join(prefix, "lib", "node_modules", filepath.FromSlash(pkg))
	path := updateFixture(t, filepath.Join(root, "bin", provider), "#!/bin/sh\nexit 91\n")
	updateFixture(t, filepath.Join(root, "package.json"), `{"name":"`+pkg+`","bin":{"`+provider+`":"bin/`+provider+`"}}`)
	link := filepath.Join(prefix, "bin", provider)
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	updateFixture(t, filepath.Join(prefix, "bin", "npm"), "#!/bin/sh\nexit 0\n")
	return prefix, AgentEntry{Path: link, Command: provider}
}

func TestAgentCLIUpdateNPMProvenance(t *testing.T) {
	for _, tc := range []struct{ provider, pkg string }{{"codex", "@openai/codex"}, {"claude", "@anthropic-ai/claude-code"}} {
		t.Run(tc.provider, func(t *testing.T) {
			prefix, entry := updateNPMFixture(t, tc.provider, tc.pkg)
			t.Setenv("PATH", t.TempDir())
			entry.Command = "/another/copy/" + tc.provider
			cmd, err := agentCLIUpdateCommand(tc.provider, entry)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{filepath.Join(prefix, "bin", "npm"), "install", "--global", "--prefix", prefix, "--no-audit", "--no-fund", tc.pkg + "@latest"}
			if !reflect.DeepEqual(cmd.Args, want) || cmd.Dir != prefix {
				t.Fatalf("command = %v, dir = %s", cmd.Args, cmd.Dir)
			}
			if err := updateAgentCLI(context.Background(), tc.provider, entry); err != nil {
				t.Fatal(err)
			}
			entry.Path = updateFixture(t, filepath.Join(prefix, "lib", "node_modules", filepath.FromSlash(tc.pkg), "foreign"), "#!/bin/sh\nexit 0\n")
			if _, err := agentCLIUpdateCommand(tc.provider, entry); !errors.Is(err, ErrAgentUpdateUnsupported) {
				t.Fatalf("foreign bin = %v", err)
			}
		})
	}
}

func TestAgentCLIUpdateBrewProvenance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installation layouts")
	}
	for _, tc := range []struct{ provider, pkg, kind, flag string }{{"codex", "codex", "Caskroom", "--cask"}, {"codex", "codex", "Cellar", "--formula"}, {"claude", "claude-code", "Caskroom", "--cask"}, {"claude", "claude-code@latest", "Caskroom", "--cask"}} {
		t.Run(tc.kind+tc.pkg, func(t *testing.T) {
			prefix := t.TempDir()
			brew := updateFixture(t, filepath.Join(prefix, "bin", "brew"), "#!/bin/sh\nexit 0\n")
			pkgRoot := filepath.Join(prefix, tc.kind, tc.pkg)
			path := updateFixture(t, filepath.Join(pkgRoot, "1.0", "bin", tc.provider), "#!/bin/sh\nexit 91\n")
			receipt := filepath.Join(pkgRoot, "1.0", "INSTALL_RECEIPT.json")
			if tc.kind == "Caskroom" {
				receipt = filepath.Join(pkgRoot, ".metadata", "receipt")
			}
			updateFixture(t, receipt, "{}")
			cmd, err := agentCLIUpdateCommand(tc.provider, AgentEntry{Path: path})
			if tc.provider == "claude" {
				if !errors.Is(err, ErrAgentUpdateUnsupported) {
					t.Fatalf("shared Homebrew installation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{brew, "upgrade", tc.flag, tc.pkg}; !reflect.DeepEqual(cmd.Args, want) {
				t.Fatalf("args=%v", cmd.Args)
			}
			if err := os.Remove(receipt); err != nil {
				t.Fatal(err)
			}
			if tc.kind == "Caskroom" {
				if err := os.Remove(filepath.Dir(receipt)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := agentCLIUpdateCommand(tc.provider, AgentEntry{Path: path}); !errors.Is(err, ErrAgentUpdateUnsupported) {
				t.Fatalf("missing receipt: %v", err)
			}
		})
	}
}

func TestAgentCLIUpdateNativeClaudeAndUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installation layouts")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	path := updateFixture(t, filepath.Join(home, ".local", "share", "claude", "versions", "1.0"), "#!/bin/sh\nexit 0\n")
	link := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	cmd, err := agentCLIUpdateCommand("claude", AgentEntry{Path: link})
	if err != nil || cmd == nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cmd.Args, []string{path, "update"}) {
		t.Fatal(cmd.Args)
	}
	t.Setenv("MULTICA_CLAUDE_PATH", "")
	t.Setenv("SHELL", filepath.Join(home, "fish"))
	entry, ok := probeAgentCLIs()["claude"]
	if !ok {
		t.Fatal("private Claude not discovered")
	}
	if got, err := updatedAgentCLIPath("claude", entry); err != nil || got != path {
		t.Fatalf("default discovery must support maintenance: %q, %v", got, err)
	}
	for _, tc := range []struct{ provider, path string }{{"codex", path}, {"gemini", path}, {"claude", updateFixture(t, filepath.Join(home, "wrapper"), "#!/bin/sh\nexit 0\n")}, {"claude", "claude"}} {
		if _, err := agentCLIUpdateCommand(tc.provider, AgentEntry{Path: tc.path}); !errors.Is(err, ErrAgentUpdateUnsupported) {
			t.Errorf("%s %s: %v", tc.provider, tc.path, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := agentCLIUpdateCommand("claude", AgentEntry{Path: link}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing CLI: %v", err)
	}
}

func TestClaudeUpdateRequiresPrivateInstallation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installation layouts")
	}
	for _, kind := range []string{"shared-npm", "prefix-outside-home", "bin-outside-home", "native-symlink-outside-home", "brew-in-home"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("PATH", t.TempDir())
			var entry AgentEntry
			switch kind {
			case "shared-npm", "prefix-outside-home", "bin-outside-home":
				prefix, installed := updateNPMFixture(t, "claude", "@anthropic-ai/claude-code")
				entry = installed
				if kind == "prefix-outside-home" {
					home = filepath.Join(prefix, "lib", "node_modules")
				}
				if kind == "bin-outside-home" {
					home = prefix
					bin := filepath.Join(prefix, "bin")
					external := filepath.Join(t.TempDir(), "bin")
					if err := os.Rename(bin, external); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(external, bin); err != nil {
						t.Fatal(err)
					}
				}
			case "native-symlink-outside-home":
				versions := filepath.Join(t.TempDir(), "versions")
				path := updateFixture(t, filepath.Join(versions, "1.0"), "#!/bin/sh\nexit 91\n")
				privateVersions := filepath.Join(home, ".local", "share", "claude", "versions")
				if err := os.MkdirAll(filepath.Dir(privateVersions), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(versions, privateVersions); err != nil {
					t.Fatal(err)
				}
				entry.Path = filepath.Join(home, ".local", "bin", "claude")
				if err := os.MkdirAll(filepath.Dir(entry.Path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, entry.Path); err != nil {
					t.Fatal(err)
				}
			case "brew-in-home":
				entry.Path = updateFixture(t, filepath.Join(home, "Caskroom", "claude-code", "1.0", "claude"), "#!/bin/sh\nexit 91\n")
				updateFixture(t, filepath.Join(home, "bin", "brew"), "#!/bin/sh\nexit 91\n")
				updateFixture(t, filepath.Join(home, "Caskroom", "claude-code", ".metadata", "receipt"), "{}")
			}
			t.Setenv("HOME", home)
			if _, err := agentCLIUpdateCommand("claude", entry); !errors.Is(err, ErrAgentUpdateUnsupported) {
				t.Fatalf("unsafe updater accepted: %v", err)
			}
			resolved, err := filepath.EvalSymlinks(entry.Path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := updatedAgentCLIPath("claude", AgentEntry{Path: resolved}); !errors.Is(err, ErrAgentUpdateUnsupported) {
				t.Fatalf("unsafe updated path accepted: %v", err)
			}
		})
	}
}

func TestAgentCLIUpdateExecutionFailureAndCancellation(t *testing.T) {
	prefix, entry := updateNPMFixture(t, "codex", "@openai/codex")
	npm := filepath.Join(prefix, "bin", "npm")
	updateFixture(t, npm, "#!/bin/sh\nprintf 'https://secret-token@registry.invalid' >&2\nexit 42\n")
	err := updateAgentCLI(context.Background(), "codex", entry)
	if err == nil || strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "42") {
		t.Fatalf("failure=%v", err)
	}
	updateFixture(t, npm, "#!/bin/sh\nwhile :; do :; done\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = updateAgentCLI(ctx, "codex", entry)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation=%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("update did not stop promptly")
	}
}

func TestAgentCLIUpdateNPMRequiresExistingPackageAndManager(t *testing.T) {
	prefix, entry := updateNPMFixture(t, "codex", "@openai/codex")
	if err := os.Remove(filepath.Join(prefix, "bin", "npm")); err != nil {
		t.Fatal(err)
	}
	managerDir := t.TempDir()
	t.Setenv("PATH", managerDir)
	if _, err := agentCLIUpdateCommand("codex", entry); !errors.Is(err, ErrAgentUpdateUnsupported) {
		t.Fatalf("missing manager: %v", err)
	}
	npm := updateFixture(t, filepath.Join(managerDir, "npm"), "#!/bin/sh\nexit 0\n")
	cmd, err := agentCLIUpdateCommand("codex", entry)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != npm || cmd.Args[4] != prefix {
		t.Fatalf("fallback manager changed prefix: %v", cmd.Args)
	}
	manifest := filepath.Join(prefix, "lib", "node_modules", "@openai", "codex", "package.json")
	updateFixture(t, manifest, `{"name":"another-package","bin":{"codex":"bin/codex"}}`)
	if _, err := agentCLIUpdateCommand("codex", entry); !errors.Is(err, ErrAgentUpdateUnsupported) {
		t.Fatalf("foreign package: %v", err)
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := agentCLIUpdateCommand("codex", entry); !errors.Is(err, ErrAgentUpdateUnsupported) {
		t.Fatalf("missing package: %v", err)
	}
}

func TestAgentCLIUpdateStandaloneCodexBindsInstalledHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installation layout")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "task-isolated-home"))
	t.Setenv("CODEX_MANAGED_BY_NPM", "1")
	t.Setenv("CODEX_INSTALL_DAEMON_ONLY", "1")
	t.Setenv("CODEX_INSTALL_DIR", filepath.Join(home, "wrong-bin"))
	codexHome := filepath.Join(home, ".codex")
	standalone := filepath.Join(codexHome, "packages", "standalone")
	release := filepath.Join(standalone, "releases", "0.156.1-x86_64-unknown-linux-musl")
	path := updateFixture(t, filepath.Join(release, "bin", "codex"), "#!/bin/sh\nexit 0\n")
	manifest := updateFixture(t, filepath.Join(release, "codex-package.json"), `{"layoutVersion":1,"version":"0.156.1","target":"x86_64-unknown-linux-musl","variant":"codex","entrypoint":"bin/codex"}`)
	current := filepath.Join(standalone, "current")
	if err := os.Symlink(release, current); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(binDir, "codex")
	if err := os.Symlink(filepath.Join(current, "bin", "codex"), launcher); err != nil {
		t.Fatal(err)
	}
	entry := AgentEntry{Path: launcher, Command: "codex"}
	cmd, err := agentCLIUpdateCommand("codex", entry)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cmd.Args, []string{path, "update"}) {
		t.Fatal(cmd.Args)
	}
	env := map[string]string{}
	for _, v := range cmd.Env {
		k, v, _ := strings.Cut(v, "=")
		env[k] = v
	}
	if env["CODEX_HOME"] != codexHome || env["CODEX_INSTALL_DIR"] != binDir || env["CODEX_NON_INTERACTIVE"] != "1" {
		t.Fatalf("updater target mismatch: %q %q", env["CODEX_HOME"], env["CODEX_INSTALL_DIR"])
	}
	if _, ok := env["CODEX_MANAGED_BY_NPM"]; ok {
		t.Fatal("inherited npm override")
	}
	if _, ok := env["CODEX_INSTALL_DAEMON_ONLY"]; ok {
		t.Fatal("inherited daemon-only override")
	}
	if err := updateAgentCLI(context.Background(), "codex", entry); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if _, err := agentCLIUpdateCommand("codex", AgentEntry{Path: path}); !errors.Is(err, ErrAgentUpdateUnsupported) {
		t.Fatalf("unselected release: %v", err)
	}
	nextRelease := filepath.Join(standalone, "releases", "0.157.0-x86_64-unknown-linux-musl")
	nextPath := updateFixture(t, filepath.Join(nextRelease, "bin", "codex"), "#!/bin/sh\nexit 0\n")
	updateFixture(t, filepath.Join(nextRelease, "codex-package.json"), `{"layoutVersion":1,"version":"0.157.0","target":"x86_64-unknown-linux-musl","variant":"codex","entrypoint":"bin/codex"}`)
	if err := os.Symlink(nextRelease, current); err != nil {
		t.Fatal(err)
	}
	// The old release is deliberately retained; executable existence alone must
	// not keep launches pinned to it after the updater selects a newer release.
	if got, err := updatedAgentCLIPath("codex", AgentEntry{Path: path, Command: path}); err != nil || got != nextPath {
		t.Fatalf("selected native path = %q, %v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := updatedAgentCLIPath("codex", AgentEntry{Path: path, Command: path}); err != nil || got != nextPath {
		t.Fatalf("removed native path = %q, %v", got, err)
	}
	updateFixture(t, path, "#!/bin/sh\nexit 0\n")
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(release, current); err != nil {
		t.Fatal(err)
	}
	updateFixture(t, manifest, `{"layoutVersion":99}`)
	if _, err := agentCLIUpdateCommand("codex", entry); !errors.Is(err, ErrAgentUpdateUnsupported) {
		t.Fatalf("invalid standalone receipt: %v", err)
	}
}

func TestAgentCLIUpdatedPathStaysInOriginalInstallation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installation layouts")
	}
	for _, kind := range []string{"npm", "brew", "claude-native"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", t.TempDir())
			provider, oldPath, newPath, launcher := "codex", "", "", ""
			switch kind {
			case "npm":
				prefix, entry := updateNPMFixture(t, "codex", "@openai/codex")
				oldPath, _ = filepath.EvalSymlinks(entry.Path)
				launcher = entry.Path
				newPath = updateFixture(t, filepath.Join(filepath.Dir(oldPath), "codex-new"), "#!/bin/sh\nexit 0\n")
				updateFixture(t, filepath.Join(prefix, "lib", "node_modules", "@openai", "codex", "package.json"), `{"name":"@openai/codex","bin":{"codex":"bin/codex-new"}}`)
			case "brew":
				updateFixture(t, filepath.Join(home, "bin", "brew"), "#!/bin/sh\nexit 0\n")
				updateFixture(t, filepath.Join(home, "Caskroom", "codex", ".metadata", "receipt"), "{}")
				oldPath = updateFixture(t, filepath.Join(home, "Caskroom", "codex", "1.0", "codex"), "#!/bin/sh\nexit 0\n")
				newPath = updateFixture(t, filepath.Join(home, "Caskroom", "codex", "2.0", "codex"), "#!/bin/sh\nexit 0\n")
				launcher = filepath.Join(home, "bin", "codex")
			case "claude-native":
				provider = "claude"
				oldPath = updateFixture(t, filepath.Join(home, ".local", "share", "claude", "versions", "1.0"), "#!/bin/sh\nexit 0\n")
				newPath = updateFixture(t, filepath.Join(home, ".local", "share", "claude", "versions", "2.0"), "#!/bin/sh\nexit 0\n")
				launcher = filepath.Join(home, ".local", "bin", "claude")
			}
			if err := os.MkdirAll(filepath.Dir(launcher), 0755); err != nil {
				t.Fatal(err)
			}
			if kind == "npm" {
				if err := os.Remove(launcher); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(newPath, launcher); err != nil {
				t.Fatal(err)
			}
			entry := AgentEntry{Path: oldPath, Command: oldPath}
			if got, err := updatedAgentCLIPath(provider, entry); err != nil || got != newPath {
				t.Fatalf("retained old: %q, %v", got, err)
			}
			if err := os.Remove(oldPath); err != nil {
				t.Fatal(err)
			}
			if got, err := updatedAgentCLIPath(provider, entry); err != nil || got != newPath {
				t.Fatalf("removed old: %q, %v", got, err)
			}
			foreign := updateFixture(t, filepath.Join(t.TempDir(), provider), "#!/bin/sh\nexit 0\n")
			if err := os.Remove(launcher); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(foreign, launcher); err != nil {
				t.Fatal(err)
			}
			if _, err := updatedAgentCLIPath(provider, entry); !errors.Is(err, ErrAgentUpdateUnsupported) {
				t.Fatalf("foreign installation: %v", err)
			}
		})
	}
}
