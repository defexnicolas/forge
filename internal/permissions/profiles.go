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
		"trusted": {
			Name:        "trusted",
			Description: "Installers, downloads, and Docker allowed. Strictly destructive ops still denied.",
			Policy: CommandPolicy{
				Allow: append(defaultAllowPatterns(), trustedAllowPatterns()...),
				Ask:   []string{"*"},
				Deny:  trustedDenyPatterns(),
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
	return []string{"safe", "normal", "fast", "trusted", "yolo"}
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

// trustedAllowPatterns extends the default allowlist with installers,
// network downloads, and container tooling. Used by the "trusted" profile
// only — `safe`, `normal`, and `fast` keep curl/wget on the denylist.
func trustedAllowPatterns() []string {
	return []string{
		"npm install *", "npm i *",
		"pnpm install *", "pnpm i *", "pnpm add *", "pnpm dlx *",
		"yarn install *", "yarn add *",
		"npx *",
		"pip install *", "pip3 install *",
		"cargo install *",
		"go install *", "go get *",
		"apt-get install *", "apt install *",
		"brew install *",
		"choco install *",
		"winget install *",
		"docker *", "docker-compose *",
		"curl *", "wget *",
		"Invoke-WebRequest *", "iwr *",
	}
}

// trustedDenyPatterns is the strict-destructive denylist for the "trusted"
// profile. It deliberately omits curl/wget so the profile's downloads work
// (Deny wins over Allow in CommandPolicy.Decide). It also omits plain `rm *`
// so workspace cleanup is allowed; only filesystem-root or home-dir wipes
// and irreversible repo operations are blocked.
func trustedDenyPatterns() []string {
	return []string{
		"rm -rf /*", "rm -rf /",
		"rm -rf ~*", "rm -rf ~/*",
		"rm -rf $HOME*", "rm -rf $HOME/*",
		"del /f /s /q *",
		"Remove-Item -Recurse -Force /*",
		"git reset --hard *",
		"git push --force *", "git push -f *",
		"mkfs *", "mkfs.* *",
		"diskpart *",
		"format *",
		"dd if=* of=/dev/*",
		"chmod -R 777 /",
		"shutdown *",
		"reboot *",
	}
}
