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

	"forge/internal/agent"
	"forge/internal/claw"
	clawchannels "forge/internal/claw/channels"
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
	clawSvc, err := claw.Open(cfg, providers, registry)
	if err != nil {
		mcpManager.Shutdown()
		return nil, err
	}
	if cfg.Claw.Enabled && cfg.Claw.Autostart {
		if err := clawSvc.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "claw: %s\n", err)
		}
	}
	// Register the whatsapp_send tool. The closure routes through Claw's
	// channel registry so the tool sees whatever transport the user has
	// paired (a no-op error until pairing happens — surfaced clearly to
	// the model).
	if clawSvc != nil {
		tools.RegisterWhatsAppSendTool(registry, func(ctx context.Context, to, body string) error {
			_, err := clawSvc.SendVia(ctx, "whatsapp", clawchannels.Message{To: to, Body: body})
			return err
		})
		// Contact store is a Claw-local feature (no external side
		// effects). Wired here so the workspace's tool registry
		// advertises both save + lookup, matching how whatsapp_send
		// gets its channel closure injected.
		tools.RegisterClawContactTools(
			registry,
			func(ctx context.Context, name, phone, email, notes string) (tools.ContactRecord, error) {
				c, err := clawSvc.SaveContact(name, phone, email, notes, "claw_save_contact")
				if err != nil {
					return tools.ContactRecord{}, err
				}
				return tools.ContactRecord{
					Name:      c.Name,
					Phone:     c.Phone,
					Email:     c.Email,
					Notes:     c.Notes,
					Source:    c.Source,
					CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z"),
					UpdatedAt: c.UpdatedAt.Format("2006-01-02T15:04:05Z"),
				}, nil
			},
			func(ctx context.Context, name string) (tools.ContactRecord, bool) {
				c, ok := clawSvc.LookupContact(name)
				if !ok {
					return tools.ContactRecord{}, false
				}
				return tools.ContactRecord{
					Name:      c.Name,
					Phone:     c.Phone,
					Email:     c.Email,
					Notes:     c.Notes,
					Source:    c.Source,
					CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z"),
					UpdatedAt: c.UpdatedAt.Format("2006-01-02T15:04:05Z"),
				}, true
			},
		)
		tools.RegisterClawFactTools(
			registry,
			func(ctx context.Context, text, subject string) (tools.FactRecord, error) {
				f, err := clawSvc.RememberFact(text, subject, "claw_remember")
				if err != nil {
					return tools.FactRecord{}, err
				}
				return tools.FactRecord{
					ID: f.ID, Text: f.Text, Subject: f.Subject, Source: f.Source,
					CreatedAt: f.CreatedAt.Format("2006-01-02T15:04:05Z"),
				}, nil
			},
			func(ctx context.Context, query string, maxResults int) []tools.FactRecord {
				hits := clawSvc.RecallFacts(query, maxResults)
				out := make([]tools.FactRecord, 0, len(hits))
				for _, f := range hits {
					out = append(out, tools.FactRecord{
						ID: f.ID, Text: f.Text, Subject: f.Subject, Source: f.Source,
						CreatedAt: f.CreatedAt.Format("2006-01-02T15:04:05Z"),
					})
				}
				return out
			},
		)
		tools.RegisterClawReminderTools(
			registry,
			func(ctx context.Context, at time.Time, body, channel, target string) (tools.ReminderRecord, error) {
				r, err := clawSvc.ScheduleReminder(at, body, channel, target)
				if err != nil {
					return tools.ReminderRecord{}, err
				}
				return tools.ReminderRecord{
					ID: r.ID, RemindAt: r.RemindAt.Format(time.RFC3339),
					Body: r.Body, Channel: r.Channel, Target: r.Target, Status: r.Status,
				}, nil
			},
			func(ctx context.Context, status string) []tools.ReminderRecord {
				rs := clawSvc.ListReminders(status)
				out := make([]tools.ReminderRecord, 0, len(rs))
				for _, r := range rs {
					out = append(out, tools.ReminderRecord{
						ID: r.ID, RemindAt: r.RemindAt.Format(time.RFC3339),
						Body: r.Body, Channel: r.Channel, Target: r.Target, Status: r.Status,
						LastError: r.LastError,
					})
				}
				return out
			},
			func(ctx context.Context, id string) error { return clawSvc.CancelReminder(id) },
		)
		tools.RegisterClawWorkspaceNoteTool(registry, func(ctx context.Context, file, note string) (string, error) {
			return clawSvc.AppendWorkspaceNote(file, note)
		})
		// Cron scheduler: lets Claw program recurring tasks for itself.
		// All three closures route through the live Service so the
		// heartbeat picks them up without a restart.
		tools.RegisterClawCronTools(
			registry,
			func(ctx context.Context, name, schedule, prompt string) (tools.CronRecord, error) {
				job, err := clawSvc.AddCron(name, schedule, prompt)
				if err != nil {
					return tools.CronRecord{}, err
				}
				return cronRecordFromJob(job), nil
			},
			func(ctx context.Context) []tools.CronRecord {
				jobs := clawSvc.ListCrons()
				out := make([]tools.CronRecord, 0, len(jobs))
				for _, j := range jobs {
					out = append(out, cronRecordFromJob(j))
				}
				return out
			},
			func(ctx context.Context, id string) error { return clawSvc.RemoveCron(id) },
		)
		// Memory introspection + on-demand dreaming. Snapshot is a deep
		// read of state.Memory; dream returns the consolidator's summary
		// string so the LLM can use it in its reply.
		tools.RegisterClawIntrospectionTools(
			registry,
			func(ctx context.Context, limitEvents, limitFacts int) tools.MemorySnapshot {
				return memorySnapshotForClaw(clawSvc, limitEvents, limitFacts)
			},
			func(ctx context.Context, reason string) (string, error) {
				res, err := clawSvc.RunDream(ctx, reason)
				if err != nil {
					return "", err
				}
				return res.Summary, nil
			},
		)
	}

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
	var pluginAgents []agent.PluginAgent
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
			for _, def := range plugins.LoadAgents(p.Path) {
				pluginAgents = append(pluginAgents, agent.PluginAgent{
					Name:        def.Name,
					Description: def.Description,
					Source:      def.Source,
					Body:        def.Body,
					Tools:       def.Tools,
					ModelRole:   def.ModelRole,
				})
			}
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
			Claw:         clawSvc,
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
			PluginAgents:   pluginAgents,
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

