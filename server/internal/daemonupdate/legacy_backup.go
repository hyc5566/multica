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
)

type legacyFileRecord struct {
	Path  string `json:"path"`
	Mode  string `json:"mode"`
	Value string `json:"value"`
}

var legacyVerifySignature = func(ctx context.Context, path string) error { return (macInspector{}).VerifySignature(ctx, path) }
var legacyCopyBundle = func(ctx context.Context, source, destination string) error {
	return exec.CommandContext(ctx, "/usr/bin/ditto", source, destination).Run()
}

func legacyTreeManifest(root string) ([]legacyFileRecord, string, error) {
	var records []legacyFileRecord
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		record := legacyFileRecord{Path: rel, Mode: info.Mode().String()}
		switch {
		case info.Mode().IsRegular():
			record.Value, err = hashFile(path)
		case info.Mode()&os.ModeSymlink != 0:
			record.Value, err = os.Readlink(path)
		case info.IsDir():
		default:
			return errors.New("unsupported object in App bundle")
		}
		if err != nil {
			return err
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(records)
	if err != nil {
		return nil, "", err
	}
	return records, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func syncLegacyTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !info.Mode().IsRegular() {
			return errors.New("unsupported backup object")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncDir(directories[i]); err != nil {
			return err
		}
	}
	return nil
}

func verifyLegacyOriginalTree(ctx context.Context, bundle, expected string) error {
	if !sha256Hex.MatchString(expected) {
		return errors.New("original App tree identity missing")
	}
	_, actual, err := legacyTreeManifest(bundle)
	if err != nil || actual != expected {
		return errors.New("original App tree does not match approved backup")
	}
	if err := legacyVerifySignature(ctx, bundle); err != nil {
		return errors.New("original App signature invalid")
	}
	return nil
}
func copyLegacyCoordinator(src, dst, expected string) error {
	if actual, err := hashFile(dst); err == nil {
		if actual != expected {
			return errors.New("saved coordinator hash mismatch")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if e := out.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if actual, err := hashFile(dst); err != nil || actual != expected {
		return errors.New("copied coordinator hash mismatch")
	}
	return syncDir(filepath.Dir(dst))
}

func legacyCLIEntryState(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	value := ""
	if info.Mode()&os.ModeSymlink != 0 {
		value, err = os.Readlink(path)
	} else if info.Mode().IsRegular() {
		value, err = hashFile(path)
	} else {
		return "", errors.New("unsupported CLI entry")
	}
	if err != nil {
		return "", err
	}
	return info.Mode().String() + ":" + value, nil
}
func (a *legacyLocalAdapter) Prepare(ctx context.Context, j *Journal) error {
	v := j.Legacy.Inventory
	if j.OldBinarySHA256 == j.NewBinarySHA256 {
		return errors.New("old and candidate binary hashes are identical; cannot prove bundle exchange")
	}
	if hash, err := hashFile(j.Legacy.CompatibilityEvidencePath); err != nil || hash != v.CompatibilityEvidenceSHA256 {
		return errors.New("compatibility evidence mismatch")
	}
	if err := verifyCLIEntry(v.Bundle, v.CLIEntry, v.WorkDir, currentPathDirs()); err != nil {
		return err
	}
	entryState, err := legacyCLIEntryState(v.CLIEntry)
	if err != nil {
		return err
	}
	if j.Legacy.CLIEntryState != "" && j.Legacy.CLIEntryState != entryState {
		return errors.New("CLI entry changed")
	}
	oldDev, err := legacyDevice(v.Bundle)
	if err != nil {
		return err
	}
	stageDev, err := legacyDevice(j.StagedBundle)
	if err != nil {
		return err
	}
	workDev, err := legacyDevice(v.WorkDir)
	if err != nil {
		return err
	}
	if oldDev != stageDev || oldDev != workDev {
		return errors.New("App and update paths must share a filesystem")
	}
	if err := verifyBundleHash(v.Bundle, v.OldBinarySHA256); err != nil {
		return err
	}
	if err := verifyBundleHash(j.StagedBundle, j.NewBinarySHA256); err != nil {
		return err
	}
	if err := legacyVerifySignature(ctx, j.StagedBundle); err != nil {
		return errors.New("candidate App signature invalid")
	}
	_, candidateTree, err := legacyTreeManifest(j.StagedBundle)
	if err != nil {
		return err
	}
	if j.Legacy.CandidateTreeSHA256 != "" && j.Legacy.CandidateTreeSHA256 != candidateTree {
		return errors.New("candidate App changed during preparation")
	}
	if err := legacyVerifySignature(ctx, v.Bundle); err != nil {
		return errors.New("old App signature invalid")
	}
	records, digest, err := legacyTreeManifest(v.Bundle)
	if err != nil {
		return err
	}
	if j.Legacy.BackupManifestSHA256 != "" && j.Legacy.BackupManifestSHA256 != digest {
		return errors.New("old App changed during preparation")
	}
	backupDir := filepath.Join(v.WorkDir, "baseline")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(backupDir)
	if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
		return errors.New("untrusted backup directory")
	}
	backup := filepath.Join(backupDir, filepath.Base(v.Bundle))
	if _, err := os.Lstat(backup); os.IsNotExist(err) {
		if err := legacyCopyBundle(ctx, v.Bundle, backup); err != nil {
			return err
		}
		if err := syncDir(backupDir); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, backupDigest, err := legacyTreeManifest(backup)
	if err != nil || backupDigest != digest {
		return errors.New("backup App does not match approved App")
	}
	if err := legacyVerifySignature(ctx, backup); err != nil {
		return errors.New("backup App signature invalid")
	}
	if err := syncLegacyTree(backup); err != nil {
		return err
	}
	if err := saveJSON(filepath.Join(backupDir, "manifest.json"), records); err != nil {
		return err
	}
	coordinator := filepath.Join(v.WorkDir, "legacy-coordinator")
	if err := copyLegacyCoordinator(bundleCLI(j.StagedBundle), coordinator, j.NewBinarySHA256); err != nil {
		return err
	}
	j.Legacy.BackupManifestSHA256 = digest
	j.Legacy.CandidateTreeSHA256 = candidateTree
	j.CandidateTreeSHA256 = candidateTree
	j.Legacy.CLIEntryState = entryState
	j.Legacy.CoordinatorSHA256 = j.NewBinarySHA256
	if err := SaveJournal(a.journalPath, *j); err != nil {
		return err
	}
	return acquireGate(a.journalPath, j)
}

// cleanupLegacyArtifacts removes only the exact attested duplicate backup and
// coordinator, after the new ledger and successful task receipt are durable.
func (a *legacyLocalAdapter) cleanupLegacyArtifacts(ctx context.Context, j *Journal) error {
	root := filepath.Dir(a.journalPath)
	dir := filepath.Join(root, "baseline")
	bundleName := filepath.Base(j.OldBundle)
	backup := filepath.Join(dir, bundleName)
	manifest := filepath.Join(dir, "manifest.json")
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if !j.Legacy.BaselineCleanupRequested {
			return errors.New("baseline disappeared before cleanup")
		}
		if err := syncDir(root); err != nil {
			return err
		}
	} else {
		if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
			return errors.New("untrusted baseline directory")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		hasBundle, hasManifest := false, false
		for _, entry := range entries {
			switch entry.Name() {
			case bundleName:
				hasBundle = true
			case "manifest.json":
				hasManifest = true
			default:
				return errors.New("foreign content in baseline directory")
			}
		}
		if (!hasBundle || !hasManifest) && !j.Legacy.BaselineCleanupRequested {
			return errors.New("baseline incomplete before cleanup")
		}
		if hasBundle {
			info, err := os.Lstat(backup)
			if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
				return errors.New("untrusted baseline App")
			}
			if err := verifyLegacyOriginalTree(ctx, backup, j.Legacy.BackupManifestSHA256); err != nil {
				return err
			}
		}
		if hasManifest {
			info, err := os.Lstat(manifest)
			if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) {
				return errors.New("untrusted baseline manifest")
			}
			if hash, err := hashFile(manifest); err != nil || hash != j.Legacy.BackupManifestSHA256 {
				return errors.New("baseline manifest mismatch")
			}
		}
		if !j.Legacy.BaselineCleanupRequested {
			j.Legacy.BaselineCleanupRequested = true
			if err := SaveJournal(a.journalPath, *j); err != nil {
				return err
			}
		}
		if hasBundle {
			if err := os.RemoveAll(backup); err != nil {
				return err
			}
			if err := syncDir(dir); err != nil {
				return err
			}
		}
		if hasManifest {
			if err := os.Remove(manifest); err != nil {
				return err
			}
			if err := syncDir(dir); err != nil {
				return err
			}
		}
		if err := os.Remove(dir); err != nil {
			return err
		}
		if err := syncDir(root); err != nil {
			return err
		}
	}
	coordinator := filepath.Join(root, "legacy-coordinator")
	info, err = os.Lstat(coordinator)
	if os.IsNotExist(err) {
		if !j.Legacy.CoordinatorCleanupRequested {
			return errors.New("coordinator disappeared before cleanup")
		}
		return syncDir(root)
	}
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || j.Legacy.CoordinatorSHA256 != j.NewBinarySHA256 {
		return errors.New("untrusted retained coordinator")
	}
	if hash, err := hashFile(coordinator); err != nil || hash != j.Legacy.CoordinatorSHA256 {
		return errors.New("retained coordinator hash mismatch")
	}
	if !j.Legacy.CoordinatorCleanupRequested {
		j.Legacy.CoordinatorCleanupRequested = true
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	if err := os.Remove(coordinator); err != nil {
		return err
	}
	return syncDir(root)
}
