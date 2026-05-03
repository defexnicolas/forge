package tui

import (
	"os"
	"path/filepath"

	"forge/internal/claw"
	"forge/internal/globalconfig"
	"forge/internal/plugins"
	"forge/internal/session"
	"forge/internal/skills"
	"forge/internal/tools"
)

func hubChatRootDir() string {
	return filepath.Join(filepath.Dir(globalconfig.Path()), "hub")
}

func openHubChatSession() (*WorkspaceSession, error) {
	root := hubChatRootDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if err := skills.EnsureGlobalDirs(); err != nil {
		return nil, err
	}
	cfg, err := loadHubGlobalConfig()
	if err != nil {
		return nil, err
	}
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)
	if err := tools.RegisterExternal(registry, root); err != nil {
		return nil, err
	}
	providers := hubSettingsProviders(cfg)
	clawSvc, err := claw.Open(cfg, providers, registry)
	if err != nil {
		return nil, err
	}
	sessionStore, err := session.New(root)
	if err != nil {
		return nil, err
	}
	skillMgr := skills.NewGlobalManager(skills.Options{
		CLI:          cfg.Skills.CLI,
		DirectoryURL: cfg.Skills.DirectoryURL,
		Repositories: cfg.Skills.Repositories,
		Agent:        cfg.Skills.Agent,
		InstallScope: cfg.Skills.InstallScope,
		Copy:         cfg.Skills.Copy,
		Installer:    cfg.Skills.Installer,
	})
	return &WorkspaceSession{
		Options: Options{
			CWD:       root,
			Config:    cfg,
			Tools:     registry,
			Providers: providers,
			Claw:      clawSvc,
			Session:   sessionStore,
			Skills:    skillMgr,
			Plugins:   plugins.NewManager(root),
		},
	}, nil
}