// cronRecordFromJob projects a claw.CronJob into the wire-stable
// tools.CronRecord shape exposed to the LLM. Times are rendered RFC3339;
// zero times come through as empty strings (the json:"omitempty" tag on
// LastRunAt then drops them entirely).
func cronRecordFromJob(j claw.CronJob) tools.CronRecord {
	rec := tools.CronRecord{
		ID:         j.ID,
		Name:       j.Name,
		Schedule:   j.Schedule,
		Prompt:     j.Prompt,
		Enabled:    j.Enabled,
		LastResult: j.LastResult,
		LastError:  j.LastError,
	}
	if !j.NextRunAt.IsZero() {
		rec.NextRunAt = j.NextRunAt.Format(time.RFC3339)
	}
	if !j.LastRunAt.IsZero() {
		rec.LastRunAt = j.LastRunAt.Format(time.RFC3339)
	}
	return rec
}

// memorySnapshotForClaw flattens a slice of claw state into the
// LLM-facing tools.MemorySnapshot. limits: at most limitEvents events
// (most-recent first) and limitFacts facts. The pending-reminder count
// is filtered to status == "pending" only — sent/cancelled don't matter
// for "what's still open".
func memorySnapshotForClaw(svc *claw.Service, limitEvents, limitFacts int) tools.MemorySnapshot {
	if svc == nil {
		return tools.MemorySnapshot{}
	}
	state := svc.Status().State
	out := tools.MemorySnapshot{
		Crons: len(state.Crons),
	}
	for _, r := range state.Reminders {
		if strings.EqualFold(strings.TrimSpace(r.Status), "pending") {
			out.Reminders++
		}
	}
	// Summaries: take the last N (cap at 6 for prompt brevity).
	maxSummaries := 6
	sums := state.Memory.Summaries
	if len(sums) > maxSummaries {
		sums = sums[len(sums)-maxSummaries:]
	}
	for _, s := range sums {
		if t := strings.TrimSpace(s.Summary); t != "" {
			out.Summaries = append(out.Summaries, t)
		}
	}
	// Events: most recent N first.
	events := state.Memory.Events
	if limitEvents > 0 && len(events) > limitEvents {
		events = events[len(events)-limitEvents:]
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		out.Events = append(out.Events, tools.MemoryEventRec{
			Kind:      e.Kind,
			Channel:   e.Channel,
			Author:    e.Author,
			Text:      e.Text,
			CreatedAt: e.CreatedAt.Format(time.RFC3339),
		})
	}
	// Facts: take the last N stored (no relevance ranking — that's what
	// claw_recall is for; this tool is "what's in memory").
	facts := state.Memory.Facts
	if limitFacts > 0 && len(facts) > limitFacts {
		facts = facts[len(facts)-limitFacts:]
	}
	for _, f := range facts {
		out.Facts = append(out.Facts, tools.FactRecord{
			ID:        f.ID,
			Text:      f.Text,
			Subject:   f.Subject,
			Source:    f.Source,
			CreatedAt: f.CreatedAt.Format(time.RFC3339),
		})
	}
	return out
}
