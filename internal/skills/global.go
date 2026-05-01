package skills

import (
	"os"
	"path/filepath"

	"forge/internal/globalconfig"
)

// NewGlobalManager constructs a Manager configured for the Hub: it reads
// from and writes to the user-level paths under ~/.codex/ instead of any
// workspace's .forge/. Used by the Hub Skills browser so the user can
// install skills without opening a workspace.
//
// Behavior differences from a workspace Manager:
//
//   - cwd is the global skills install dir (~/.codex/skills); ScanLocal()
//     and LoadSkill() therefore find globally-installed skills directly.
//   - CacheDir defaults to ~/.codex/cache/skills so the directory + repo
//     scrape is shared across every workspace the user opens.
//   - InstallScope defaults to "user" so installations land in the global
//     dir rather than the workspace.
//   - PluginSkillDirs is intentionally NOT carried over -- plugins are
//     workspace-specific (discovered via .forge/plugins or .claude/plugins
//     in the cwd), and the Hub does not have a cwd.
//
// `opts` lets a caller override CLI, DirectoryURL, etc. Fields it leaves
// blank get the same canonical defaults as a workspace Manager would.
func NewGlobalManager(opts Options) *Manager {
	opts = normalizeOptions(opts)
	if opts.CacheDir == "" {
		opts.CacheDir = globalconfig.CacheDir()
	}
	if opts.InstallDir == "" {
		opts.InstallDir = globalconfig.SkillsInstallDir()
	}
	// Hub-installed skills always go to the user dir; the workspace concept
	// does not apply when no workspace is open.
	opts.InstallScope = "user"
	// PluginSkillDirs only makes sense per-workspace.
	opts.PluginSkillDirs = nil
	return &Manager{cwd: opts.InstallDir, options: opts}
}

// EnsureGlobalDirs makes sure the global cache and install dirs exist so
// ScanLocal() finds an empty list rather than a missing-dir error on the
// first Hub launch.
func EnsureGlobalDirs() error {
	for _, dir := range []string{globalconfig.SkillsInstallDir(), globalconfig.CacheDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// installedSourceForGlobal is here so the heuristic in registry.go has an
// extra path to consider when the Hub's global install dir is queried.
// Currently the existing installedSource() string-matches "/plugins/",
// ".agents/skills", ".codex/skills"; the global install dir matches the
// .codex/skills branch and reports Source="global", so no change needed
// today. This file exists as the place to add Hub-specific source
// detection later.
var _ = filepath.Join
