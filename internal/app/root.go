package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"forge/internal/config"
	"forge/internal/hooks"
	"forge/internal/llm"
	"forge/internal/mcp"
	"forge/internal/plugins"
	"forge/internal/session"
	"forge/internal/skills"
	"forge/internal/tools"
	"forge/internal/tui"

	"github.com/spf13/cobra"
)

func NewRootCommand() *cobra.Command {
	var cwd string
	var resume string

	cmd := &cobra.Command{
		Use:   "forge",
		Short: "Terminal workbench for coding agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cwd == "" {
				var err error
				cwd, err = os.Getwd()
				if err != nil {
					return err
				}
			}

			// Permission prompt: ask if .forge/ doesn't exist.
			forgeDir := filepath.Join(cwd, ".forge")
			if _, statErr := os.Stat(forgeDir); os.IsNotExist(statErr) {
				fmt.Fprintf(cmd.OutOrStdout(), "Forge wants to operate in: %s\n", cwd)
				fmt.Fprintf(cmd.OutOrStdout(), "This will create a .forge/ directory for config, sessions, and context.\n")
				fmt.Fprintf(cmd.OutOrStdout(), "Allow? [Y/n] ")
				var answer string
				fmt.Scanln(&answer)
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer == "n" || answer == "no" {
					fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
					return nil
				}
				// Auto-init.
				if err := os.MkdirAll(forgeDir, 0o755); err != nil {
					return err
				}
				for _, sub := range []string{"sessions", "yarn", "tools", "skills", "plugins", filepath.Join("cache", "skills")} {
					_ = os.MkdirAll(filepath.Join(forgeDir, sub), 0o755)
				}
				_ = os.MkdirAll(filepath.Join(cwd, ".agents", "skills"), 0o755)
				fmt.Fprintf(cmd.OutOrStdout(), "Initialized .forge/ in %s\n\n", cwd)
			}

			cfg, err := config.Load(cwd)
			if err != nil {
				return err
			}

			registry := tools.NewRegistry()
			tools.RegisterBuiltins(registry)
			if err := tools.RegisterExternal(registry, cwd); err != nil {
				return err
			}

			mcpManager := mcp.NewManager(cwd, registry)
			if err := mcpManager.Start(context.Background()); err != nil {
				fmt.Fprintf(os.Stderr, "mcp: %s\n", err)
			}

			providers := llm.NewRegistry()
			providers.Register(llm.NewOpenAICompatible("openai_compatible", cfg.Providers.OpenAICompatible))
			providers.Register(llm.NewOpenAICompatible("lmstudio", cfg.Providers.LMStudio))
			sessionStore, err := openSession(cwd, resume)
			if err != nil {
				return err
			}

			hookRunner := hooks.NewRunner(cwd)

			// Load MCP and hooks from discovered plugins.
			pluginMgr := plugins.NewManager(cwd)
			enabledState := plugins.LoadEnabledState(cwd)
			if discoveredPlugins, err := pluginMgr.Discover(); err == nil {
				for _, p := range discoveredPlugins {
					if enabledState.Disabled[p.Name] {
						continue
					}
					if mcpPath := p.MCPConfigPath(); mcpPath != "" {
						if err := mcpManager.StartFromFile(context.Background(), mcpPath); err != nil {
							fmt.Fprintf(os.Stderr, "plugin %s mcp: %s\n", p.Name, err)
						}
					}
					if hooksPath := p.HooksPath(); hooksPath != "" {
						if err := hookRunner.Load(hooksPath); err != nil {
							fmt.Fprintf(os.Stderr, "plugin %s hooks: %s\n", p.Name, err)
						}
					}
				}
			}

			app := tui.New(tui.Options{
				CWD:       cwd,
				Config:    cfg,
				Tools:     registry,
				Providers: providers,
				Session:   sessionStore,
				Skills: skills.NewManager(cwd, skills.Options{
					CLI:          cfg.Skills.CLI,
					DirectoryURL: cfg.Skills.DirectoryURL,
					Repositories: cfg.Skills.Repositories,
					Agent:        cfg.Skills.Agent,
					InstallScope: cfg.Skills.InstallScope,
					Copy:         cfg.Skills.Copy,
					Installer:    cfg.Skills.Installer,
				}),
				Plugins: pluginMgr,
				MCP:     mcpManager,
				Hooks:   hookRunner,
			})

			return app.Run(context.Background())
		},
	}

	cmd.PersistentFlags().StringVar(&cwd, "cwd", "", "workspace directory")
	cmd.PersistentFlags().StringVar(&resume, "resume", "", "resume an existing session id, or latest")
	cmd.AddCommand(newVersionCommand())
	cmd.AddCommand(newToolsCommand())
	cmd.AddCommand(newInitCommand())
	cmd.AddCommand(newPluginCommand())

	return cmd
}

func openSession(cwd, resume string) (*session.Store, error) {
	switch resume {
	case "":
		return session.New(cwd)
	case "latest":
		return session.OpenLatest(cwd)
	default:
		return session.Open(cwd, resume)
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "forge dev")
		},
	}
}
