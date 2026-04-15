// Package projectstate builds and caches a compact snapshot of a repo's
// structure and technology stack so the model can answer structural
// questions without re-walking the tree every session.
package projectstate

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshot is a compact, serializable view of the repository. Kept small on
// purpose: it is injected into YARN context, so cost matters.
type Snapshot struct {
	RepoRoot     string    `json:"repo_root"`
	GitBranch    string    `json:"git_branch,omitempty"`
	GitRemote    string    `json:"git_remote,omitempty"`
	GitHead      string    `json:"git_head,omitempty"`
	Languages    []string  `json:"languages,omitempty"`
	PackageMgrs  []string  `json:"package_managers,omitempty"`
	Manifests    []string  `json:"manifests,omitempty"`
	TopLevel     []string  `json:"top_level,omitempty"`
	Tree         []string  `json:"tree,omitempty"`
	TestHints    []string  `json:"test_hints,omitempty"`
	EntryPoints  []string  `json:"entry_points,omitempty"`
	Generated    time.Time `json:"generated"`
}

// Marshal serializes the snapshot to JSON for storage.
func (s Snapshot) Marshal() (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Unmarshal parses a stored snapshot.
func Unmarshal(raw string) (Snapshot, error) {
	var s Snapshot
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}

// Summary renders a compact, model-friendly text block (~1-2KB).
func (s Snapshot) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project snapshot (cached)\n")
	fmt.Fprintf(&b, "Repo: %s\n", s.RepoRoot)
	if s.GitBranch != "" {
		fmt.Fprintf(&b, "Git: branch=%s head=%s", s.GitBranch, shortHead(s.GitHead))
		if s.GitRemote != "" {
			fmt.Fprintf(&b, " remote=%s", s.GitRemote)
		}
		b.WriteByte('\n')
	}
	if len(s.Languages) > 0 {
		fmt.Fprintf(&b, "Languages: %s\n", strings.Join(s.Languages, ", "))
	}
	if len(s.PackageMgrs) > 0 {
		fmt.Fprintf(&b, "Package managers: %s\n", strings.Join(s.PackageMgrs, ", "))
	}
	if len(s.Manifests) > 0 {
		fmt.Fprintf(&b, "Manifests: %s\n", strings.Join(s.Manifests, ", "))
	}
	if len(s.EntryPoints) > 0 {
		fmt.Fprintf(&b, "Entry points: %s\n", strings.Join(s.EntryPoints, ", "))
	}
	if len(s.TestHints) > 0 {
		fmt.Fprintf(&b, "Test hints: %s\n", strings.Join(s.TestHints, ", "))
	}
	if len(s.TopLevel) > 0 {
		fmt.Fprintf(&b, "Top level: %s\n", strings.Join(s.TopLevel, ", "))
	}
	if len(s.Tree) > 0 {
		b.WriteString("Tree (depth 2):\n")
		for _, line := range s.Tree {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortHead(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// Scan walks the repo rooted at cwd and produces a Snapshot. Walks only two
// levels deep, skips common junk directories, and caps entries to keep the
// result compact.
func Scan(cwd string) (Snapshot, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{RepoRoot: filepath.ToSlash(abs), Generated: time.Now().UTC()}

	branch, head, remote := gitInfo(abs)
	snap.GitBranch = branch
	snap.GitHead = head
	snap.GitRemote = remote

	top, err := os.ReadDir(abs)
	if err != nil {
		return snap, err
	}
	manifests := detectManifests(abs, top)
	snap.Manifests = manifests
	snap.Languages = languagesFromManifests(manifests)
	snap.PackageMgrs = packageMgrsFromManifests(manifests)
	snap.EntryPoints = detectEntryPoints(abs, top, snap.Languages)
	snap.TestHints = detectTestHints(abs, top)

	for _, e := range top {
		name := e.Name()
		if skipTopEntry(name) {
			continue
		}
		if e.IsDir() {
			snap.TopLevel = append(snap.TopLevel, name+"/")
		} else {
			snap.TopLevel = append(snap.TopLevel, name)
		}
	}
	sort.Strings(snap.TopLevel)
	if len(snap.TopLevel) > 40 {
		snap.TopLevel = append(snap.TopLevel[:40], "…")
	}

	snap.Tree = buildTree(abs, top)
	return snap, nil
}

func gitInfo(root string) (branch, head, remote string) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", "", ""
	}
	branch = runGit(root, "rev-parse", "--abbrev-ref", "HEAD")
	head = runGit(root, "rev-parse", "HEAD")
	remote = runGit(root, "config", "--get", "remote.origin.url")
	return
}

func runGit(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectManifests returns a sorted list of detected manifest filenames.
func detectManifests(root string, top []fs.DirEntry) []string {
	known := []string{
		"go.mod", "package.json", "pnpm-workspace.yaml", "yarn.lock",
		"pyproject.toml", "requirements.txt", "Pipfile", "poetry.lock",
		"Cargo.toml", "Gemfile", "build.gradle", "build.gradle.kts",
		"pom.xml", "composer.json", "Makefile", "CMakeLists.txt",
		"Dockerfile", "docker-compose.yml", "docker-compose.yaml",
		".tool-versions", ".nvmrc",
	}
	index := map[string]bool{}
	for _, e := range top {
		if !e.IsDir() {
			index[e.Name()] = true
		}
	}
	var found []string
	for _, name := range known {
		if index[name] {
			found = append(found, name)
		}
	}
	return found
}

func languagesFromManifests(manifests []string) []string {
	langs := map[string]struct{}{}
	for _, m := range manifests {
		switch m {
		case "go.mod":
			langs["Go"] = struct{}{}
		case "package.json", "pnpm-workspace.yaml", "yarn.lock", ".nvmrc":
			langs["JavaScript/TypeScript"] = struct{}{}
		case "pyproject.toml", "requirements.txt", "Pipfile", "poetry.lock":
			langs["Python"] = struct{}{}
		case "Cargo.toml":
			langs["Rust"] = struct{}{}
		case "Gemfile":
			langs["Ruby"] = struct{}{}
		case "build.gradle", "build.gradle.kts", "pom.xml":
			langs["JVM (Java/Kotlin)"] = struct{}{}
		case "composer.json":
			langs["PHP"] = struct{}{}
		case "CMakeLists.txt":
			langs["C/C++"] = struct{}{}
		}
	}
	return sortedKeys(langs)
}

func packageMgrsFromManifests(manifests []string) []string {
	mgrs := map[string]struct{}{}
	for _, m := range manifests {
		switch m {
		case "go.mod":
			mgrs["go mod"] = struct{}{}
		case "package.json":
			mgrs["npm"] = struct{}{}
		case "pnpm-workspace.yaml":
			mgrs["pnpm"] = struct{}{}
		case "yarn.lock":
			mgrs["yarn"] = struct{}{}
		case "poetry.lock":
			mgrs["poetry"] = struct{}{}
		case "Pipfile":
			mgrs["pipenv"] = struct{}{}
		case "Cargo.toml":
			mgrs["cargo"] = struct{}{}
		case "Gemfile":
			mgrs["bundler"] = struct{}{}
		case "build.gradle", "build.gradle.kts":
			mgrs["gradle"] = struct{}{}
		case "pom.xml":
			mgrs["maven"] = struct{}{}
		case "composer.json":
			mgrs["composer"] = struct{}{}
		}
	}
	return sortedKeys(mgrs)
}

func detectEntryPoints(root string, top []fs.DirEntry, langs []string) []string {
	candidates := []string{"main.go", "cmd", "src", "app", "index.js", "index.ts", "server.js", "server.ts", "pyproject.toml"}
	var found []string
	for _, name := range candidates {
		path := filepath.Join(root, name)
		if _, err := os.Stat(path); err == nil {
			found = append(found, name)
		}
	}
	return found
}

func detectTestHints(root string, top []fs.DirEntry) []string {
	hints := map[string]struct{}{}
	for _, e := range top {
		name := e.Name()
		lower := strings.ToLower(name)
		switch {
		case lower == "tests" || lower == "test" || lower == "__tests__":
			hints[name+"/"] = struct{}{}
		case lower == "jest.config.js" || lower == "jest.config.ts" || lower == "vitest.config.ts":
			hints["jest/vitest"] = struct{}{}
		case lower == "pytest.ini" || lower == "tox.ini":
			hints["pytest"] = struct{}{}
		}
	}
	// Go convention: presence of *_test.go anywhere.
	if hasGoTests(root) {
		hints["go test"] = struct{}{}
	}
	return sortedKeys(hints)
}

func hasGoTests(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func buildTree(root string, top []fs.DirEntry) []string {
	var lines []string
	dirs := make([]fs.DirEntry, 0)
	for _, e := range top {
		if e.IsDir() && !skipTopEntry(e.Name()) {
			dirs = append(dirs, e)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })
	for _, d := range dirs {
		lines = append(lines, d.Name()+"/")
		children, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		var names []string
		for _, c := range children {
			if skipTopEntry(c.Name()) {
				continue
			}
			suffix := ""
			if c.IsDir() {
				suffix = "/"
			}
			names = append(names, c.Name()+suffix)
		}
		sort.Strings(names)
		if len(names) > 12 {
			names = append(names[:12], "…")
		}
		for _, n := range names {
			lines = append(lines, "  "+d.Name()+"/"+n)
		}
		if len(lines) > 120 {
			lines = append(lines, "… (tree truncated)")
			break
		}
	}
	return lines
}

func skipTopEntry(name string) bool {
	switch name {
	case ".git", ".forge", ".idea", ".vscode", "node_modules", "dist", "build",
		"target", ".venv", "venv", "__pycache__", ".next", ".cache", ".gradle",
		".DS_Store":
		return true
	}
	return strings.HasPrefix(name, ".") && name != ".env.example"
}

func skipWalkDir(name string) bool {
	return skipTopEntry(name)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
