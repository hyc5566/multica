package main

import (
	"github.com/multica-ai/multica/server/internal/cli"
	"reflect"
	"testing"
)

func TestTaiwanSetupDefaultsAndExistingURLs(t *testing.T) {
	t.Setenv("MULTICA_SERVER_URL", "")
	t.Setenv("MULTICA_APP_URL", "")
	if reflect.ValueOf(setupCmd.RunE).Pointer() != reflect.ValueOf(runSetupSelfHost).Pointer() {
		t.Fatal("plain setup must not dispatch to Cloud reset")
	}
	server, _ := resolveSelfHostServerURL(setupCmd, cli.CLIConfig{})
	app := resolveSelfHostAppURL(setupCmd, cli.CLIConfig{})
	if server != "https://10.1.24.90:45671" || app != server {
		t.Fatalf("unexpected defaults: %s %s", server, app)
	}
	existing := cli.CLIConfig{ServerURL: "https://existing.example", AppURL: "https://app.example", Token: "test-token"}
	server, _ = resolveSelfHostServerURL(setupCmd, existing)
	if server != existing.ServerURL || resolveSelfHostAppURL(setupCmd, existing) != existing.AppURL {
		t.Fatal("existing URLs must be retained")
	}
	t.Setenv("MULTICA_SERVER_URL", "https://override.example")
	t.Setenv("MULTICA_APP_URL", "https://override-app.example")
	server, _ = resolveSelfHostServerURL(setupCmd, existing)
	if server != "https://override.example" || resolveSelfHostAppURL(setupCmd, existing) != "https://override-app.example" {
		t.Fatal("explicit overrides ignored")
	}
}

func TestTaiwanTokenLoginDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", "")
	if got := resolveLoginTokenServerURL(testCmd()); got != "https://10.1.24.90:45671" {
		t.Fatalf("got %s", got)
	}
}
