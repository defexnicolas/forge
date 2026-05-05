package agent

import "testing"

func TestIsToolFailureSummary(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    bool
	}{
		// Genuine failures the runtime wrappers produce.
		{"runtime appended 'failed: ...'", "edit_file failed: file not found", true},
		{"json parse error pattern", "error: unexpected character 'x'", true},
		{"policy denial", "denied by command policy: rm *", true},
		{"path not found suffix", "config.toml: not found", true},
		{"starts with 'error'", "error opening file", true},
		{"starts with 'failed'", "failed to spawn shell", true},

		// Legitimate output that previously tripped the guard.
		{"run_command clean", "npm test", false},
		{"run_command echoing 'error' string", "echo 'error in tests'", false},
		{"command containing 'failed' word", "grep failed_login auth.log", false},
		{"git diff result mentioning errors file", "git diff src/error_handler.go", false},
		{"search hits with 'not found' substring", "search 'phrase not found in body'", false},
		{"empty summary", "", false},
		{"benign muted output", "0 results", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isToolFailureSummary(tc.summary); got != tc.want {
				t.Errorf("isToolFailureSummary(%q) = %v, want %v", tc.summary, got, tc.want)
			}
		})
	}
}
