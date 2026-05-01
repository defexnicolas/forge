package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge/internal/config"
	"forge/internal/db"
	"forge/internal/gitops"
	"forge/internal/hooks"
	"forge/internal/llm"
	"forge/internal/lsp"
	"forge/internal/mcp"
	"forge/internal/plugins"
	"forge/internal/projectstate"
	"forge/internal/skills"
	"forge/internal/tools"
	"forge/internal/tui"
)

var errWorkspaceInitAborted = errors.New("workspace initialization aborted")

type workspaceBootstrapOptions struct {
	Resume          string
	Output          io.Writer
	PromptIfMissing bool
}

func openWorkspaceSession(ctx context.Context, cwd string, opts workspaceBootstrapOptions) (*tui.WorkspaceSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	cwd = absCWD

	if err := ensureWorkspaceScaffold(cwd, opts.Output, opts.PromptIfMissing); err != nil {
		return nil, err
	}

	cfg, err := config.LoadWithGlobal(cwd)
	if err != nil {
		// LoadWithGlobal still hands back the workspace-only config when the
		// global file is malformed, so fail open: log and continue.
		fmt.Fprintf(os.Stderr, "global config: %s\n", err)
	}
	config.InheritChatModelDefaults(&cfg)
	gitState, err := gitops.InspectSessionState(
		cwd,
		cfg.Git.AutoInit,
		cfg.Git.RequireCleanOrSnapshot,
		cfg.Git.BaselineCommitMessage,
	)
	if err != nil {
		return nil, err
	}
	if gitState.AutoInitialized && opts.Output != nil {
		fmt.Fprintf(opts.Output, "Initialized git repository in %s\n", cwd)
	}

	// Redirect stderr to the workspace log before loading subsystems that may
	// emit background diagnostics.
	_ = redirectStderrToLog(cwd)

	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	if err := tools.RegisterExternal(registry, cwd); err != nil {
		return nil, err
	}

	mcpManager := mcp.NewManager(cwd, registry)
	if err := mcpManager.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %s\n", err)
	}
	tools.RegisterMCPResourceTools(registry, mcpResourceAdapter{m: mcpManager})

	providers := llm.NewRegistry()
	providers.Register(llm.NewOpenAICompatible("openai_compatible", cfg.Providers.OpenAICompatible))
	providers.Register(llm.NewOpenAICompatible("lmstudio", cfg.Providers.LMStudio))
	probeActiveContext(cwd, &cfg, providers)

	sessionStore, err := openSession(cwd, opts.Resume)
	if err != nil {
		mcpManager.Shutdown()
		return nil, err
	}

	hookRunner := hooks.NewRunner(cwd)
	pluginMgr := plugins.NewManager(cwd)
	enabledState := plugins.LoadEnabledState(cwd)
	var pluginSkillDirs []string
	var pluginLSPConfigs []string
	var enabledPlugins []plugins.Plugin
	var outputStyles []plugins.OutputStyle
	if discoveredPlugins, err := pluginMgr.Discover(); err == nil {
		for _, p := range discoveredPlugins {
			if enabledState.Disabled[p.Name] {
				continue
			}
			enabledPlugins = append(enabledPlugins, p)
			if mcpPath := p.MCPConfigPath(); mcpPath != "" {
				if err := mcpManager.StartFromFile(ctx, mcpPath); err != nil {
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
			if lspPath := p.LSPConfigPath(); lspPath != "" {
				pluginLSPConfigs = append(pluginLSPConfigs, lspPath)
			}
			outputStyles = append(outputStyles, p.ListOutputStyles()...)
		}
	}
	if len(pluginSkillDirs) > 0 {
		tools.RegisterRunSkillTool(registry, pluginSkillDirs)
	}

	pluginSettings, settingsErrs := plugins.MergePluginSettings(enabledPlugins)
	for _, err := range settingsErrs {
		fmt.Fprintf(os.Stderr, "plugin settings: %s\n", err)
	}
	for k, v := range pluginSettings.Env {
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}

	lspConfig, err := lsp.LoadConfig(cwd, pluginLSPConfigs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lsp config: %s\n", err)
	}
	var lspClient lsp.Client
	if len(lspConfig.ByExt) > 0 {
		lspClient = lsp.NewRouter(cwd, lspConfig)
	}

	var (
		projectSvc *projectstate.Service
		projectDB  *sql.DB
	)
	if sqlDB, err := db.Open(cwd); err == nil {
		projectDB = sqlDB
		projectSvc = projectstate.NewService(sqlDB)
		go func() {
			scanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := projectSvc.EnsureSnapshot(scanCtx, cwd); err != nil {
				fmt.Fprintf(os.Stderr, "projectstate: %s\n", err)
			}
		}()
	} else {
		fmt.Fprintf(os.Stderr, "projectstate db: %s\n", err)
	}

	workspace := &tui.WorkspaceSession{
		Options: tui.Options{
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
			Plugins:        pluginMgr,
			MCP:            mcpManager,
			Hooks:          hookRunner,
			GitState:       gitState,
			LSP:            lspClient,
			PluginSettings: pluginSettings,
			OutputStyles:   outputStyles,
		},
	}
	workspace.CloseFunc = func() error {
		var errs []string
		if mcpManager != nil {
			mcpManager.Shutdown()
		}
		if projectDB != nil {
			if err := projectDB.Close(); err != nil {
				errs = append(errs, err.Error())
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return nil
	}
	return workspace, nil
}

func ensureWorkspaceScaffold(cwd string, out io.Writer, promptIfMissing bool) error {
	forgeDir := filepath.Join(cwd, ".forge")
	if _, statErr := os.Stat(forgeDir); os.IsNotExist(statErr) {
		if promptIfMissing {
			if out != nil {
				fmt.Fprintf(out, "Forge wants to operate in: %s\n", cwd)
				fmt.Fprintf(out, "This will create a .forge/ directory for config, sessions, and context.\n")
				fmt.Fprintf(out, "Allow? [Y/n] ")
			}
			var answer string
			fmt.Scanln(&answer)
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer == "n" || answer == "no" {
				if out != nil {
					fmt.Fprintln(out, "Aborted.")
				}
				return errWorkspaceInitAborted
			}
		}
		if err := ensureProjectScaffold(cwd, out, false); err != nil {
			return err
		}
		if promptIfMissing && out != nil {
			fmt.Fprintf(out, "Initialized .forge/ in %s\n\n", cwd)
		}
	}
	return ensureProjectScaffold(cwd, out, false)
}
