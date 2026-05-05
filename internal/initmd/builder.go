// Package initmd builds an AGENTS.md document from the workspace
// snapshot.
//
// The output is intentionally deterministic — every section is derived
// from the projectstate.Snapshot (already cached, repo-aware) plus a
// few small file probes (package.json scripts, Makefile targets,
// Cargo.toml, README first lines). No LLM call. Producing a plain,
// repeatable result keeps AGENTS.md prompt-cache-friendly: re-running
// /init refresh on the same git head yields byte-identical bytes, so
// the tier-A cache (internal/context/builder.go) does not invalidate.
//
// A trailing HTML comment (forgeGeneratedMarker) lets future tools
// detect "this file was written by /init" without parsing the body.
// Any AGENTS.md missing the marker is presumed user-authored and
// /init refuses to overwrite it without the explicit `refresh` verb.
package initmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge/internal/projectstate"
)

// forgeGeneratedMarker is the HTML comment we append to every AGENTS.md
// /init writes. Detection is substring-based so re-edits that preserve
// the marker still count as "ours to overwrite". A user wanting to
// take ownership of the file just deletes the marker line.
const forgeGeneratedMarker = "<!-- forge-init: generated"

// BuildOptions controls Build's behaviour. Smart is reserved for the
// LLM-augmented variant (capa B in the plan) and is currently a no-op
// — Capa A alone produces a useful AGENTS.md, so the deterministic
// path ships first.
type BuildOptions struct {
	Smart bool
}

// Build assembles an AGENTS.md document from the workspace at cwd.
// Returns the rendered markdown string. Errors are returned only for
// I/O failures inside the projectstate snapshot path; missing optional
// inputs (no README, no package.json, etc.) degrade silently.
func Build(ctx context.Context, cwd string, ps *projectstate.Service, opts BuildOptions) (string, error) {
	if ps == nil {
		return "", fmt.Errorf("initmd: project state service is required")
	}
	snap, err := ps.EnsureSnapshot(ctx, cwd)
	if err != nil {
		return "", fmt.Errorf("initmd: snapshot: %w", err)
	}

	summary := readReadmeSummary(cwd)
	commands := detectCommands(cwd, snap)

	return render(snap, summary, commands), nil
}

// IsForgeGenerated reports whether the given AGENTS.md content was
// written by Forge's /init command. Used by the slash handler to
// distinguish user-authored files (refuse to overwrite) from previous
// /init outputs (safe to overwrite).
func IsForgeGenerated(content string) bool {
	return strings.Contains(content, forgeGeneratedMarker)
}

// commands is the detected set of project commands. Empty values are
// rendered as omitted lines so the output stays clean for repos
// missing one or more conventions.
type commands struct {
	Test  string
	Build string
	Lint  string
	Fmt   string
	Dev   string
}

func render(snap projectstate.Snapshot, summary string, cmds commands) string {
	var b strings.Builder

	b.WriteString("# AGENTS.md\n\n")

	b.WriteString("## Project\n\n")
	if summary != "" {
		b.WriteString(summary)
	} else if len(snap.Languages) > 0 {
		fmt.Fprintf(&b, "Repository at `%s` — %s.", filepath.Base(snap.RepoRoot), strings.Join(snap.Languages, ", "))
	} else {
		b.WriteString("Describe how this repository is built, tested, and edited.")
	}
	b.WriteString("\n\n")

	if len(snap.Languages) > 0 || len(snap.PackageMgrs) > 0 || len(snap.Manifests) > 0 {
		b.WriteString("## Stack\n\n")
		if len(snap.Languages) > 0 {
			fmt.Fprintf(&b, "- Languages: %s\n", strings.Join(snap.Languages, ", "))
		}
		if len(snap.PackageMgrs) > 0 {
			fmt.Fprintf(&b, "- Package managers: %s\n", strings.Join(snap.PackageMgrs, ", "))
		}
		if len(snap.Manifests) > 0 {
			fmt.Fprintf(&b, "- Manifests: %s\n", strings.Join(snap.Manifests, ", "))
		}
		b.WriteString("\n")
	}

	if cmds.Test != "" || cmds.Build != "" || cmds.Lint != "" || cmds.Fmt != "" || cmds.Dev != "" {
		b.WriteString("## Commands\n\n")
		if cmds.Test != "" {
			fmt.Fprintf(&b, "- Test: `%s`\n", cmds.Test)
		}
		if cmds.Build != "" {
			fmt.Fprintf(&b, "- Build: `%s`\n", cmds.Build)
		}
		if cmds.Lint != "" {
			fmt.Fprintf(&b, "- Lint: `%s`\n", cmds.Lint)
		}
		if cmds.Fmt != "" {
			fmt.Fprintf(&b, "- Format: `%s`\n", cmds.Fmt)
		}
		if cmds.Dev != "" {
			fmt.Fprintf(&b, "- Dev: `%s`\n", cmds.Dev)
		}
		b.WriteString("\n")
	}

	if len(snap.EntryPoints) > 0 || len(snap.TopLevel) > 0 {
		b.WriteString("## Architecture\n\n")
		if len(snap.EntryPoints) > 0 {
			fmt.Fprintf(&b, "- Entry points: %s\n", strings.Join(snap.EntryPoints, ", "))
		}
		if len(snap.TopLevel) > 0 {
			top := snap.TopLevel
			if len(top) > 12 {
				top = append(top[:12:12], "…")
			}
			fmt.Fprintf(&b, "- Top level: %s\n", strings.Join(top, ", "))
		}
		b.WriteString("\n")
	}

	if len(snap.TestHints) > 0 {
		b.WriteString("## Tests\n\n")
		fmt.Fprintf(&b, "- Hints: %s\n\n", strings.Join(snap.TestHints, ", "))
	}

	b.WriteString("## Rules\n\n")
	b.WriteString("- Keep changes small and reversible.\n")
	b.WriteString("- Prefer patches over full-file rewrites.\n\n")

	fmt.Fprintf(&b, "%s %s from snapshot %s -->\n",
		forgeGeneratedMarker,
		time.Now().UTC().Format(time.RFC3339),
		shortGitHead(snap.GitHead),
	)

	return b.String()
}

