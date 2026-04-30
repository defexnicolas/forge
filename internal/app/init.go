package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"forge/internal/gitops"

	"github.com/spf13/cobra"
)

func newInitCommand() *cobra.Command {
	var initGit bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create starter Forge project files",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if err := ensureProjectScaffold(cwd, out, true); err != nil {
				return err
			}

			// Optionally initialize git.
			if initGit {
				result, err := gitops.EnsureRepo(cwd, gitops.DefaultBaselineCommitMessage)
				if err != nil {
					fmt.Fprintf(out, "  warning: git init failed: %s\n", err)
				} else if result.Initialized {
					fmt.Fprintf(out, "  initialized git repository\n")
					if result.BaselineCreated {
						fmt.Fprintf(out, "  created baseline commit %s\n", result.BaselineCommitID)
					}
				} else {
					fmt.Fprintf(out, "  exists  .git/ (already a git repo)\n")
				}
			}

			fmt.Fprintf(out, "\nForge initialized in %s\n", cwd)
			fmt.Fprintf(out, "Run `forge` to start the interactive session.\n")
			return nil
		},
	}

	var showEnv bool
	cmd.Flags().BoolVar(&initGit, "git", true, "initialize git and create a baseline commit when needed")
	cmd.Flags().BoolVar(&showEnv, "env", false, "print instructions to add forge to PATH")

	cmd.PostRunE = func(cmd *cobra.Command, args []string) error {
		if showEnv {
			cwd, _ := os.Getwd()
			out := cmd.OutOrStdout()
			forgeDir := filepath.Join(cwd, ".forge")

			// Create env.ps1
			ps1 := fmt.Sprintf("$env:PATH += \";%s\"\nWrite-Host \"forge added to PATH for this session\"\n", cwd)
			ps1Path := filepath.Join(forgeDir, "env.ps1")
			_ = os.WriteFile(ps1Path, []byte(ps1), 0o644)

			// Create env.sh
			sh := fmt.Sprintf("#!/bin/sh\nexport PATH=\"$PATH:%s\"\necho \"forge added to PATH for this session\"\n", cwd)
			shPath := filepath.Join(forgeDir, "env.sh")
			_ = os.WriteFile(shPath, []byte(sh), 0o755)

			fmt.Fprintln(out, "\nCreated environment scripts:")
			fmt.Fprintf(out, "  %s\n", ps1Path)
			fmt.Fprintf(out, "  %s\n\n", shPath)
			fmt.Fprintln(out, "Usage:")
			fmt.Fprintf(out, "  PowerShell:  . %s\n", ps1Path)
			fmt.Fprintf(out, "  Bash/Zsh:    source %s\n\n", shPath)
			fmt.Fprintln(out, "For permanent PATH (PowerShell):")
			fmt.Fprintf(out, "  [System.Environment]::SetEnvironmentVariable('PATH', $env:PATH + ';%s', 'User')\n", cwd)
		}
		return nil
	}

	return cmd
}

func ensureProjectScaffold(cwd string, out io.Writer, verbose bool) error {
	dirs := []string{
		".forge",
		filepath.Join(".forge", "sessions"),
		filepath.Join(".forge", "yarn"),
		filepath.Join(".forge", "tools"),
		filepath.Join(".forge", "skills"),
		filepath.Join(".forge", "cache", "skills"),
		filepath.Join(".forge", "plugins"),
		filepath.Join(".agents", "skills"),
	}
	for _, dir := range dirs {
		fullPath := filepath.Join(cwd, dir)
		if err := os.MkdirAll(fullPath, 0o755); err != nil {
			return err
		}
		if verbose {
			fmt.Fprintf(out, "  created %s/\n", dir)
		}
	}
	if err := ensureFile(filepath.Join(cwd, ".forge", "config.toml"), defaultConfigTOML, out, verbose, ".forge/config.toml"); err != nil {
		return err
	}
	if err := ensureFile(filepath.Join(cwd, ".forge", ".gitignore"), forgeGitignore, out, verbose, ".forge/.gitignore"); err != nil {
		return err
	}
	if err := ensureFile(filepath.Join(cwd, "AGENTS.md"), defaultAgentsMD, out, verbose, "AGENTS.md"); err != nil {
		return err
	}
	return nil
}

func ensureFile(path, content string, out io.Writer, verbose bool, label string) error {
	if _, err := os.Stat(path); err == nil {
		if verbose {
			fmt.Fprintf(out, "  exists  %s\n", label)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	if verbose {
		fmt.Fprintf(out, "  created %s\n", label)
	}
	return nil
}

const defaultConfigTOML = `default_agent = "build"
approval_profile = "normal"

[providers.default]
name = "lmstudio"

[providers.openai_compatible]
type = "openai-compatible"
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
default_model = "gpt-5.4-mini"
supports_tools = true

[providers.lmstudio]
type = "openai-compatible"
base_url = "http://localhost:1234/v1"
api_key = "lm-studio"
default_model = "local-model"
supports_tools = true

[context]
engine = "yarn"
budget_tokens = 4500
auto_compact = true
model_context_tokens = 8192
reserve_output_tokens = 2000

[context.yarn]
profile = "9B"
max_nodes = 8
max_file_bytes = 12000
history_events = 12
pins = "always"
mentions = "always"
compact_events = 80
compact_transcript_chars = 50000

[context.task]
budget_tokens = 4000
max_nodes = 6
max_file_bytes = 8000
history_events = 4

[runtime]
request_timeout_seconds = 45
subagent_timeout_seconds = 90
task_timeout_seconds = 180
max_no_progress_steps = 3
max_empty_responses = 2
max_same_tool_failures = 2
max_consecutive_read_only = 6
max_planner_summary_steps = 2
max_builder_read_loops = 4
retry_on_provider_timeout = false

[git]
auto_init = true
create_baseline_commit = true
require_clean_or_snapshot = true
auto_stage_mutations = true
auto_commit = false
baseline_commit_message = "chore: initialize forge workspace baseline"
snapshot_commit_message = "chore: snapshot workspace before forge mutation"

[model_loading]
enabled = false
strategy = "single"
parallel_slots = 2

[build.subagents]
enabled = true
concurrency = 2
roles = ["explorer", "reviewer", "debug"]

[skills]
cli = "npx"
directory_url = "https://skills.sh/"
repositories = ["vercel-labs/agent-skills", "vercel-labs/skills"]
agent = "codex"
install_scope = "project"
copy = true

[plugins]
enabled = true
claude_compatible = true
`

const defaultAgentsMD = `# AGENTS.md

## Project

Describe how this repository is built, tested, and edited.

## Commands

- Test: fill this in.
- Format: fill this in.

## Rules

- Keep changes small and reversible.
- Prefer patches over full-file rewrites.
`

const forgeGitignore = `# Ephemeral session and context data
sessions/
yarn/
cache/
`
