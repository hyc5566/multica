package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The Taiwan build publishes its own release channel instead of the upstream
// GitHub releases: one small manifest names the current version and the
// checksum-pinned installer that upgrades a CLI installation in place. The
// generated launcher exports these variables, so source builds and upstream
// installs never see this path.
const (
	ZhTWReleaseManifestURLEnv = "MULTICA_ZH_TW_RELEASE_MANIFEST_URL"
	ZhTWLauncherEnv           = "MULTICA_ZH_TW_LAUNCHER"
	zhTWMaxManifestBytes      = 64 * 1024
	zhTWMaxInstallerBytes     = 1024 * 1024
	zhTWInstallerTimeout      = 10 * time.Minute
)

var zhTWVersionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)-zh-tw\.(\d+)$`)

// ZhTWReleaseManifest is the channel document published next to each release.
type ZhTWReleaseManifest struct {
	Version     string                      `json:"version"`
	Commit      string                      `json:"commit"`
	PublishedAt string                      `json:"published_at"`
	NotesURL    string                      `json:"notes_url"`
	CLI         ZhTWReleaseInstaller        `json:"cli"`
	Desktop     map[string]ZhTWReleaseAsset `json:"desktop"`
}

// ZhTWReleaseInstaller pins the release installer by URL and SHA-256.
type ZhTWReleaseInstaller struct {
	InstallURL    string `json:"install_url"`
	InstallSHA256 string `json:"install_sha256"`
}

// ZhTWReleaseAsset describes one Desktop download. The CLI does not use it;
// it is parsed so a malformed manifest fails here too.
type ZhTWReleaseAsset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ZhTWReleaseManifestURL returns the configured channel, or "" when this
// installation does not follow the Taiwan release channel.
func ZhTWReleaseManifestURL() string {
	return strings.TrimSpace(os.Getenv(ZhTWReleaseManifestURLEnv))
}

// IsZhTWReleaseVersion reports whether v is a Taiwan release such as
// "0.6.1-zh-tw.2".
func IsZhTWReleaseVersion(v string) bool {
	_, ok := parseZhTWVersion(v)
	return ok
}

// IsNewerZhTWVersion compares two Taiwan release versions. Anything else,
// including git-describe builds, never counts as newer or older.
func IsNewerZhTWVersion(latest, current string) bool {
	l, ok := parseZhTWVersion(latest)
	if !ok {
		return false
	}
	c, ok := parseZhTWVersion(current)
	if !ok {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseZhTWVersion(v string) ([4]int, bool) {
	m := zhTWVersionPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [4]int{}, false
	}
	var out [4]int
	for i := range out {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [4]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func requireHTTPSURL(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("%s must be an https URL", field)
	}
	return nil
}

// ParseZhTWReleaseManifest validates the channel document strictly: the CLI
// executes the installer it names, so every field it relies on must be sound.
func ParseZhTWReleaseManifest(data []byte) (*ZhTWReleaseManifest, error) {
	if len(data) > zhTWMaxManifestBytes {
		return nil, fmt.Errorf("release manifest too large")
	}
	var m ZhTWReleaseManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse release manifest: %w", err)
	}
	if !IsZhTWReleaseVersion(m.Version) {
		return nil, fmt.Errorf("release manifest version %q is not a zh-tw release", m.Version)
	}
	if err := requireHTTPSURL(m.CLI.InstallURL, "cli.install_url"); err != nil {
		return nil, err
	}
	m.CLI.InstallSHA256 = strings.ToLower(m.CLI.InstallSHA256)
	if !isSHA256Hex(m.CLI.InstallSHA256) {
		return nil, fmt.Errorf("cli.install_sha256 must be a SHA-256 hex digest")
	}
	for platform, asset := range m.Desktop {
		if err := requireHTTPSURL(asset.URL, "desktop."+platform+".url"); err != nil {
			return nil, err
		}
		if !isSHA256Hex(strings.ToLower(asset.SHA256)) || asset.Size <= 0 {
			return nil, fmt.Errorf("desktop.%s needs a SHA-256 digest and size", platform)
		}
	}
	return &m, nil
}

func fetchLimited(rawURL string, limit int64, timeout time.Duration) ([]byte, error) {
	if err := requireHTTPSURL(rawURL, "download URL"); err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: timeout,
		// Stay on the origin the channel named; never follow trust elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", rawURL, limit)
	}
	return data, nil
}

// FetchZhTWReleaseManifest downloads and validates the configured channel.
func FetchZhTWReleaseManifest(manifestURL string) (*ZhTWReleaseManifest, error) {
	data, err := fetchLimited(manifestURL, zhTWMaxManifestBytes, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("fetch release manifest: %w", err)
	}
	return ParseZhTWReleaseManifest(data)
}

// UpdateViaZhTWInstaller upgrades a launcher-managed CLI installation by
// running the release's own checksum-pinned installer with --upgrade. The
// installer swaps the launcher atomically; the running daemon keeps its old
// binary until it restarts through the launcher.
func UpdateViaZhTWInstaller(targetVersion string, timeout time.Duration) (string, error) {
	manifestURL := ZhTWReleaseManifestURL()
	if manifestURL == "" {
		return "", fmt.Errorf("%s is not set", ZhTWReleaseManifestURLEnv)
	}
	if os.Getenv(ZhTWLauncherEnv) == "" {
		return "", fmt.Errorf("not a launcher-managed installation; rerun the installer")
	}
	manifest, err := FetchZhTWReleaseManifest(manifestURL)
	if err != nil {
		return "", err
	}
	if strings.TrimPrefix(targetVersion, "v") != manifest.Version {
		return "", fmt.Errorf("requested %s but the release channel offers %s", targetVersion, manifest.Version)
	}
	script, err := fetchLimited(manifest.CLI.InstallURL, zhTWMaxInstallerBytes, updateDownloadTimeoutOrDefault(timeout))
	if err != nil {
		return "", fmt.Errorf("download installer: %w", err)
	}
	sum := sha256.Sum256(script)
	if hex.EncodeToString(sum[:]) != manifest.CLI.InstallSHA256 {
		return "", fmt.Errorf("installer checksum mismatch; nothing was installed")
	}
	dir, err := os.MkdirTemp("", "multica-zh-tw-upgrade-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, script, 0o700); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), zhTWInstallerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", path, "--upgrade")
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("installer failed: %w", err)
	}
	return string(out), nil
}
