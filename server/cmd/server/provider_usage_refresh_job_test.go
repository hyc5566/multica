package main

import (
	"testing"
	"time"
)

func TestProviderUsagePlanTimeIsStableWithinBucket(t *testing.T) {
	first := time.Date(2026, 9, 5, 10, 4, 40, 0, time.UTC)
	second := first.Add(10 * time.Second)
	a := providerUsagePlanTime("builtin:daemon-a:codex", first)
	b := providerUsagePlanTime("builtin:daemon-a:codex", second)
	if !a.Equal(b) {
		t.Fatalf("plan changed within bucket: %s != %s", a, b)
	}
	if a.After(first) || first.Sub(a) >= providerUsageRefreshCadence {
		t.Fatalf("plan %s is outside current cadence before %s", a, first)
	}
}
