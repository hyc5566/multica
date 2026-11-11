package daemonupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

type fixtureInspector struct{ version, commit string }

func (f fixtureInspector) BundleValue(_ context.Context, _ string, key string) (string, error) {
	if key == "CFBundleIdentifier" {
		return taiwanBundleID, nil
	}
	return f.version, nil
}
func (fixtureInspector) VerifySignature(context.Context, string) error { return nil }
func (f fixtureInspector) BinaryVersion(context.Context, string) (string, string, string, error) {
	return f.version, f.commit, "multica-profile-v1", nil
}

func TestAdoptionRequiresObservedHeartbeatsAndNewReceipt(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	pathDir := filepath.Join(root, "path")
	if err := os.MkdirAll(pathDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	profile := filepath.Join(root, "profiles", "desktop-test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "userdata", "zh-tw-update")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "Multica.app")
	hash := fixtureBundle(t, bundle, "fixture cli")
	version, commit := "0.8.1-zh-tw.4", strings.Repeat("a", 40)
	manifest, err := json.Marshal(map[string]any{"source_id": "hyc5566/multica", "version": version, "commit": commit, "data_compatibility": "multica-profile-v1", "coordinated_update_protocol": 1,
		"coordinated_desktop": map[string]any{"darwin-arm64": map[string]any{"bundle_id": taiwanBundleID, "binary_sha256": hash, "binary_version": version, "binary_commit": commit, "data_compatibility": "multica-profile-v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "approved.json")
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	manifestHash := fmt.Sprintf("%x", sha256.Sum256(manifest))
	gate := filepath.Join(profile, "daemon.maintenance")
	var beats atomic.Uint64
	var receipt atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		receipts := []any{}
		if receipt.Load() {
			receipts = append(receipts, map[string]any{"task_id": "task", "runtime_id": "r", "completed_at": time.Now().UTC(), "ack_at": time.Now().UTC().Add(time.Second)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": 123, "os": "darwin", "profile": "desktop-test", "daemon_id": "daemon", "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": version, "cli_commit": commit, "maintenance_shutdown": true,
			"maintenance_gate": map[string]any{"supported": true, "blocked": false, "path": gate}, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}},
			"update_attestation": map[string]any{"binary_sha256": hash, "process_started_at": "start", "runtime_heartbeat_acks": map[string]uint64{"r": beats.Add(1)}, "task_receipts": receipts}})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, portText, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portText)
	if err := os.WriteFile(filepath.Join(profile, "daemon.port"), []byte(fmt.Sprintf(`{"pid":123,"port":%d}`, port)), 0600); err != nil {
		t.Fatal(err)
	}
	options := AdoptionOptions{ManifestPath: manifestPath, ManifestSHA256: manifestHash, Bundle: bundle, CLIEntry: bundleCLI(bundle), Profile: "desktop-test", WorkDir: work, Wait: 50 * time.Millisecond}
	inspect := fixtureInspector{version, commit}
	if done, err := adopt(context.Background(), options, inspect, 2*time.Millisecond); err != nil || done {
		t.Fatalf("without receipt: %v %v", done, err)
	}
	ledgerPath := filepath.Join(work, "ledger.json")
	if _, err := os.Stat(ledgerPath); !os.IsNotExist(err) {
		t.Fatal("ledger created without receipt")
	}
	receipt.Store(true)
	options.Wait = 2 * time.Second
	if done, err := adopt(context.Background(), options, inspect, 2*time.Millisecond); err != nil || !done {
		t.Fatalf("accepted adoption: %v %v", done, err)
	}
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(ledger, &value); err != nil {
		t.Fatal(err)
	}
	value["previous"] = map[string]string{"version": "keep"}
	ledger, _ = json.Marshal(value)
	if err := os.WriteFile(ledgerPath, ledger, 0600); err != nil {
		t.Fatal(err)
	}
	if done, err := adopt(context.Background(), options, inspect, 2*time.Millisecond); err != nil || !done {
		t.Fatalf("same-version adoption: %v %v", done, err)
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil || string(after) != string(ledger) {
		t.Fatal("no-op changed previous ledger")
	}
	if err := os.WriteFile(filepath.Join(pathDir, "multica"), []byte("standalone"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := adopt(context.Background(), options, inspect, 2*time.Millisecond); err == nil {
		t.Fatal("mixed PATH accepted")
	}
}
