package daemonupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/cli"
)

type gateOwner struct {
	ID  string `json:"id"`
	PID int    `json:"pid"`
}

var gateOpenOwner = os.OpenFile
var gateWriteOwner = func(f *os.File, owner gateOwner) error { return json.NewEncoder(f).Encode(owner) }
var gateSyncOwner = func(f *os.File) error { return f.Sync() }
var syncGateDirectory = syncDir

func cleanupOwnedIncompleteGate(journalPath string, j *Journal, ownerInfo os.FileInfo) error {
	gateInfo, err := os.Lstat(j.GatePath)
	if err != nil {
		return err
	}
	identity, err := gateIdentity(gateInfo)
	if err != nil || identity != j.GateIdentity {
		return errors.New("gate inode changed; operator recovery required")
	}
	entries, err := os.ReadDir(j.GatePath)
	if err != nil {
		return err
	}
	if len(entries) > 1 || (len(entries) == 1 && entries[0].Name() != "owner") {
		return errors.New("gate contains foreign content; operator recovery required")
	}
	if len(entries) == 1 {
		if ownerInfo == nil {
			return errors.New("gate owner is not verified; operator recovery required")
		}
		current, err := os.Lstat(filepath.Join(j.GatePath, "owner"))
		if err != nil || !os.SameFile(ownerInfo, current) || !current.Mode().IsRegular() || !ownedByCurrentUser(current) {
			return errors.New("gate owner changed; operator recovery required")
		}
		if err := os.Remove(filepath.Join(j.GatePath, "owner")); err != nil {
			return err
		}
	}
	if err := os.Remove(j.GatePath); err != nil {
		return err
	}
	if err := syncGateDirectory(filepath.Dir(j.GatePath)); err != nil {
		return err
	}
	j.GateIdentity, j.GateCreationIntent = "", false
	return SaveJournal(journalPath, *j)
}

func verifyGate(j *Journal) error {
	dir, err := cli.ProfileDir(j.Profile)
	if err != nil || j.Profile == "" || filepath.Clean(j.GatePath) != filepath.Join(dir, "daemon.maintenance") {
		return errors.New("gate profile mismatch")
	}
	info, err := os.Lstat(j.GatePath)
	if err != nil {
		return err
	}
	identity, err := gateIdentity(info)
	if err != nil || identity == "" || identity != j.GateIdentity {
		return errors.New("gate identity mismatch")
	}
	ownerPath := filepath.Join(j.GatePath, "owner")
	ownerInfo, err := os.Lstat(ownerPath)
	if err != nil || !ownerInfo.Mode().IsRegular() || !ownedByCurrentUser(ownerInfo) || ownerInfo.Size() > 4096 {
		return errors.New("gate owner invalid")
	}
	f, err := os.Open(ownerPath)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(ownerInfo, opened) {
		return errors.New("gate owner changed")
	}
	var owner gateOwner
	if err := json.NewDecoder(f).Decode(&owner); err != nil || owner.ID != j.Token || owner.PID <= 0 {
		return errors.New("gate owner mismatch")
	}
	after, err := os.Lstat(j.GatePath)
	if err != nil || !os.SameFile(info, after) {
		return errors.New("gate changed")
	}
	return nil
}

// acquireGate never adopts a pre-existing directory unless the journal
// already records its exact inode and token. Ambiguous crashes fail closed.
func acquireGate(journalPath string, j *Journal) error {
	if j.Token == "" || len(j.Token) > 128 {
		return errors.New("invalid gate token")
	}
	dir, err := cli.ProfileDir(j.Profile)
	if err != nil || j.Profile == "" || filepath.Clean(j.GatePath) != filepath.Join(dir, "daemon.maintenance") {
		return errors.New("gate profile mismatch")
	}
	if j.GateIdentity != "" {
		if j.GateCreationIntent {
			if _, err := os.Lstat(filepath.Join(j.GatePath, "owner")); os.IsNotExist(err) {
				if cleanErr := cleanupOwnedIncompleteGate(journalPath, j, nil); cleanErr != nil {
					return cleanErr
				}
				return errors.New("interrupted empty gate removed; retry the update")
			}
			if err := verifyGate(j); err != nil {
				return fmt.Errorf("interrupted gate at %s requires operator recovery: %w", j.GatePath, err)
			}
			if err := syncGateDirectory(j.GatePath); err != nil {
				return err
			}
			if err := syncGateDirectory(filepath.Dir(j.GatePath)); err != nil {
				return err
			}
			j.GateCreationIntent = false
			if err := SaveJournal(journalPath, *j); err != nil {
				return err
			}
		}
		return verifyGate(j)
	}
	if j.GateCreationIntent {
		return errors.New("gate creation interrupted before inode was recorded; operator recovery required")
	}
	j.GateCreationIntent = true
	if err := SaveJournal(journalPath, *j); err != nil {
		return err
	}
	if err := os.Mkdir(j.GatePath, 0700); err != nil {
		j.GateCreationIntent = false
		if saveErr := SaveJournal(journalPath, *j); saveErr != nil {
			return fmt.Errorf("cannot acquire gate: %v; journal: %w", err, saveErr)
		}
		return fmt.Errorf("cannot acquire gate: %w", err)
	}
	info, err := os.Lstat(j.GatePath)
	if err != nil {
		return err
	}
	identity, err := gateIdentity(info)
	if err != nil {
		return err
	}
	j.GateIdentity = identity
	if err := SaveJournal(journalPath, *j); err != nil {
		return err
	}
	owner, err := gateOpenOwner(filepath.Join(j.GatePath, "owner"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if cleanErr := cleanupOwnedIncompleteGate(journalPath, j, nil); cleanErr != nil {
			return fmt.Errorf("create gate owner: %v; %w", err, cleanErr)
		}
		return err
	}
	ownerInfo, infoErr := owner.Stat()
	if infoErr != nil {
		owner.Close()
		return infoErr
	}
	err = gateWriteOwner(owner, gateOwner{j.Token, os.Getpid()})
	if err == nil {
		err = gateSyncOwner(owner)
	}
	if closeErr := owner.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		if cleanErr := cleanupOwnedIncompleteGate(journalPath, j, ownerInfo); cleanErr != nil {
			return fmt.Errorf("write gate owner: %v; %w", err, cleanErr)
		}
		return err
	}
	if err := syncGateDirectory(j.GatePath); err != nil {
		return fmt.Errorf("sync gate directory: %w", err)
	}
	if err := syncGateDirectory(filepath.Dir(j.GatePath)); err != nil {
		return fmt.Errorf("sync profile directory: %w", err)
	}
	j.GateCreationIntent = false
	if err := SaveJournal(journalPath, *j); err != nil {
		return err
	}
	return verifyGate(j)
}

func releaseGate(j *Journal) error {
	if err := verifyGate(j); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(j.GatePath, "owner")); err != nil {
		return err
	}
	if err := os.Remove(j.GatePath); err != nil {
		return fmt.Errorf("gate owner removed but gate still blocks claims: %w", err)
	}
	return syncGateDirectory(filepath.Dir(j.GatePath))
}
