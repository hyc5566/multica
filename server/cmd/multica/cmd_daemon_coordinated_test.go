package main

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestAdoptionPendingUsesExitTwo(t *testing.T) {
	if got := cli.ExitCodeFor(adoptionPending{}); got != 2 {
		t.Fatalf("pending exit code = %d", got)
	}
}

func TestLegacyPendingUsesExitTwo(t *testing.T) {
	if got := cli.ExitCodeFor(legacyPending{}); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
}
