package main

import (
	"fmt"
	"os"

	"forge/internal/app"
	"forge/internal/buildinfo"
)

// These are populated at link time via -ldflags. See scripts/build.ps1 and
// scripts/build.sh. When forge is built with `go run`, `go install` (without
// flags), or `go build` straight, they stay empty and the updater silently
// disables itself — no source repo means no remote to compare against.
var (
	Version    = "dev"
	BuildSHA   = ""
	BuildTime  = ""
	SourceRepo = ""
)

func main() {
	buildinfo.Set(buildinfo.Info{
		Version:    Version,
		BuildSHA:   BuildSHA,
		BuildTime:  BuildTime,
		SourceRepo: SourceRepo,
	})
	if err := app.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
