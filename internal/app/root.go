package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge/internal/config"
	"forge/internal/db"
	"forge/internal/gitops"
	"forge/internal/hooks"
	"forge/internal/llm"
	"forge/internal/mcp"
	"forge/internal/plugins"
	"forge/internal/projectstate"
	"forge/internal/session"
	"forge/internal/skills"
	"forge/internal/tools"
	"forge/internal/tui"

	"github.com/pelletier/go-toml/v2"
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
				if err := ensureProjectScaffold(cwd, cmd.OutOrStdout(), false); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Initialized .forge/ in %s\n\n", cwd)
			}

			if err := ensureProjectScaffold(cwd, cmd.OutOrStdout(), false); err != nil {
				return err
			}

			cfg, err := config.Load(cwd)
			if err != nil {
				return err
			}
			gitState, err := gitops.InspectSessionState(
				cwd,
				cfg.Git.AutoInit,
				cfg.Git.RequireCleanOrSnapshot,
				cfg.Git.BaselineCommitMessage,
			)
			if err != nil {
				return err
			}
			if gitState.AutoInitialized {
				fmt.Fprintf(cmd.OutOrStdout(), "Initialized git repository in %s\n", cwd)
			}

			// Redirect stderr to .forge/forge.log BEFORE any work that may
			// write to it. Bubble Tea owns stdout; anything we print to
			// stderr after this point lands in a tailable file instead of
			// being scribbled over the TUI frame. Done at most once per
			// process — no close hook needed, the OS reclaims the fd at
			// exit.
			_ = redirectStderrToLog(cwd)

			registry := tools.NewRegistry()
			tools.RegisterBuiltins(registry)
			if err := tools.RegisterExternal(registry, cwd); err != nil {
				return err
			}

			mcpManager := mcp.NewManager(cwd, registry)
			if err := mcpManager.Start(context.Background()); err != nil {
				fmt.Fprintf(os.Stderr, "mcp: %s\n", err)
			}
			tools.RegisterMCPResourceTools(registry, mcpResourceAdapter{m: mcpManager})

			providers := llm.NewRegistry()
			providers.Register(llm.NewOpenAICompatible("openai_compatible", cfg.Providers.OpenAICompatible))
			providers.Register(llm.NewOpenAICompatible("lmstudio", cfg.Providers.LMStudio))
			probeActiveContext(cwd, &cfg, providers)
			sessionStore, err := openSession(cwd, resume)
			if err != nil {
				return err
			}

			hookRunner := hooks.NewRunner(cwd)

			// Load MCP, hooks, and skill dirs from discovered plugins.
			pluginMgr := plugins.NewManager(cwd)
			enabledState := plugins.LoadEnabledState(cwd)
			var pluginSkillDirs []string
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
					if skillsDir := p.SkillsDir(); skillsDir != "" {
						pluginSkillDirs = append(pluginSkillDirs, skillsDir)
					}
				}
			}
			if len(pluginSkillDirs) > 0 {
				tools.RegisterRunSkillTool(registry, pluginSkillDirs)
			}

			var projectSvc *projectstate.Service
			if sqlDB, err := db.Open(cwd); err == nil {
				projectSvc = projectstate.NewService(sqlDB)
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := projectSvc.EnsureSnapshot(ctx, cwd); err != nil {
						fmt.Fprintf(os.Stderr, "projectstate: %s\n", err)
					}
				}()
			} else {
				fmt.Fprintf(os.Stderr, "projectstate db: %s\n", err)
			}

			app := tui.New(tui.Options{
				CWD:          cwd,
				Config:       cfg,
				Tools:        registry,
				Providers:    providers,
				Session:      sessionStore,
				ProjectState: projectSvc,
				Skills: skills.NewManager(cwd, skills.Options{
					CLI:             cfg.Skills.CLI,
					DirectoryURL:    cfg.Skills.DirectoryURL,
					Repositories:    cfg.Skills.Repositories,
					Agent:           cfg.Skills.Agent,
					InstallScope:    cfg.Skills.InstallScope,
					Copy:            cfg.Skills.Copy,
					Installer:       cfg.Skills.Installer,
					PluginSkillDirs: pluginSkillDirs,
				}),
				Plugins:  pluginMgr,
				MCP:      mcpManager,
				Hooks:    hookRunner,
				GitState: gitState,
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

// probeActiveContext hits the default provider's /models endpoint to discover
// the actual loaded context window (e.g. YaRN-extended 262k on LM Studio) and
// stashes it into cfg.Context.Detected. Best-effort: failures are silent, we
// just fall back to the static profile caps.
func probeActiveContext(cwd string, cfg *config.Config, providers *llm.Registry) {
	name := cfg.Providers.Default.Name
	if name == "" {
		return
	}
	provider, ok := providers.Get(name)
	if !ok {
		return
	}
	modelID := cfg.Models["chat"]
	if modelID == "" {
		modelID = cfg.Providers.LMStudio.DefaultModel
	}
	// Reuse a cached detection if it already matches the active model.
	if cfg.Context.Detected != nil && cfg.Context.Detected.ModelID == modelID && cfg.Context.Detected.LoadedContextLength > 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	info, err := provider.ProbeModel(ctx, modelID)
	if err != nil || info == nil || info.LoadedContextLength <= 0 {
		return
	}
	cfg.Context.Detected = &config.DetectedContext{
		ModelID:             info.ID,
		LoadedContextLength: info.LoadedContextLength,
		MaxContextLength:    info.MaxContextLength,
		ProbedAt:            time.Now().UTC(),
	}
	// Persist so restarts skip the probe and /yarn inspect can show it.
	if data, err := toml.Marshal(cfg); err == nil {
		_ = os.WriteFile(filepath.Join(cwd, ".forge", "config.toml"), data, 0o644)
	}
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
