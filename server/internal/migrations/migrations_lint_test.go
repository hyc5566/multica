package migrations

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationNumericPrefixesAreUnique(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")

	// Migrations through 128 contain historical duplicate numeric prefixes.
	// From 129 onward, keep the numeric sequence unique so release tooling and
	// operators can identify one schema change unambiguously by its number.
	const firstUniqueMigrationNumber = 129
	// These exact Taiwan-edition stems were already released before upstream
	// reused their numbers. The ledger keys full stems; renaming would replay
	// non-idempotent SQL on installed databases. All new collisions still fail.
	historicalTaiwanPairs := map[string]string{
		"451_runtime_provider_usage_snapshot":        "451_agent_task_comment_thread",
		"452_runtime_provider_usage_snapshot_key":    "452_agent_task_pending_thread_unique",
		"453_provider_quota_observation":             "453_drop_pending_issue_agent_unique",
		"454_provider_quota_observation_key":         "454_drop_comment_content_bigm_index",
		"455_task_quota_checkpoint":                  "455_drop_comment_content_trgm_index",
		"456_task_quota_checkpoint_key":              "456_cancel_comment_assignee_fallbacks",
		"457_task_quota_checkpoint_workspace_time":   "457_task_message_output_truncated",
		"458_provider_quota_observation_received_at": "458_agent_task_cancellation_actor",
		"459_provider_quota_rollup":                  "459_chat_message_assistant_task_index",
		"460_provider_quota_rollup_key":              "460_agent_task_queue_autopilot_run_created_at_index",
		"461_provider_quota_rollup_workspace_time":   "461_channel_trigger_snapshot",
		"462_provider_quota_observation_id":          "462_delete_reference_only_pr_links",
	}
	stemByNumber := make(map[int]string)
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUniqueMigrationNumber {
			continue
		}
		if previous, exists := stemByNumber[number]; exists {
			if historicalTaiwanPairs[previous] == stem || historicalTaiwanPairs[stem] == previous {
				continue
			}
			t.Errorf("migrations %s and %s share numeric prefix %s", previous, stem, prefix)
			continue
		}
		stemByNumber[number] = stem
	}
}

func TestMigrationFilesHaveMatchingDirections(t *testing.T) {
	files := migrationFilesForLint(t, "*.sql")

	directionsByStem := make(map[string]map[string]bool)
	for _, file := range files {
		stem, direction, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		if directionsByStem[stem] == nil {
			directionsByStem[stem] = make(map[string]bool)
		}
		directionsByStem[stem][direction] = true
	}

	for stem, directions := range directionsByStem {
		if !directions["up"] || !directions["down"] {
			t.Errorf("migration %s must have both .up.sql and .down.sql files", stem)
		}
	}
}

func migrationFilesForLint(t *testing.T, pattern string) []string {
	t.Helper()

	dir := realMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migration files matched %s in %s", pattern, dir)
	}
	sort.Strings(files)
	return files
}

func realMigrationsDir(t *testing.T) string {
	t.Helper()

	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration lint test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "migrations"))
}

func splitMigrationFilename(name string) (stem, direction string, ok bool) {
	for _, candidateDirection := range []string{"up", "down"} {
		suffix := fmt.Sprintf(".%s.sql", candidateDirection)
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), candidateDirection, true
		}
	}
	return "", "", false
}
