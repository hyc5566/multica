package handler

import "testing"

func TestAnnotateTaskQuotaWindowsUsesOnlyStableModelIdentity(t *testing.T) {
	snapshot := ProviderUsageSnapshot{Windows: []ProviderUsageWindow{
		{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro"},
		{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
	}}
	annotateTaskQuotaWindows(&snapshot, "antigravity", "gemini-2.5-pro")
	if snapshot.Windows[0].Scope != "model" || snapshot.Windows[0].ModelMatch != "exact" {
		t.Fatalf("exact model annotation = %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].ModelMatch != "unknown" {
		t.Fatalf("non-matching model must remain unknown: %+v", snapshot.Windows[1])
	}
}

func TestAnnotateTaskQuotaWindowsMarksSharedAccountWindows(t *testing.T) {
	snapshot := ProviderUsageSnapshot{Windows: []ProviderUsageWindow{{ID: "five_hour", Label: "5 hour"}}}
	annotateTaskQuotaWindows(&snapshot, "claude", "claude-opus")
	if snapshot.Windows[0].Scope != "account" || snapshot.Windows[0].ModelMatch != "shared" {
		t.Fatalf("Claude annotation = %+v", snapshot.Windows[0])
	}
}

func TestAnnotateTaskQuotaWindowsDoesNotGuessCodexModel(t *testing.T) {
	snapshot := ProviderUsageSnapshot{Windows: []ProviderUsageWindow{
		{ID: "codex-primary", Group: "Codex", Label: "5 hour"},
		{ID: "model-0-primary", Group: "Spark", Label: "5 hour"},
	}}
	annotateTaskQuotaWindows(&snapshot, "codex", "gpt-5.3-codex-spark")
	if snapshot.Windows[0].Scope != "provider" || snapshot.Windows[0].ModelMatch != "shared" {
		t.Fatalf("base Codex annotation = %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].Scope != "model" || snapshot.Windows[1].ModelMatch != "unknown" {
		t.Fatalf("named bucket must not be guessed exact: %+v", snapshot.Windows[1])
	}
}
