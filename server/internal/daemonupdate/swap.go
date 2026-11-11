package daemonupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func exists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("bundle path is not a directory: %s", path)
	}
	return true, nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func moveBundle(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(from)); err != nil {
		return err
	}
	if filepath.Dir(from) != filepath.Dir(to) {
		return syncDir(filepath.Dir(to))
	}
	return nil
}

func verifyBundleHash(path, expected string) error {
	if len(expected) != 64 {
		return errors.New("invalid expected binary hash")
	}
	actual, err := hashFile(bundleCLI(path))
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("bundle binary hash mismatch")
	}
	return nil
}

func (a *localAdapter) validateBundlePaths(j *Journal) error {
	base := filepath.Base(j.OldBundle)
	root := filepath.Dir(a.journalPath)
	if !filepath.IsAbs(j.OldBundle) || !strings.HasSuffix(base, ".app") || filepath.Clean(j.RollbackBundle) != filepath.Join(root, "rollback", base) ||
		filepath.Clean(j.OlderBundle) != filepath.Join(root, "older", base) || filepath.Dir(filepath.Clean(j.StagedBundle)) != filepath.Join(root, "staging") ||
		!strings.HasSuffix(j.StagedBundle, ".app") {
		return errors.New("unsafe bundle paths")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || !ownedByCurrentUser(rootInfo) {
		return errors.New("unsafe update directory")
	}
	for _, name := range []string{"staging", "rollback", "older"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
			return errors.New("unsafe update subdirectory")
		}
	}
	return nil
}

