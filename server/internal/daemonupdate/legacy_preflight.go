package daemonupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

type LegacyPreflightOptions struct{ InventoryPath, InventorySHA256, ManifestPath, StagedBundle, CompatibilityEvidencePath string }

var legacyMachineIdentity = MachineIdentitySHA256

// LegacyPreflight only inspects local state; it never stops a process or creates a gate.
func LegacyPreflight(ctx context.Context, options LegacyPreflightOptions) (bool, error) {
	v, err := ReadLegacyInventory(options.InventoryPath, options.InventorySHA256)
	if err != nil {
		return false, err
	}
	targetMachine, err := legacyMachineIdentity()
	if err != nil {
		return false, err
	}
	if targetMachine == v.ControllerMachineIDSHA256 {
		return false, errors.New("controller and target must be different machines")
	}
	if !filepath.IsAbs(options.ManifestPath) {
		return false, errors.New("absolute candidate manifest path required")
	}
	if !filepath.IsAbs(options.CompatibilityEvidencePath) {
		return false, errors.New("compatibility evidence required")
	}
	if hash, e := hashFile(options.CompatibilityEvidencePath); e != nil || hash != v.CompatibilityEvidenceSHA256 {
		return false, errors.New("compatibility evidence mismatch")
	}
	manifest, err := readApprovedManifest(options.ManifestPath, v.CandidateManifestSHA256)
	if err != nil {
		return false, err
	}
	comparison, err := compareVersion(manifest.Version, v.OldVersion)
	if err != nil || comparison <= 0 {
		return false, errors.New("candidate must be newer")
	}
	if !filepath.IsAbs(options.StagedBundle) || filepath.Dir(filepath.Clean(options.StagedBundle)) != filepath.Join(v.WorkDir, "staging") || !strings.HasSuffix(options.StagedBundle, ".app") {
		return false, errors.New("invalid staged App path")
	}
	for _, bundle := range []string{v.Bundle, options.StagedBundle} {
		info, e := os.Lstat(bundle)
		if e != nil || !info.IsDir() {
			return false, errors.New("untrusted App bundle path")
		}
	}
	oldReal, err := filepath.EvalSymlinks(v.Bundle)
	if err != nil {
		return false, err
	}
	stageReal, err := filepath.EvalSymlinks(filepath.Join(v.WorkDir, "staging"))
	if err != nil {
		return false, err
	}
	for _, input := range []string{options.InventoryPath, options.ManifestPath, options.CompatibilityEvidencePath} {
		clean, e := filepath.EvalSymlinks(input)
		if e != nil {
			return false, e
		}
		if strings.HasPrefix(clean, oldReal+string(os.PathSeparator)) || strings.HasPrefix(clean, stageReal+string(os.PathSeparator)) {
			return false, errors.New("approval input must survive App exchange")
		}
	}
	info, err := os.Lstat(v.WorkDir)
	if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
		return false, errors.New("untrusted update directory")
	}
	profileDir, err := cli.ProfileDir(v.Profile)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(filepath.Join(profileDir, "daemon-management.json")); err == nil || !os.IsNotExist(err) {
		return false, errors.New("independent daemon requires separate procedure")
	}
	for _, path := range []string{filepath.Join(v.WorkDir, "ledger.json"), filepath.Join(v.WorkDir, "journal.json"), filepath.Join(profileDir, "daemon.maintenance")} {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			return false, errors.New("existing update state requires review")
		}
	}
	inspect := macInspector{}
	for _, bundle := range []string{v.Bundle, options.StagedBundle} {
		id, e := inspect.BundleValue(ctx, bundle, "CFBundleIdentifier")
		if e != nil || id != taiwanBundleID {
			return false, errors.New("App bundle ID mismatch")
		}
		if inspect.VerifySignature(ctx, bundle) != nil {
			return false, errors.New("App signature verification failed")
		}
	}
	oldVersion, err := inspect.BundleValue(ctx, v.Bundle, "CFBundleShortVersionString")
	if err != nil || oldVersion != v.OldVersion {
		return false, errors.New("legacy App version mismatch")
	}
	newVersion, err := inspect.BundleValue(ctx, options.StagedBundle, "CFBundleShortVersionString")
	if err != nil || newVersion != manifest.Version {
		return false, errors.New("candidate App version mismatch")
	}
	if err := verifyBundleHash(v.Bundle, v.OldBinarySHA256); err != nil {
		return false, err
	}
	asset := manifest.Desktop["darwin-arm64"]
	if asset.BinarySHA256 == v.OldBinarySHA256 {
		return false, errors.New("old and candidate binary hashes are identical")
	}
	if err := verifyBundleHash(options.StagedBundle, asset.BinarySHA256); err != nil {
		return false, err
	}
	binaryVersion, commit, compatibility, err := inspect.BinaryVersion(ctx, bundleCLI(options.StagedBundle))
	if err != nil || binaryVersion != manifest.Version || commit != manifest.Commit || compatibility != manifest.DataCompatibility {
		return false, errors.New("candidate binary identity mismatch")
	}
	if err := verifyCLIEntry(v.Bundle, v.CLIEntry, v.WorkDir, currentPathDirs()); err != nil {
		return false, err
	}
	pids, err := legacyAppPIDs(v.Bundle)
	if err != nil {
		return false, err
	}
	if err := verifyLegacyProcesses(v, pids, InspectLegacyProcess); err != nil {
		return false, err
	}
	data, err := os.ReadFile(filepath.Join(profileDir, "daemon.port"))
	if err != nil {
		return false, err
	}
	var port struct {
		PID  int `json:"pid"`
		Port int `json:"port"`
	}
	if json.Unmarshal(data, &port) != nil || port.PID != v.Daemon.PID || port.Port < 1 || port.Port > 65535 {
		return false, errors.New("legacy daemon endpoint mismatch")
	}
	a := newLocalAdapter(filepath.Join(v.WorkDir, "journal.json"))
	endpoint, err := a.endpoint(&Journal{HealthPort: port.Port}, "/health")
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, errors.New("legacy daemon health unavailable")
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return false, errors.New("invalid legacy daemon health")
	}
	return validateLegacyHealth(data, v)
}
