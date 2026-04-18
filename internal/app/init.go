package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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

			// Create .forge directory structure.
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
				fmt.Fprintf(out, "  created %s/\n", dir)
			}

			// Write config.toml if it doesn't exist.
			configPath := filepath.Join(cwd, ".forge", "config.toml")
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				if err := os.WriteFile(configPath, []byte(defaultConfigTOML), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(out, "  created .forge/config.toml\n")
			} else {
				fmt.Fprintf(out, "  exists  .forge/config.toml\n")
			}

			// Write .forge/.gitignore to exclude ephemeral data.
			forgeGitignorePath := filepath.Join(cwd, ".forge", ".gitignore")
			if _, err := os.Stat(forgeGitignorePath); os.IsNotExist(err) {
				if err := os.WriteFile(forgeGitignorePath, []byte(forgeGitignore), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(out, "  created .forge/.gitignore\n")
			}

			// Write AGENTS.md at root if it doesn't exist.
			agentsPath := filepath.Join(cwd, "AGENTS.md")
			if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
				if err := os.WriteFile(agentsPath, []byte(defaultAgentsMD), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(out, "  created AGENTS.md\n")
			} else {
				fmt.Fprintf(out, "  exists  AGENTS.md\n")
			}

			// Optionally initialize git.
			if initGit {
				gitDir := filepath.Join(cwd, ".git")
				if _, err := os.Stat(gitDir); os.IsNotExist(err) {
					gitCmd := exec.Command("git", "init")
					gitCmd.Dir = cwd
					gitCmd.Stdout = out
					gitCmd.Stderr = cmd.ErrOrStderr()
					if err := gitCmd.Run(); err != nil {
						fmt.Fprintf(out, "  warning: git init failed: %s\n", err)
					} else {
						fmt.Fprintf(out, "  initialized git repository\n")
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
	cmd.Flags().BoolVar(&initGit, "git", false, "also run git init if not already a repo")
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
supports_tools = false

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
