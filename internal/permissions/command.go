package permissions

import (
	"path/filepath"
	"strings"
)

type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
	Deny  Decision = "deny"
)

type CommandPolicy struct {
	Allow []string
	Ask   []string
	Deny  []string
}

func DefaultCommandPolicy() CommandPolicy {
	return CommandPolicy{
		Allow: []string{
			"go test ./...",
			"go test *",
			"git status",
			"git status *",
			"git diff",
			"git diff *",
		},
		Ask: []string{
			"npm test",
			"npm test *",
			"pnpm test",
			"pnpm test *",
			"yarn test",
			"yarn test *",
		},
		Deny: []string{
			"rm *",
			"del *",
			"erase *",
			"Remove-Item *",
			"git reset *",
			"git checkout -- *",
			"curl *",
			"wget *",
			"Invoke-WebRequest *",
			"iwr *",
		},
	}
}

func (p CommandPolicy) Decide(command string) (Decision, string) {
	normalized := normalize(command)
	for _, pattern := range p.Deny {
		if matches(pattern, normalized) {
			return Deny, "denied by command policy: " + pattern
		}
	}
	for _, pattern := range p.Allow {
		if matches(pattern, normalized) {
			return Allow, "allowed by command policy: " + pattern
		}
	}
	for _, pattern := range p.Ask {
		if matches(pattern, normalized) {
			return Ask, "approval required by command policy: " + pattern
		}
	}
	return Ask, "approval required by command policy: unmatched command"
}

func (p CommandPolicy) Describe() string {
	var b strings.Builder
	b.WriteString("run_command policy\n")
	b.WriteString("allow:\n")
	for _, pattern := range p.Allow {
		b.WriteString("- " + pattern + "\n")
	}
	b.WriteString("ask:\n")
	for _, pattern := range p.Ask {
		b.WriteString("- " + pattern + "\n")
	}
	b.WriteString("deny:\n")
	for _, pattern := range p.Deny {
		b.WriteString("- " + pattern + "\n")
	}
	return strings.TrimSpace(b.String())
}

func normalize(command string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(command)), " ")
}

func matches(pattern, command string) bool {
	pattern = normalize(pattern)
	if ok, _ := filepath.Match(strings.ToLower(pattern), strings.ToLower(command)); ok {
		return true
	}
	if strings.HasSuffix(pattern, " *") {
		prefix := strings.TrimSuffix(pattern, " *")
		return strings.EqualFold(command, prefix) || strings.HasPrefix(strings.ToLower(command), strings.ToLower(prefix+" "))
	}
	return strings.EqualFold(pattern, command)
}
