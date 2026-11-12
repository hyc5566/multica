package daemonupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
)

func BeginLegacy(ctx context.Context, o LegacyPreflightOptions) (bool, error) {
	v, err := ReadLegacyInventory(o.InventoryPath, o.InventorySHA256)
	if err != nil {
		return false, err
	}
	deadline, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		idle, err := LegacyPreflight(deadline, o)
		if err != nil {
			return false, err
		}
		if idle {
			break
		}
		if err := pause(deadline, time.Second); err != nil {
			return false, err
		}
	}
	path := legacyJournalPath(v)
	unlock, err := lockJournal(filepath.Join(v.WorkDir, "journal.lock"))
	if err != nil {
		return false, err
	}
	defer unlock()
	idle, err := LegacyPreflight(deadline, o)
	if err != nil {
		return false, err
	}
	if !idle {
		return false, errors.New("legacy work resumed during preflight")
	}
	manifest, err := readApprovedManifest(o.ManifestPath, v.CandidateManifestSHA256)
	if err != nil {
		return false, err
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	if hash, e := hashFile(executable); e != nil || hash != manifest.Desktop["darwin-arm64"].BinarySHA256 {
		return false, errors.New("unverified legacy coordinator")
	}
	exe, err := (macInspector{}).BundleValue(deadline, o.StagedBundle, "CFBundleExecutable")
	if err != nil || exe == "" || exe == "." || exe == ".." || filepath.Base(exe) != exe {
		return false, errors.New("invalid candidate App executable")
	}
	if info, err := os.Stat(filepath.Join(o.StagedBundle, "Contents", "MacOS", exe)); err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return false, errors.New("candidate App executable unavailable")
	}
	profileDir, err := cli.ProfileDir(v.Profile)
	if err != nil {
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
		return false, errors.New("legacy daemon endpoint changed")
	}
	appPID := 0
	for _, p := range v.Apps {
		if filepath.Dir(p.ExecutablePath) == filepath.Join(v.Bundle, "Contents", "MacOS") {
			if appPID != 0 {
				return false, errors.New("multiple main App processes")
			}
			appPID = p.PID
		}
	}
	if appPID == 0 {
		return false, errors.New("main App process missing")
	}
	var runtimeIDs []string
	for _, ids := range v.Workspaces {
		runtimeIDs = append(runtimeIDs, ids...)
	}
	sort.Strings(runtimeIDs)
	token := uuid.NewString()
	asset := manifest.Desktop["darwin-arm64"]
	j := &Journal{Version: 1, Token: token, Phase: "prepared", Profile: v.Profile, CLIEntry: v.CLIEntry, DaemonID: v.DaemonID, ServerURLSHA256: v.ServerURLSHA256, DaemonPID: v.Daemon.PID, DaemonStartedAt: v.Daemon.StartedAt, HealthPort: port.Port, AppPID: appPID, GatePath: filepath.Join(profileDir, "daemon.maintenance"), OldBundle: v.Bundle, StagedBundle: o.StagedBundle, RollbackBundle: filepath.Join(v.WorkDir, "rollback", filepath.Base(v.Bundle)), OlderBundle: filepath.Join(v.WorkDir, "older", filepath.Base(v.Bundle)), OldBinarySHA256: v.OldBinarySHA256, NewBinarySHA256: asset.BinarySHA256, OldVersion: v.OldVersion, NewVersion: manifest.Version, NewCommit: manifest.Commit, OldDataCompatibility: v.OldDataCompatibility, DataCompatibility: manifest.DataCompatibility, RuntimeIDs: runtimeIDs, Workspaces: v.Workspaces, NewAppExecutable: exe, ReadyFile: filepath.Join(v.WorkDir, "ready-"+token), HandoffFile: filepath.Join(v.WorkDir, "handoff-"+token), Legacy: &LegacyState{InventorySHA256: o.InventorySHA256, InventoryPath: o.InventoryPath, Inventory: v, ManifestPath: o.ManifestPath, CompatibilityEvidencePath: o.CompatibilityEvidencePath}}
	if err := newLocalAdapter(path).validateBundlePaths(j); err != nil {
		return false, err
	}
	if err := SaveJournal(path, *j); err != nil {
		return false, err
	}
	return AdvanceLegacy(ctx, path, j, &legacyLocalAdapter{newLocalAdapter(path)})
}

