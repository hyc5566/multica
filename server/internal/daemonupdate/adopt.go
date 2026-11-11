package daemonupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

const taiwanBundleID = "ai.multica.desktop.zh-tw.local"

type AdoptionOptions struct {
	ManifestPath, ManifestSHA256, Bundle, CLIEntry, Profile, WorkDir string
	Wait                                                             time.Duration
}

type adoptionManifest struct {
	SourceID          string `json:"source_id"`
	Version           string `json:"version"`
	Commit            string `json:"commit"`
	DataCompatibility string `json:"data_compatibility"`
	Protocol          int    `json:"coordinated_update_protocol"`
	Desktop           map[string]struct {
		BundleID          string `json:"bundle_id"`
		BinarySHA256      string `json:"binary_sha256"`
		BinaryVersion     string `json:"binary_version"`
		BinaryCommit      string `json:"binary_commit"`
		DataCompatibility string `json:"data_compatibility"`
	} `json:"coordinated_desktop"`
}

type adoptionInspector interface {
	BundleValue(context.Context, string, string) (string, error)
	VerifySignature(context.Context, string) error
	BinaryVersion(context.Context, string) (string, string, string, error)
}

type macInspector struct{}

func (macInspector) BundleValue(ctx context.Context, bundle, key string) (string, error) {
	out, err := exec.CommandContext(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :"+key, filepath.Join(bundle, "Contents", "Info.plist")).Output()
	return strings.TrimSpace(string(out)), err
}
func (macInspector) VerifySignature(ctx context.Context, bundle string) error {
	return exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", bundle).Run()
}
func (macInspector) BinaryVersion(ctx context.Context, path string) (string, string, string, error) {
	out, err := exec.CommandContext(ctx, path, "version", "--output", "json").Output()
	if err != nil {
		return "", "", "", err
	}
	var info struct {
		Version       string `json:"version"`
		Commit        string `json:"build_commit"`
		Compatibility string `json:"data_compatibility"`
		Shutdown      string `json:"daemon_maintenance_shutdown"`
		Gate          string `json:"daemon_maintenance_gate"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", "", "", err
	}
	if info.Shutdown != "v1" || info.Gate != "v1" {
		return "", "", "", errors.New("coordinated shutdown or gate unsupported")
	}
	return info.Version, info.Commit, info.Compatibility, nil
}

type adoptionState struct {
	Version           int                 `json:"version"`
	ManifestSHA256    string              `json:"manifest_sha256"`
	Bundle            string              `json:"bundle"`
	CLIEntry          string              `json:"cli_entry"`
	Profile           string              `json:"profile"`
	DaemonID          string              `json:"daemon_id"`
	ServerURLSHA256   string              `json:"server_url_sha256"`
	PID               int                 `json:"pid"`
	StartedAt         string              `json:"started_at"`
	AdoptionStartedAt time.Time           `json:"adoption_started_at"`
	ReleaseVersion    string              `json:"release_version"`
	Commit            string              `json:"commit"`
	BinarySHA256      string              `json:"binary_sha256"`
	Workspaces        map[string][]string `json:"workspaces"`
	RuntimeIDs        []string            `json:"runtime_ids"`
	Last              map[string]uint64   `json:"last"`
	Observed          map[string]int      `json:"observed"`
}

func readApprovedManifest(path, expected string) (adoptionManifest, error) {
	var m adoptionManifest
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(expected) {
		return m, errors.New("manifest SHA-256 required")
	}
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return m, errors.New("invalid manifest size")
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != expected {
		return m, errors.New("manifest SHA-256 mismatch")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	asset, ok := m.Desktop["darwin-arm64"]
	if !ok || m.SourceID != "hyc5566/multica" || m.Protocol != 1 || m.DataCompatibility != "multica-profile-v1" ||
		asset.BundleID != taiwanBundleID || asset.BinaryVersion != m.Version || asset.BinaryCommit != m.Commit || asset.DataCompatibility != m.DataCompatibility ||
		!regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(m.Commit) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(asset.BinarySHA256) || !releaseVersion.MatchString(m.Version) {
		return m, errors.New("manifest does not authorize this coordinated release")
	}
	return m, nil
}

func verifyAdoptionInputs(ctx context.Context, options AdoptionOptions, inspect adoptionInspector) (adoptionManifest, error) {
	m, err := readApprovedManifest(options.ManifestPath, options.ManifestSHA256)
	if err != nil {
		return m, err
	}
	if !filepath.IsAbs(options.Bundle) || !strings.HasSuffix(options.Bundle, ".app") || !filepath.IsAbs(options.CLIEntry) ||
		!strings.HasPrefix(options.Profile, "desktop-") || filepath.Base(options.Profile) != options.Profile || strings.ContainsAny(options.Profile, `/\\`) ||
		!filepath.IsAbs(options.WorkDir) || filepath.Base(options.WorkDir) != "zh-tw-update" {
		return m, errors.New("invalid App, profile or work directory")
	}
	dir, err := cli.ProfileDir(options.Profile)
	if err != nil {
		return m, err
	}
	if _, err := os.Lstat(filepath.Join(dir, "daemon-management.json")); err == nil || !os.IsNotExist(err) {
		return m, errors.New("independent daemon marker requires separate operator procedure")
	}
	asset := m.Desktop["darwin-arm64"]
	id, err := inspect.BundleValue(ctx, options.Bundle, "CFBundleIdentifier")
	if err != nil || id != asset.BundleID {
		return m, errors.New("App bundle ID mismatch")
	}
	version, err := inspect.BundleValue(ctx, options.Bundle, "CFBundleShortVersionString")
	if err != nil || version != m.Version {
		return m, errors.New("App version mismatch")
	}
	if err := inspect.VerifySignature(ctx, options.Bundle); err != nil {
		return m, errors.New("App signature verification failed")
	}
	binary := bundleCLI(options.Bundle)
	if err := verifyCLIEntry(options.Bundle, options.CLIEntry, options.WorkDir, currentPathDirs()); err != nil {
		return m, err
	}
	hash, err := hashFile(binary)
	if err != nil || hash != asset.BinarySHA256 {
		return m, errors.New("embedded CLI hash mismatch")
	}
	binaryVersion, commit, compatibility, err := inspect.BinaryVersion(ctx, binary)
	if err != nil || binaryVersion != m.Version || commit != m.Commit || compatibility != m.DataCompatibility {
		return m, errors.New("embedded CLI version or compatibility mismatch")
	}
	return m, nil
}

// Adopt records no baseline until three observed heartbeat advances per
// original runtime and a later, server-confirmed task receipt are both seen.
func Adopt(ctx context.Context, options AdoptionOptions) (bool, error) {
	return adopt(ctx, options, macInspector{}, time.Second)
}

func adopt(ctx context.Context, options AdoptionOptions, inspect adoptionInspector, poll time.Duration) (bool, error) {
	manifest, err := verifyAdoptionInputs(ctx, options, inspect)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(options.WorkDir)
	if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
		return false, errors.New("untrusted update directory")
	}
	unlock, err := lockJournal(filepath.Join(options.WorkDir, "journal.lock"))
	if err != nil {
		return false, err
	}
	defer unlock()
	ledgerPath := filepath.Join(options.WorkDir, "ledger.json")
	existingMatch := false
	if info, err := os.Lstat(ledgerPath); err == nil {
		if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Size() > 65536 {
			return false, errors.New("untrusted ledger")
		}
		data, readErr := os.ReadFile(ledgerPath)
		if readErr != nil {
			return false, readErr
		}
		var existing struct {
			Compatibility string `json:"data_compatibility"`
			CLIEntry      string `json:"cli_entry"`
			Current       struct {
				Version      string `json:"version"`
				Commit       string `json:"commit"`
				BinarySHA256 string `json:"binary_sha256"`
			} `json:"current"`
		}
		if json.Unmarshal(data, &existing) == nil && existing.Compatibility == manifest.DataCompatibility && existing.CLIEntry == options.CLIEntry && existing.Current.Version == manifest.Version && existing.Current.Commit == manifest.Commit && existing.Current.BinarySHA256 == manifest.Desktop["darwin-arm64"].BinarySHA256 {
			existingMatch = true
		} else {
			return false, errors.New("ledger already exists with another baseline; adoption will not overwrite it")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	journalPath := filepath.Join(options.WorkDir, "journal.json")
	if info, err := os.Lstat(journalPath); err == nil {
		if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Size() > 1<<20 {
			return false, errors.New("untrusted update journal")
		}
		data, readErr := os.ReadFile(journalPath)
		if readErr != nil {
			return false, readErr
		}
		var journal struct {
			Phase string `json:"phase"`
		}
		if json.Unmarshal(data, &journal) != nil || (journal.Phase != "complete" && journal.Phase != "restored" && journal.Phase != "no_change") {
			return false, errors.New("update journal is not final")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	profileDir, err := cli.ProfileDir(options.Profile)
	if err != nil {
		return false, err
	}
	portData, err := os.ReadFile(filepath.Join(profileDir, "daemon.port"))
	if err != nil {
		return false, err
	}
	var port struct {
		Port int `json:"port"`
		PID  int `json:"pid"`
	}
	if json.Unmarshal(portData, &port) != nil || port.PID <= 0 {
		return false, errors.New("invalid daemon endpoint")
	}
	a := newLocalAdapter(filepath.Join(options.WorkDir, "adoption.json"))
	j := &Journal{Profile: options.Profile, HealthPort: port.Port}
	h, err := a.readHealth(ctx, j)
	if err != nil {
		return false, err
	}
	asset := manifest.Desktop["darwin-arm64"]
	if h.PID != port.PID || h.Status != "running" || h.OS != "darwin" || h.LaunchedBy != "desktop" || h.LifecycleManager != "" || h.Profile != options.Profile || h.DaemonID == "" || h.ServerURL == "" ||
		h.Version != manifest.Version || h.Commit != manifest.Commit || h.Attestation.BinarySHA256 != asset.BinarySHA256 || h.Attestation.ProcessStartedAt == "" ||
		!h.MaintenanceShutdown || !h.MaintenanceGate.Supported || h.MaintenanceGate.Blocked || h.MaintenanceGate.Path != filepath.Join(profileDir, "daemon.maintenance") {
		return false, errors.New("running daemon is not the approved coordinated App daemon")
	}
	if existingMatch {
		return true, nil
	}
	workspaces, err := workspaceSet(h.Workspaces)
	if err != nil {
		return false, err
	}
	var runtimeIDs []string
	for _, ids := range workspaces {
		runtimeIDs = append(runtimeIDs, ids...)
	}
	sort.Strings(runtimeIDs)
	if len(runtimeIDs) == 0 {
		return false, errors.New("no runtime available for adoption")
	}
	observer, err := newHeartbeatObserver(runtimeIDs, h.Attestation.RuntimeHeartbeatAcks)
	if err != nil {
		return false, err
	}
	statePath := filepath.Join(options.WorkDir, "adoption.json")
	state := adoptionState{Version: 1, ManifestSHA256: options.ManifestSHA256, Bundle: options.Bundle, CLIEntry: options.CLIEntry, Profile: options.Profile, DaemonID: h.DaemonID, ServerURLSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(h.ServerURL))), PID: h.PID, StartedAt: h.Attestation.ProcessStartedAt,
		AdoptionStartedAt: time.Now().UTC(), ReleaseVersion: manifest.Version, Commit: manifest.Commit, BinarySHA256: asset.BinarySHA256, Workspaces: workspaces, RuntimeIDs: runtimeIDs, Last: observer.last, Observed: observer.observed}
	if info, err := os.Lstat(statePath); err == nil {
		if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Size() > 1<<20 {
			return false, errors.New("untrusted adoption state")
		}
		data, readErr := os.ReadFile(statePath)
		if readErr != nil {
			return false, readErr
		}
		var previous adoptionState
		if json.Unmarshal(data, &previous) != nil || previous.Version != 1 || previous.ManifestSHA256 != state.ManifestSHA256 || previous.Bundle != state.Bundle || previous.CLIEntry != state.CLIEntry || previous.Profile != state.Profile || previous.DaemonID != state.DaemonID || previous.ServerURLSHA256 != state.ServerURLSHA256 || previous.PID != state.PID || previous.StartedAt != state.StartedAt || previous.ReleaseVersion != state.ReleaseVersion || previous.Commit != state.Commit || previous.BinarySHA256 != state.BinarySHA256 {
			return false, errors.New("adoption identity changed; operator review required")
		}
		if previous.AdoptionStartedAt.IsZero() || previous.AdoptionStartedAt.After(time.Now().UTC()) {
			return false, errors.New("invalid adoption start time")
		}
		state = previous
	} else if !os.IsNotExist(err) {
		return false, err
	} else if err := saveJSON(statePath, state); err != nil {
		return false, err
	}
	j = &Journal{Profile: state.Profile, DaemonID: state.DaemonID, ServerURLSHA256: state.ServerURLSHA256, NewVersion: state.ReleaseVersion, NewCommit: state.Commit, NewBinarySHA256: state.BinarySHA256, Workspaces: state.Workspaces, RuntimeIDs: state.RuntimeIDs}
	// A resumed invocation samples a fresh baseline: persisted counters alone
	// can never attest the required three newly observed server acknowledgements.
	observer, err = newHeartbeatObserver(state.RuntimeIDs, h.Attestation.RuntimeHeartbeatAcks)
	if err != nil {
		return false, err
	}
	deadline := time.NewTimer(options.Wait)
	defer deadline.Stop()
	for {
		if err := validateNewHealth(j, h, state.PID, state.StartedAt); err != nil {
			return false, err
		}
		if !h.MaintenanceGate.Supported || h.MaintenanceGate.Blocked || h.MaintenanceGate.Path != filepath.Join(profileDir, "daemon.maintenance") {
			return false, errors.New("daemon gate changed during adoption")
		}
		advanced, err := observer.observe(h.Attestation.RuntimeHeartbeatAcks)
		if err != nil {
			return false, err
		}
		state.Last, state.Observed = observer.last, observer.observed
		if err := saveJSON(statePath, state); err != nil {
			return false, err
		}
		if advanced {
			for _, receipt := range h.Attestation.TaskReceipts {
				for _, id := range state.RuntimeIDs {
					if receipt.TaskID != "" && receipt.RuntimeID == id && receipt.CompletedAt.After(state.AdoptionStartedAt) && !receipt.AckAt.Before(receipt.CompletedAt) {
						if _, err := verifyAdoptionInputs(ctx, options, inspect); err != nil {
							return false, err
						}
						ledger := map[string]any{"data_compatibility": manifest.DataCompatibility, "cli_entry": options.CLIEntry, "current": map[string]string{"version": manifest.Version, "commit": manifest.Commit, "binary_sha256": asset.BinarySHA256}, "previous": map[string]string{}}
						if err := saveJSON(ledgerPath, ledger); err != nil {
							return false, err
						}
						return true, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil
		case <-time.After(poll):
		}
		portData, err = os.ReadFile(filepath.Join(profileDir, "daemon.port"))
		if err != nil || json.Unmarshal(portData, &port) != nil || port.PID != state.PID {
			return false, errors.New("daemon endpoint changed during adoption")
		}
		j.HealthPort = port.Port
		h, err = a.readHealth(ctx, j)
		if err != nil {
			return false, err
		}
	}
}
