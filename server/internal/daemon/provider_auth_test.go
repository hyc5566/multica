package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestProviderAuthLoginArgs(t *testing.T) {
	for provider, want := range map[string]string{
		"codex":  "login",
		"claude": "auth login",
	} {
		args, command := providerAuthLoginArgs(provider)
		if got := strings.Join(args, " "); got != want || command != provider+" "+want {
			t.Errorf("providerAuthLoginArgs(%q) = %q, %q; want %q", provider, got, command, want)
		}
	}
	if args, command := providerAuthLoginArgs("custom"); len(args) != 0 || command != "" {
		t.Fatalf("custom runtime unexpectedly selected login: %q, %q", args, command)
	}
}

func TestProviderAuthEnvironmentExcludesTaskSecrets(t *testing.T) {
	t.Setenv("MULTICA_TOKEN", "must-not-leak")
	t.Setenv("OPENAI_API_KEY", "must-not-leak-either")
	values := map[string]string{
		"HOME":                     "/home/test-user",
		"CODEX_HOME":               "/tmp/task-codex-home",
		"MULTICA_TASK_CONFIG_ROOT": "/tmp/task-config",
	}
	got := strings.Join(providerAuthEnvironment("codex", values), "\n")
	for _, want := range []string{"HOME=/home/test-user", "CODEX_HOME=/tmp/task-codex-home"} {
		if !strings.Contains(got, want) {
			t.Errorf("provider environment omitted %q", want)
		}
	}
	for _, secret := range []string{os.Getenv("MULTICA_TOKEN"), os.Getenv("OPENAI_API_KEY"), "MULTICA_TASK_CONFIG_ROOT"} {
		if strings.Contains(got, secret) {
			t.Errorf("provider environment leaked %q", secret)
		}
	}
}
