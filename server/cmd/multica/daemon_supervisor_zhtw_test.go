package main

import "testing"

func TestSupervisedBySystemd(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cases := []struct {
		name   string
		values map[string]string
		goos   string
		want   bool
	}{
		{"systemd started us", map[string]string{"SYSTEMD_EXEC_PID": "42"}, "linux", true},
		{"inherited from a terminal service", map[string]string{"SYSTEMD_EXEC_PID": "7", "INVOCATION_ID": "x"}, "linux", false},
		{"older systemd with installer marker", map[string]string{"MULTICA_DAEMON_SUPERVISOR": "systemd", "INVOCATION_ID": "x"}, "linux", true},
		{"marker without a systemd invocation", map[string]string{"MULTICA_DAEMON_SUPERVISOR": "systemd"}, "linux", false},
		{"plain terminal", map[string]string{}, "linux", false},
		{"macOS", map[string]string{"SYSTEMD_EXEC_PID": "42"}, "darwin", false},
	}
	for _, tc := range cases {
		if got := supervisedBySystemd(env(tc.values), 42, tc.goos); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
