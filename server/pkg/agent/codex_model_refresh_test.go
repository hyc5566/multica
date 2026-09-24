package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodexModelRefreshRecoversAndInvalidatesPerCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	dir := t.TempDir()
	payload := filepath.Join(dir, "catalog.json")
	fake := filepath.Join(dir, "codex")
	writeTestExecutable(t, fake, []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then echo 'codex-cli 0.156.0'; exit 0; fi
if [ "$1" != "debug" ] || [ "$2" != "models" ] || [ "$#" != 2 ]; then exit 99; fi
cat '`+payload+`'
`))
	cmd := Command{Path: fake}
	ctx := context.Background()
	first, err := ListModels(ctx, "codex", cmd)
	if err != nil || !first.Fallback {
		t.Fatalf("missing source must be fallback: %+v, %v", first, err)
	}
	write := func(id string) {
		t.Helper()
		if err := os.WriteFile(payload, []byte(`{"models":[{"slug":"`+id+`","display_name":"New release","visibility":"list","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high"}]},{"slug":"internal-hidden","visibility":"hide"}]}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("gpt-6-sol")
	recovered, err := ListModels(ctx, "codex", cmd)
	if err != nil || recovered.Fallback || len(recovered.Models) != 1 || recovered.Models[0].ID != "gpt-6-sol" || recovered.Models[0].Thinking == nil {
		t.Fatalf("failed discovery poisoned cache or new model was filtered: %+v, %v", recovered, err)
	}
	write("gpt-6-luna")
	cached, err := ListModels(ctx, "codex", cmd)
	if err != nil || cached.Models[0].ID != "gpt-6-sol" {
		t.Fatalf("ordinary lookup should use memo: %+v, %v", cached, err)
	}
	InvalidateModelCache("codex", Command{Path: filepath.Join(dir, "other")})
	cached, _ = ListModels(ctx, "codex", cmd)
	if cached.Models[0].ID != "gpt-6-sol" {
		t.Fatal("another command invalidated this catalog")
	}
	InvalidateModelCache("codex", cmd)
	refreshed, err := ListModels(ctx, "codex", cmd)
	if err != nil || refreshed.Fallback || len(refreshed.Models) != 1 || refreshed.Models[0].ID != "gpt-6-luna" {
		t.Fatalf("explicit refresh did not discover new release: %+v, %v", refreshed, err)
	}
}

func TestCodexBundledCatalogRemainsFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	fake := filepath.Join(t.TempDir(), "codex")
	writeTestExecutable(t, fake, []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then echo 'codex-cli 0.156.0'; exit 0; fi
if [ "$3" != "--bundled" ]; then exit 1; fi
echo '{"models":[{"slug":"bundled-only","visibility":"list"}]}'
`))
	got, err := ListModels(context.Background(), "codex", Command{Path: fake})
	if err != nil || !got.Fallback || len(got.Models) != 1 || got.Models[0].ID != "bundled-only" {
		t.Fatalf("bundle must stay non-authoritative: %+v %v", got, err)
	}
}
