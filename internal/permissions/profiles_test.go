package permissions

import (
	"strings"
	"testing"
)

func TestProfileDescriptionsDoNotPromisePatchAutoApproval(t *testing.T) {
	for _, name := range ProfileNames() {
		profile, ok := GetProfile(name)
		if !ok {
			t.Fatalf("missing profile %s", name)
		}
		description := strings.ToLower(profile.Description)
		if strings.Contains(description, "patches auto-approved") || strings.Contains(description, "auto-approved") {
			t.Fatalf("profile %s still promises patch auto-approval: %q", name, profile.Description)
		}
	}
}

func TestTrustedProfileAllowsInstallersAndDownloads(t *testing.T) {
	profile, ok := GetProfile("trusted")
	if !ok {
		t.Fatalf("trusted profile is missing")
	}
	cases := []struct {
		command string
		want    Decision
	}{
		{"npm install lodash", Allow},
		{"npm i react", Allow},
		{"pnpm install", Allow},
		{"pnpm add zod", Allow},
		{"yarn add react", Allow},
		{"npx create-next-app@latest myapp", Allow},
		{"pip install requests", Allow},
		{"cargo install ripgrep", Allow},
		{"docker run node:20 node --version", Allow},
		{"docker-compose up -d", Allow},
		{"curl https://example.com/installer.sh", Allow},
		{"wget https://example.com/file.tar.gz", Allow},
		{"Invoke-WebRequest -Uri https://example.com", Allow},
		{"rm -rf /", Deny},
		{"rm -rf ~/important", Deny},
		{"git push --force origin main", Deny},
		{"git push -f", Deny},
		{"git reset --hard HEAD~5", Deny},
		{"unknown-binary --flag", Ask},
	}
	for _, tc := range cases {
		got, _ := profile.Policy.Decide(tc.command)
		if got != tc.want {
			t.Errorf("trusted.Decide(%q) = %s, want %s", tc.command, got, tc.want)
		}
	}
}

func TestProfileNamesIncludesTrusted(t *testing.T) {
	names := ProfileNames()
	want := []string{"safe", "normal", "fast", "trusted", "yolo"}
	if len(names) != len(want) {
		t.Fatalf("ProfileNames() = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("ProfileNames()[%d] = %q, want %q", i, names[i], n)
		}
	}
}