func shortGitHead(head string) string {
	if head == "" {
		return "(no-git)"
	}
	if len(head) > 12 {
		return head[:12]
	}
	return head
}

// readReadmeSummary returns the first non-empty, non-heading line of a
// top-level README. Bounded to 200 chars so a verbose README intro
// does not bloat AGENTS.md (which sits in tier-A on every prompt).
func readReadmeSummary(cwd string) string {
	for _, name := range []string{"README.md", "README.MD", "Readme.md", "readme.md", "README"} {
		path := filepath.Join(cwd, name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<!") || strings.HasPrefix(line, "[!") {
				continue
			}
			f.Close()
			if len(line) > 200 {
				line = line[:200] + "…"
			}
			return line
		}
		f.Close()
	}
	return ""
}

// detectCommands probes the workspace for canonical command strings.
// Each backend is best-effort: if parsing fails or the input is
// missing, the corresponding field stays empty and the renderer omits
// the line. Order matters: when multiple backends define the same
// concept (e.g. both go.mod and a Makefile have a "test" entry),
// later backends override earlier ones because the project-specific
// Makefile usually reflects the maintainer's intent better than the
// generic language default.
func detectCommands(cwd string, snap projectstate.Snapshot) commands {
	c := commands{}

	for _, lang := range snap.Languages {
		switch lang {
		case "Go":
			c.Test = "go test ./..."
			c.Build = "go build ./..."
			c.Lint = "go vet ./..."
			c.Fmt = "gofmt -w ."
		case "Rust":
			c.Test = "cargo test"
			c.Build = "cargo build"
			c.Lint = "cargo clippy"
			c.Fmt = "cargo fmt"
		case "Python":
			c.Test = "pytest"
		}
	}

	for _, m := range snap.Manifests {
		switch m {
		case "package.json":
			applyPackageJSON(filepath.Join(cwd, m), &c)
		case "Makefile":
			applyMakefile(filepath.Join(cwd, m), &c)
		}
	}

	return c
}

func applyPackageJSON(path string, c *commands) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}
	if pkg.Scripts == nil {
		return
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := pkg.Scripts[k]; ok && strings.TrimSpace(v) != "" {
				return "npm run " + k
			}
		}
		return ""
	}
	if v := pick("test"); v != "" {
		c.Test = v
	}
	if v := pick("build"); v != "" {
		c.Build = v
	}
	if v := pick("lint"); v != "" {
		c.Lint = v
	}
	if v := pick("format", "fmt", "prettier"); v != "" {
		c.Fmt = v
	}
	if v := pick("dev", "start"); v != "" {
		c.Dev = v
	}
}

// applyMakefile reads Makefile targets and overrides any command
// already detected when a same-named target exists. Parses the
// `target:` lines only — recipes themselves are ignored. Detection
// stops at the first 200 lines to keep large generated Makefiles
// from dominating cost.
func applyMakefile(path string, c *commands) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	targets := map[string]bool{}
	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() && lines < 200 {
		lines++
		line := scanner.Text()
		if !strings.Contains(line, ":") {
			continue
		}
		// Target lines start at column 0 (no leading whitespace).
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ") {
			continue
		}
		head := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		// Skip phony declarations and assignments.
		if strings.Contains(head, "=") || strings.HasPrefix(head, ".") {
			continue
		}
		// Strip pattern targets like "%.o".
		if strings.ContainsAny(head, "%$()") {
			continue
		}
		for _, name := range strings.Fields(head) {
			targets[name] = true
		}
	}
	apply := func(field *string, names ...string) {
		for _, n := range names {
			if targets[n] {
				*field = "make " + n
				return
			}
		}
	}
	apply(&c.Test, "test", "tests", "check")
	apply(&c.Build, "build", "all", "compile")
	apply(&c.Lint, "lint", "vet")
	apply(&c.Fmt, "fmt", "format")
	apply(&c.Dev, "dev", "run", "serve")
}

