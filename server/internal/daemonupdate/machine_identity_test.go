package daemonupdate

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestMacMachineIdentityNative(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS hardware identity")
	}
	first, err := MachineIdentitySHA256()
	if err != nil {
		t.Fatal(err)
	}
	second, err := MachineIdentitySHA256()
	if err != nil || len(first) != 64 || first != second {
		t.Fatal("native hardware fingerprint is unavailable or unstable")
	}
}

func TestMacMachineFingerprintRejectsUnknownHardware(t *testing.T) {
	const id = "AABBCCDD-1234-5678-9ABC-0123456789AB"
	line := `  |   "IOPlatformUUID" = "` + id + `"` + "\n"
	for _, test := range []struct {
		name, output string
		failure      bool
	}{
		{"valid", line, false},
		{"lowercase", strings.ReplaceAll(line, id, strings.ToLower(id)), false},
		{"missing", `"IOPlatformSerialNumber" = "fixture"`, true},
		{"duplicate", line + line, true},
		{"zero", strings.ReplaceAll(line, id, "00000000-0000-0000-0000-000000000000"), true},
		{"invalid", strings.ReplaceAll(line, id, "unknown"), true},
		{"oversized", strings.Repeat("x", 1<<20+1), true},
		{"denied", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := macMachineIdentitySHA256(func(command string, args ...string) ([]byte, error) {
				if command != "/usr/sbin/ioreg" || strings.Join(args, " ") != "-rd1 -c IOPlatformExpertDevice" {
					t.Fatal("unexpected hardware query")
				}
				if test.name == "denied" {
					return nil, errors.New("denied")
				}
				return []byte(test.output), nil
			})
			if (err != nil) != test.failure {
				t.Fatalf("failure=%v, err=%v", test.failure, err)
			}
			if !test.failure && got != fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(id)))) {
				t.Fatal("unexpected fingerprint")
			}
			if strings.Contains(got, id) || strings.Contains(got, strings.ToLower(id)) {
				t.Fatal("raw identity exposed")
			}
		})
	}
}
