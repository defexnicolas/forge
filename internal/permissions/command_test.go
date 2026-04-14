package permissions

import "testing"

func TestDefaultCommandPolicy(t *testing.T) {
	policy := DefaultCommandPolicy()
	tests := []struct {
		command  string
		decision Decision
	}{
		{"go test ./...", Allow},
		{"go test ./internal/agent", Allow},
		{"git diff", Allow},
		{"npm test", Ask},
		{"yarn test --watch=false", Ask},
		{"rm -rf .", Deny},
		{"git reset --hard HEAD", Deny},
		{"curl https://example.com", Deny},
		{"python script.py", Ask},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			decision, _ := policy.Decide(tt.command)
			if decision != tt.decision {
				t.Fatalf("expected %s, got %s", tt.decision, decision)
			}
		})
	}
}
