package daemonupdate

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

var platformUUIDLine = regexp.MustCompile(`(?m)^\s*(?:\|\s*)*"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})"\s*$`)

// MachineIdentitySHA256 identifies the target Mac without exposing its UUID.
// The operator must supply the independently observed controller fingerprint.
func MachineIdentitySHA256() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("App machine identity requires macOS")
	}
	return macMachineIdentitySHA256(legacyMachineCommand)
}

func macMachineIdentitySHA256(run func(string, ...string) ([]byte, error)) (string, error) {
	data, err := run("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
	if err != nil || len(data) > 1<<20 {
		return "", errors.New("Mac hardware identity unavailable")
	}
	matches := platformUUIDLine.FindAllSubmatch(data, -1)
	if len(matches) != 1 {
		return "", errors.New("Mac hardware identity missing or ambiguous")
	}
	value := strings.ToLower(string(matches[0][1]))
	if value == "00000000-0000-0000-0000-000000000000" || value == "ffffffff-ffff-ffff-ffff-ffffffffffff" {
		return "", errors.New("invalid Mac hardware identity")
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value))), nil
}