func RunLegacyJournal(ctx context.Context, path, token string) (bool, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != "journal.json" || filepath.Base(filepath.Dir(path)) != "zh-tw-update" || !transactionToken.MatchString(token) {
		return false, errors.New("invalid legacy journal location or token")
	}
	unlock, err := lockJournal(filepath.Join(filepath.Dir(path), "journal.lock"))
	if err != nil {
		return false, err
	}
	defer unlock()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Size() > 1<<20 {
		return false, errors.New("untrusted legacy journal")
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, errors.New("legacy journal changed")
	}
	var j Journal
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return false, err
	}
	if j.Version != 1 || j.Token != token || j.Legacy == nil {
		return false, errors.New("invalid legacy journal")
	}
	v, err := ReadLegacyInventory(j.Legacy.InventoryPath, j.Legacy.InventorySHA256)
	if err != nil || !reflect.DeepEqual(v, j.Legacy.Inventory) {
		return false, errors.New("approved inventory changed")
	}
	target, err := legacyMachineIdentity()
	if err != nil {
		return false, err
	}
	if target == v.ControllerMachineIDSHA256 {
		return false, errors.New("controller and target must be different machines")
	}
	if digest, err := hashFile(j.Legacy.CompatibilityEvidencePath); err != nil || digest != v.CompatibilityEvidenceSHA256 {
		return false, errors.New("compatibility evidence changed")
	}
	m, err := readApprovedManifest(j.Legacy.ManifestPath, v.CandidateManifestSHA256)
	if err != nil {
		return false, err
	}
	asset := m.Desktop["darwin-arm64"]
	var runtimeIDs []string
	for _, ids := range v.Workspaces {
		runtimeIDs = append(runtimeIDs, ids...)
	}
	sort.Strings(runtimeIDs)
	appPIDMatches := false
	for _, p := range v.Apps {
		if p.PID == j.AppPID && filepath.Dir(p.ExecutablePath) == filepath.Join(v.Bundle, "Contents", "MacOS") {
			appPIDMatches = true
		}
	}
	profileDir, err := cli.ProfileDir(v.Profile)
	if err != nil {
		return false, err
	}
	if j.Profile != v.Profile || j.CLIEntry != v.CLIEntry || j.OldBundle != v.Bundle || j.OldVersion != v.OldVersion || j.OldCommit != "" || j.OldBinarySHA256 != v.OldBinarySHA256 || j.DaemonID != v.DaemonID || j.ServerURLSHA256 != v.ServerURLSHA256 || j.DaemonPID != v.Daemon.PID || j.DaemonStartedAt != v.Daemon.StartedAt || !appPIDMatches || j.GatePath != filepath.Join(profileDir, "daemon.maintenance") || j.NewVersion != m.Version || j.NewCommit != m.Commit || j.NewBinarySHA256 != asset.BinarySHA256 || j.DataCompatibility != m.DataCompatibility || j.OldDataCompatibility != v.OldDataCompatibility || !reflect.DeepEqual(j.Workspaces, v.Workspaces) || !reflect.DeepEqual(j.RuntimeIDs, runtimeIDs) || filepath.Clean(v.WorkDir) != filepath.Dir(path) || !strings.HasPrefix(filepath.Clean(j.StagedBundle), filepath.Join(v.WorkDir, "staging")+string(os.PathSeparator)) || j.ReadyFile != filepath.Join(v.WorkDir, "ready-"+token) || j.HandoffFile != filepath.Join(v.WorkDir, "handoff-"+token) {
		return false, errors.New("legacy journal does not match approved inventory")
	}
	a := newLocalAdapter(path)
	if err := a.validateBundlePaths(&j); err != nil {
		return false, err
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	self, err := hashFile(executable)
	if err != nil || self != j.NewBinarySHA256 {
		return false, errors.New("unverified legacy coordinator")
	}
	return AdvanceLegacy(ctx, path, &j, &legacyLocalAdapter{a})
}

// LegacyJournalPhase reports only the non-sensitive durable status of this
// particular transaction; it never returns the approved inventory.
func LegacyJournalPhase(path, token, inventoryHash string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != "journal.json" || filepath.Base(filepath.Dir(path)) != "zh-tw-update" {
		return "", errors.New("invalid legacy journal path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Size() > 1<<20 {
		return "", errors.New("untrusted legacy journal")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("legacy journal changed")
	}
	var j Journal
	if err := json.NewDecoder(f).Decode(&j); err != nil {
		return "", err
	}
	if j.Version != 1 || j.Legacy == nil || !transactionToken.MatchString(j.Token) || (token != "" && token != j.Token) || (inventoryHash != "" && inventoryHash != j.Legacy.InventorySHA256) {
		return "", errors.New("legacy journal identity mismatch")
	}
	switch j.Phase {
	case "prepared", "preparing", "stopping_old", "swapping", "verifying_new", "releasing_gate", "awaiting_acceptance", "finalizing", "rolling_back", "restored_legacy", "complete":
		return j.Phase, nil
	default:
		return "", errors.New("unrecognized legacy transaction phase")
	}
}
