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
