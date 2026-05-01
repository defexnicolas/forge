package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/session"
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
			launchDir, err := os.Getwd()
			if err != nil {
				return err
			}

			var initialWorkspace *tui.WorkspaceSession
			if cwd != "" {
				initialWorkspace, err = openWorkspaceSession(context.Background(), cwd, workspaceBootstrapOptions{
					Resume:          resume,
					Output:          cmd.OutOrStdout(),
					PromptIfMissing: true,
				})
				if err != nil {
					if errors.Is(err, errWorkspaceInitAborted) {
						return nil
					}
					return err
				}
			}

			app := tui.NewShell(tui.ShellOptions{
				InitialWorkspace: initialWorkspace,
				InitialHubDir:    launchDir,
				StateStore:       tui.NewFileHubStateStore(""),
				OpenWorkspace: func(workspaceCWD, workspaceResume string) (*tui.WorkspaceSession, error) {
					return openWorkspaceSession(context.Background(), workspaceCWD, workspaceBootstrapOptions{
						Resume:          workspaceResume,
						Output:          nil,
						PromptIfMissing: false,
					})
				},
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
