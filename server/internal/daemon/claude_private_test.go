package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClaudeDefaultUsesPrivateHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixtures")
	}
	for _, scenario := range []string{"private", "missing", "escaped"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("MULTICA_CLAUDE_PATH", "")
			t.Setenv("SHELL", filepath.Join(home, "fish"))
			shared := t.TempDir()
			sharedCLI := filepath.Join(shared, "claude")
			if err := os.WriteFile(sharedCLI, []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", shared)
			private := filepath.Join(home, ".local", "bin", "claude")
			if err := os.MkdirAll(filepath.Dir(private), 0755); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "private":
				if err := os.WriteFile(private, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
			case "escaped":
				if err := os.Symlink(sharedCLI, private); err != nil {
					t.Fatal(err)
				}
			}
			entry, found := probeAgentCLIs()["claude"]
			if scenario == "private" {
				if !found || entry.Path != private || entry.Command != private {
					t.Fatalf("private discovery: %#v, found=%v", entry, found)
				}
				if path, ok := reresolveAgentCommand(entry.Command); !ok || path != private {
					t.Fatalf("rediscovery = %q, %v", path, ok)
				}
			} else {
				if found {
					t.Fatalf("must not discover shared CLI: %#v", entry)
				}
				if path, ok := reresolveAgentCommand(private); ok {
					t.Fatalf("rediscovered invalid private CLI: %s", path)
				}
			}
		})
	}
}
