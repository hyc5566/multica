package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestZhTWVersionComparison(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.6.1-zh-tw.2", "0.6.1-zh-tw.1", true},
		{"0.6.1-zh-tw.10", "0.6.1-zh-tw.9", true},
		{"0.7.0-zh-tw.1", "0.6.9-zh-tw.12", true},
		{"0.6.1-zh-tw.1", "0.6.1-zh-tw.1", false},
		{"0.6.1-zh-tw.1", "0.6.1-zh-tw.2", false},
		{"0.6.1-zh-tw.2", "0.6.0-131-g2ca7196c3b", false},
		{"0.6.1", "0.6.1-zh-tw.1", false},
	}
	for _, tc := range cases {
		if got := IsNewerZhTWVersion(tc.latest, tc.current); got != tc.want {
			t.Errorf("IsNewerZhTWVersion(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
	if !IsNewerVersion("0.6.1-zh-tw.2", "0.6.1-zh-tw.1") {
		t.Fatal("IsNewerVersion must compare the zh-tw release number")
	}
	if !IsReleaseVersion("v0.6.1-zh-tw.3") || IsReleaseVersion("0.6.0-131-g2ca7196c3b") {
		t.Fatal("IsReleaseVersion must accept zh-tw releases and reject describe builds")
	}
}

func validManifest(installURL, sum string) string {
	return fmt.Sprintf(`{"version":"0.6.1-zh-tw.2","commit":"abc","cli":{"install_url":%q,"install_sha256":%q},
"desktop":{"darwin-arm64":{"url":"https://s90.example/a.zip","sha256":"%s","size":10}}}`, installURL, sum, strings.Repeat("a", 64))
}

func TestParseZhTWReleaseManifestRejectsUnsafeFields(t *testing.T) {
	sum := strings.Repeat("b", 64)
	if _, err := ParseZhTWReleaseManifest([]byte(validManifest("https://s90.example/install.sh", sum))); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	bad := []string{
		validManifest("http://s90.example/install.sh", sum),
		validManifest("https://s90.example/install.sh", "short"),
		strings.Replace(validManifest("https://s90.example/install.sh", sum), "0.6.1-zh-tw.2", "0.6.1", 1),
		strings.Replace(validManifest("https://s90.example/install.sh", sum), `"size":10`, `"size":0`, 1),
		`{"version":`,
	}
	for _, raw := range bad {
		if _, err := ParseZhTWReleaseManifest([]byte(raw)); err == nil {
			t.Errorf("manifest accepted: %s", raw)
		}
	}
}

func TestFetchLatestReleaseUsesZhTWManifest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(validManifest("https://s90.example/install.sh", strings.Repeat("c", 64))))
	}))
	defer server.Close()
	withTestTransport(t, server)
	t.Setenv(ZhTWReleaseManifestURLEnv, server.URL+"/downloads/latest.json")

	release, err := FetchLatestRelease()
	if err != nil {
		t.Fatalf("FetchLatestRelease: %v", err)
	}
	if release.TagName != "0.6.1-zh-tw.2" {
		t.Fatalf("tag = %q", release.TagName)
	}
}

func TestFetchZhTWManifestRefusesRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/latest.json", http.StatusFound)
	}))
	defer server.Close()
	withTestTransport(t, server)
	if _, err := FetchZhTWReleaseManifest(server.URL + "/latest.json"); err == nil {
		t.Fatal("redirected manifest was accepted")
	}
}

func TestUpdateViaZhTWInstallerVerifiesAndRunsUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("installer is a bash script")
	}
	marker := filepath.Join(t.TempDir(), "args")
	script := fmt.Sprintf("#!/usr/bin/env bash\nprintf '%%s' \"$*\" > %q\necho upgraded\n", marker)
	sum := sha256.Sum256([]byte(script))
	var manifest string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			_, _ = w.Write([]byte(manifest))
		case "/install.sh":
			_, _ = w.Write([]byte(script))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withTestTransport(t, server)
	t.Setenv(ZhTWReleaseManifestURLEnv, server.URL+"/latest.json")
	t.Setenv(ZhTWLauncherEnv, "/home/user/.local/bin/multica")

	manifest = validManifest(server.URL+"/install.sh", strings.Repeat("0", 64))
	if _, err := UpdateViaZhTWInstaller("0.6.1-zh-tw.2", 0); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum mismatch not rejected: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("installer ran despite checksum mismatch")
	}

	manifest = validManifest(server.URL+"/install.sh", hex.EncodeToString(sum[:]))
	if _, err := UpdateViaZhTWInstaller("0.6.1-zh-tw.9", 0); err == nil {
		t.Fatal("version not offered by the channel was accepted")
	}
	out, err := UpdateViaZhTWInstaller("0.6.1-zh-tw.2", 0)
	if err != nil {
		t.Fatalf("UpdateViaZhTWInstaller: %v (%s)", err, out)
	}
	args, err := os.ReadFile(marker)
	if err != nil || string(args) != "--upgrade" {
		t.Fatalf("installer args = %q, %v", args, err)
	}
}

func TestUpdateViaZhTWInstallerRequiresLauncher(t *testing.T) {
	t.Setenv(ZhTWReleaseManifestURLEnv, "https://s90.example/latest.json")
	t.Setenv(ZhTWLauncherEnv, "")
	if _, err := UpdateViaZhTWInstaller("0.6.1-zh-tw.2", 0); err == nil {
		t.Fatal("non-launcher installation was upgraded")
	}
}

// withTestTransport trusts the httptest server for the duration of the test,
// the same way ConfigureTLSFromEnv installs the s90 CA in production.
func withTestTransport(t *testing.T, server *httptest.Server) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}
