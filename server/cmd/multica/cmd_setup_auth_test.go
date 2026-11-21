package main

import (
	"github.com/multica-ai/multica/server/internal/cli"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupPreservesSettingsAndScopesCredentials(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same server", true: "different server"}[changed], func(t *testing.T) {
			redirectTestHome(t, t.TempDir())
			old := cli.CLIConfig{ServerURL: "https://old.example", Token: "mul_fixture", WorkspaceID: "workspace", WorkspacesRoot: "/tasks", DeviceName: "v6", MaxConcurrentTasks: 2}
			if err := cli.SaveCLIConfigForProfile(old, "v6"); err != nil {
				t.Fatal(err)
			}
			target := old.ServerURL
			if changed {
				target = "https://new.example"
			}
			if _, err := persistSelfHostConfigIfReachable(target, "https://app.example", "v6", old, func(string) serverProbeResult { return serverProbeResult{healthy: true} }); err != nil {
				t.Fatal(err)
			}
			got, err := cli.LoadCLIConfigForProfile("v6")
			if err != nil {
				t.Fatal(err)
			}
			if changed && (got.WorkspacesRoot != "" || got.DeviceName != "" || got.MaxConcurrentTasks != 0) {
				t.Fatal("different backend inherited the local profile")
			}
			if !changed && (got.WorkspacesRoot != old.WorkspacesRoot || got.DeviceName != old.DeviceName || got.MaxConcurrentTasks != 2) {
				t.Fatal("local settings lost")
			}
			if changed && (got.Token != "" || got.WorkspaceID != "") {
				t.Fatal("credentials leaked to a different server")
			}
			if !changed && (got.Token != old.Token || got.WorkspaceID != old.WorkspaceID) {
				t.Fatal("same-server credentials lost")
			}
		})
	}
}

func TestValidateSetupToken(t *testing.T) {
	for _, status := range []int{200, 401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/me" || r.Header.Get("Authorization") != "Bearer mul_fixture" {
					t.Error("unexpected validation request")
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"name":"test"}`))
			}))
			defer server.Close()
			reuse, err := validateSetupToken(cli.CLIConfig{ServerURL: server.URL, Token: "mul_fixture"})
			if reuse != (status == 200) {
				t.Errorf("reuse=%v", reuse)
			}
			if (err != nil) != (status != 200 && status != 401) {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	t.Run("untrusted TLS is not an expired token", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer server.Close()
		reuse, err := validateSetupToken(cli.CLIConfig{ServerURL: server.URL, Token: "mul_fixture"})
		if reuse || err == nil || !strings.Contains(err.Error(), "token retained") || isInvalidAuthentication(err) {
			t.Fatalf("reuse=%v err=%v", reuse, err)
		}
	})
	t.Run("no token requests login", func(t *testing.T) {
		reuse, err := validateSetupToken(cli.CLIConfig{})
		if reuse || err != nil {
			t.Fatalf("reuse=%v err=%v", reuse, err)
		}
	})
}

func TestAuthStatusDistinguishesServerFailureFromRejectedToken(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			redirectTestHome(t, t.TempDir())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			t.Setenv("MULTICA_SERVER_URL", server.URL)
			t.Setenv("MULTICA_TOKEN", "mul_fixture")
			stderr := captureStderr(t)
			defer stderr.restore()
			if err := runAuthStatus(testCmd(), nil); err != nil {
				t.Fatal(err)
			}
			out := stderr.read()
			isRejected := status == http.StatusUnauthorized
			if strings.Contains(out, "invalid or expired") != isRejected ||
				strings.Contains(out, "Could not verify the token") == isRejected {
				t.Fatalf("misleading verification output: %q", out)
			}
		})
	}
}
