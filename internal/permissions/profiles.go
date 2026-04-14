package permissions

// Profile defines a named permission configuration.
type Profile struct {
	Name        string
	Description string
	Policy      CommandPolicy
}

// DefaultProfiles returns the 4 built-in permission profiles.
func DefaultProfiles() map[string]Profile {
	return map[string]Profile{
		"safe": {
			Name:        "safe",
			Description: "Commands require approval; edits still follow agent mode approval.",
			Policy: CommandPolicy{
				Allow: nil,
				Ask:   []string{"*"},
				Deny:  defaultDenyPatterns(),
			},
		},
		"normal": {
			Name:        "normal",
			Description: "Default command policy. Safe commands allowed, others ask.",
			Policy:      DefaultCommandPolicy(),
		},
		"fast": {
			Name:        "fast",
			Description: "Broader command allowlist. File edits and patches still require approval.",
			Policy: CommandPolicy{
				Allow: append(defaultAllowPatterns(), "npm run *", "pnpm run *", "make *", "cargo test *", "python -m pytest *"),
				Ask:   []string{"*"},
				Deny:  defaultDenyPatterns(),
			},
		},
		"yolo": {
			Name:        "yolo",
			Description: "Commands allowed except denylist. File edits and patches still follow agent policy.",
			Policy: CommandPolicy{
				Allow: []string{"*"},
				Ask:   nil,
				Deny:  defaultDenyPatterns(),
			},
		},
	}
}

// GetProfile returns a profile by name.
func GetProfile(name string) (Profile, bool) {
	p, ok := DefaultProfiles()[name]
	return p, ok
}

// ProfileNames returns the available profile names in order.
func ProfileNames() []string {
	return []string{"safe", "normal", "fast", "yolo"}
}

func defaultDenyPatterns() []string {
	return []string{
		"rm -rf *", "rm -r *", "rm *",
		"del *", "erase *", "Remove-Item *",
		"git reset --hard *", "git checkout -- *",
		"curl *", "wget *", "Invoke-WebRequest *", "iwr *",
	}
}

func defaultAllowPatterns() []string {
	return []string{
		"go test *", "go test ./...",
		"npm test", "pnpm test", "yarn test",
		"git status", "git status *",
		"git diff", "git diff *", "git log *",
	}
}
