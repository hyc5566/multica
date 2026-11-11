package daemonupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func legacyAppPIDs(bundle string) ([]int, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("legacy App enumeration requires macOS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "uid=", "-o", "pid=", "-o", "comm=").Output()
	if err != nil {
		return nil, errors.New("legacy App process enumeration failed")
	}
	return parseLegacyAppPIDs(string(output), bundle, os.Getuid())
}

// Updates must not exchange a bundle while another account is using it.
var checkSharedBundle = func(bundle string) error {
	_, err := legacyAppPIDs(bundle)
	return err
}

func parseLegacyAppPIDs(output, bundle string, uid int) ([]int, error) {
	if !filepath.IsAbs(bundle) || !strings.HasSuffix(bundle, ".app") {
		return nil, errors.New("invalid App bundle")
	}
	prefix := filepath.Join(bundle, "Contents") + string(os.PathSeparator)
	var result []int
	seen := map[int]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			return nil, errors.New("malformed process listing")
		}
		owner, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, errors.New("malformed process UID")
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			return nil, errors.New("malformed process PID")
		}
		// Locate the command after the two numeric columns without splitting paths
		// containing spaces. Only processes mapped inside this App are candidates.
		remaining := strings.TrimLeft(line, " \t")
		remaining = strings.TrimLeft(remaining[len(fields[0]):], " \t")
		remaining = strings.TrimLeft(remaining[len(fields[1]):], " \t")
		if owner != uid {
			if strings.HasPrefix(remaining, prefix) {
				return nil, errors.New("App bundle is in use by another user")
			}
			continue
		}
		if strings.HasPrefix(remaining, prefix) && remaining != bundleCLI(bundle) {
			if pid == 1 {
				return nil, errors.New("invalid App process PID")
			}
			if seen[pid] {
				return nil, errors.New("duplicate process listing")
			}
			seen[pid] = true
			result = append(result, pid)
		}
	}
	sort.Ints(result)
	return result, nil
}
