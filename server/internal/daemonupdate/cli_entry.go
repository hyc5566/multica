package daemonupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// verifyCLIEntry refuses every visible independent multica entry, including a
// managed userData copy. It never removes or rewrites an existing entry.
func verifyCLIEntry(bundle, entry, workDir string, pathDirs []string) error {
	if !filepath.IsAbs(entry) {
		return errors.New("CLI entry must be absolute")
	}
	actual, err := filepath.EvalSymlinks(bundleCLI(bundle))
	if err != nil {
		return err
	}
	entryTarget, err := filepath.EvalSymlinks(entry)
	if err != nil || entryTarget != actual {
		return errors.New("registered CLI entry differs from App CLI")
	}
	candidates := []string{filepath.Join(filepath.Dir(workDir), "bin", "multica")}
	for _, dir := range pathDirs {
		if dir == "" {
			dir = "."
		}
		candidates = append(candidates, filepath.Join(dir, "multica"))
	}
	for _, path := range candidates {
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot inspect CLI entry %s", path)
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil || target != actual {
			return fmt.Errorf("independent CLI entry detected at %s; resolve it manually", path)
		}
	}
	return nil
}

func currentPathDirs() []string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	if runtime.GOOS == "darwin" {
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	}
	return dirs
}