func (a *localAdapter) SwapBundles(ctx context.Context, j *Journal) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if j.OldBinarySHA256 == j.NewBinarySHA256 {
		return errors.New("old and candidate binary hashes are identical; cannot prove bundle exchange")
	}
	if err := a.validateBundlePaths(j); err != nil {
		return err
	}
	if j.CandidateTreeSHA256 == "" {
		if j.SwapStep != 0 {
			return errors.New("candidate tree identity missing during resumed swap")
		}
		_, digest, err := legacyTreeManifest(j.StagedBundle)
		if err != nil {
			return err
		}
		j.CandidateTreeSHA256 = digest
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	if !sha256Hex.MatchString(j.CandidateTreeSHA256) {
		return errors.New("invalid candidate tree identity")
	}
	candidate := ""
	for _, path := range []string{j.OldBundle, j.StagedBundle} {
		if verifyBundleHash(path, j.NewBinarySHA256) == nil {
			if candidate != "" {
				return errors.New("ambiguous candidate App")
			}
			candidate = path
		}
	}
	if candidate == "" {
		return errors.New("candidate App unavailable")
	}
	_, actualTree, err := legacyTreeManifest(candidate)
	if err != nil || actualTree != j.CandidateTreeSHA256 {
		return errors.New("candidate App tree changed")
	}
	for {
		alive, err := processExists(j.AppPID)
		if err != nil {
			return err
		}
		if !alive {
			break
		}
		if err := pause(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	if err := verifyGate(j); err != nil {
		return err
	}
	if j.SwapStep == 0 {
		previous, err := exists(j.RollbackBundle)
		if err != nil {
			return err
		}
		older, err := exists(j.OlderBundle)
		if err != nil {
			return err
		}
		if previous && older {
			return errors.New("older rollback destination already occupied")
		}
		if previous {
			if err := verifyBundleHash(j.RollbackBundle, j.PreviousRollbackSHA256); err != nil {
				return err
			}
			_, digest, err := legacyTreeManifest(j.RollbackBundle)
			if err != nil {
				return err
			}
			if j.PreviousRollbackTreeSHA256 != "" && j.PreviousRollbackTreeSHA256 != digest {
				return errors.New("previous rollback tree changed")
			}
			if j.PreviousRollbackTreeSHA256 == "" {
				j.PreviousRollbackTreeSHA256 = digest
				if err := SaveJournal(a.journalPath, *j); err != nil {
					return err
				}
			}
			if err := moveBundle(j.RollbackBundle, j.OlderBundle); err != nil {
				return err
			}
		} else if older {
			if err := verifyBundleHash(j.OlderBundle, j.PreviousRollbackSHA256); err != nil {
				return err
			}
			_, digest, err := legacyTreeManifest(j.OlderBundle)
			if err != nil || !sha256Hex.MatchString(j.PreviousRollbackTreeSHA256) || digest != j.PreviousRollbackTreeSHA256 {
				return errors.New("previous rollback tree mismatch")
			}
		} else if j.PreviousRollbackSHA256 != "" {
			return errors.New("recorded previous rollback missing")
		}
		j.SwapStep = 1
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	if j.SwapStep == 1 {
		current, err := os.Lstat(j.OldBundle)
		if err != nil {
			return err
		}
		staged, err := os.Lstat(j.StagedBundle)
		if err != nil {
			return err
		}
		currentID, err := gateIdentity(current)
		if err != nil {
			return err
		}
		stagedID, err := gateIdentity(staged)
		if err != nil {
			return err
		}
		if j.CurrentIdentity == "" && j.StagedIdentity == "" {
			if err := verifyBundleHash(j.OldBundle, j.OldBinarySHA256); err != nil {
				return err
			}
			if err := verifyBundleHash(j.StagedBundle, j.NewBinarySHA256); err != nil {
				return err
			}
			j.CurrentIdentity, j.StagedIdentity = currentID, stagedID
			if err := SaveJournal(a.journalPath, *j); err != nil {
				return err
			}
		}
		if currentID == j.CurrentIdentity && stagedID == j.StagedIdentity {
			if err := verifyBundleHash(j.OldBundle, j.OldBinarySHA256); err != nil {
				return err
			}
			if err := verifyBundleHash(j.StagedBundle, j.NewBinarySHA256); err != nil {
				return err
			}
			if err := checkSharedBundle(j.OldBundle); err != nil {
				return err
			}
			if err := exchangeBundles(j.OldBundle, j.StagedBundle); err != nil {
				return err
			}
			if err := syncDir(filepath.Dir(j.OldBundle)); err != nil {
				return err
			}
			if err := syncDir(filepath.Dir(j.StagedBundle)); err != nil {
				return err
			}
		} else if currentID == j.StagedIdentity && stagedID == j.CurrentIdentity {
			if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
				return err
			}
			if err := verifyBundleHash(j.StagedBundle, j.OldBinarySHA256); err != nil {
				return err
			}
		} else {
			return errors.New("bundle inode changed during exchange")
		}
		j.SwapStep = 2
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	if j.SwapStep == 2 {
		staged, err := exists(j.StagedBundle)
		if err != nil {
			return err
		}
		rollback, err := exists(j.RollbackBundle)
		if err != nil {
			return err
		}
		if staged && !rollback {
			if err := verifyBundleHash(j.StagedBundle, j.OldBinarySHA256); err != nil {
				return err
			}
			if err := moveBundle(j.StagedBundle, j.RollbackBundle); err != nil {
				return err
			}
		} else if !staged && rollback {
			if err := verifyBundleHash(j.RollbackBundle, j.OldBinarySHA256); err != nil {
				return err
			}
		} else {
			return errors.New("ambiguous staged and rollback bundles")
		}
		j.SwapStep = 3
		if err := SaveJournal(a.journalPath, *j); err != nil {
			return err
		}
	}
	if j.SwapStep != 3 {
		return errors.New("invalid bundle swap step")
	}
	if err := verifyBundleHash(j.OldBundle, j.NewBinarySHA256); err != nil {
		return err
	}
	if err := verifyBundleHash(j.RollbackBundle, j.OldBinarySHA256); err != nil {
		return err
	}
	if j.PreviousRollbackSHA256 != "" {
		if err := verifyBundleHash(j.OlderBundle, j.PreviousRollbackSHA256); err != nil {
			return err
		}
		_, digest, err := legacyTreeManifest(j.OlderBundle)
		if err != nil || !sha256Hex.MatchString(j.PreviousRollbackTreeSHA256) || digest != j.PreviousRollbackTreeSHA256 {
			return errors.New("previous rollback tree mismatch")
		}
	}
	return nil
}
