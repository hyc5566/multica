package daemonupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestGateOwnerFailuresCleanOnlyOwnedEmptyGate(t *testing.T) {
	for _, fault := range []string{"open", "write", "sync"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(cli.TaskConfigRootEnv, root)
			profile := filepath.Join(root, "profiles", "test")
			if err := os.MkdirAll(profile, 0700); err != nil {
				t.Fatal(err)
			}
			j := &Journal{Profile: "test", Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance")}
			oldOpen, oldWrite, oldSync := gateOpenOwner, gateWriteOwner, gateSyncOwner
			defer func() { gateOpenOwner, gateWriteOwner, gateSyncOwner = oldOpen, oldWrite, oldSync }()
			switch fault {
			case "open":
				gateOpenOwner = func(string, int, os.FileMode) (*os.File, error) { return nil, errors.New("open fault") }
			case "write":
				gateWriteOwner = func(*os.File, gateOwner) error { return errors.New("write fault") }
			case "sync":
				gateSyncOwner = func(*os.File) error { return errors.New("sync fault") }
			}
			if err := acquireGate(filepath.Join(root, "journal"), j); err == nil {
				t.Fatal("expected failure")
			}
			if _, err := os.Lstat(j.GatePath); !os.IsNotExist(err) {
				t.Fatalf("owned incomplete gate remains: %v", err)
			}
			if j.GateIdentity != "" || j.GateCreationIntent {
				t.Fatal("gate state not cleared")
			}
		})
	}
}

func TestGateDirectorySyncFailureReturnsErrorAndPreservesOwnedGate(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{Profile: "test", Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance")}
	old := syncGateDirectory
	defer func() { syncGateDirectory = old }()
	syncGateDirectory = func(string) error { return errors.New("sync fault") }
	if err := acquireGate(filepath.Join(root, "journal"), j); err == nil {
		t.Fatal("sync fault accepted")
	}
	if err := verifyGate(j); err != nil {
		t.Fatalf("owned gate was not preserved: %v", err)
	}
}

func TestGateRejectsOtherOwnerAndDoesNotAdoptDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{Profile: "test", Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance")}
	path := filepath.Join(root, "journal")
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	if err := verifyGate(j); err != nil {
		t.Fatal(err)
	}
	other := *j
	other.Token = "other"
	if err := verifyGate(&other); err == nil {
		t.Fatal("other token accepted")
	}
	other = *j
	other.GateIdentity = ""
	if err := acquireGate(path, &other); err == nil {
		t.Fatal("pre-existing gate adopted")
	}
	if _, err := os.Stat(filepath.Join(j.GatePath, "owner")); err != nil {
		t.Fatalf("owner removed: %v", err)
	}
}
