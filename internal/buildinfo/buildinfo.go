// Package buildinfo carries link-time metadata (version, git SHA, source
// repo path) from cmd/forge/main.go to anywhere in the codebase that needs
// it. Putting it in its own package avoids a cycle: cmd/forge → internal/app
// → internal/tui → internal/updater would otherwise need to import back into
// cmd/forge to read the vars.
//
// Set() is called exactly once during main() before any other package reads
// the values. After that, Get() is safe to call from any goroutine.
package buildinfo

import "sync/atomic"

// Info is the immutable snapshot of build metadata.
type Info struct {
	Version    string
	BuildSHA   string
	BuildTime  string
	SourceRepo string
}

var stored atomic.Pointer[Info]

// Set records the build metadata. Intended to be called once from main().
// Calling it more than once silently overwrites — that's fine for tests.
func Set(info Info) {
	cp := info
	stored.Store(&cp)
}

// Get returns the stored Info or a zero value if Set was never called
// (e.g. when running under `go test` without main()).
func Get() Info {
	if p := stored.Load(); p != nil {
		return *p
	}
	return Info{Version: "dev"}
}

// HasSourceRepo reports whether the binary has an embedded source-repo path.
// When false, update checks must be silently disabled — there's no on-disk
// clone to compare against.
func HasSourceRepo() bool {
	return Get().SourceRepo != ""
}
