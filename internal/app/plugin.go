package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"forge/internal/plugins"

	"github.com/spf13/cobra"
)

func newPluginCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage plugins",
	}
	cmd.AddCommand(
		newPluginListCommand(),
		newPluginInstallCommand(),
		newPluginEnableCommand(),
		newPluginDisableCommand(),
		newPluginRemoveCommand(),
		newPluginInspectCommand(),
	)
	return cmd
}

func newPluginListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List discovered plugins",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			mgr := plugins.NewManager(cwd)
			found, err := mgr.Discover()
			if err != nil {
				return err
			}
			state := plugins.LoadEnabledState(cwd)
			for _, p := range found {
				status := "enabled"
				if state.Disabled[p.Name] {
					status = "disabled"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-14s %-12s %s\n", p.Name, p.Source, status, p.Path)
			}
			if len(found) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No plugins found.")
			}
			return nil
		},
	}
}

func newPluginInstallCommand() *cobra.Command {
	var global bool
	cmd := &cobra.Command{
		Use:   "install <path-or-git-url>",
		Short: "Install a plugin from a local path or git URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			source := args[0]
			pluginsDir := filepath.Join(cwd, ".forge", "plugins")
			if global {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				pluginsDir = filepath.Join(home, ".forge", "plugins")
			}
			_ = os.MkdirAll(pluginsDir, 0o755)

			if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") || strings.HasSuffix(source, ".git") {
				// Git clone.
				name := filepath.Base(strings.TrimSuffix(source, ".git"))
				dest := filepath.Join(pluginsDir, name)
				if _, err := os.Stat(dest); err == nil {
					return fmt.Errorf("plugin already exists: %s", dest)
				}
				gitCmd := exec.Command("git", "clone", "--depth=1", source, dest)
				gitCmd.Stdout = cmd.OutOrStdout()
				gitCmd.Stderr = cmd.ErrOrStderr()
				if err := gitCmd.Run(); err != nil {
					return fmt.Errorf("git clone failed: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Installed plugin: %s -> %s\n", name, dest)
			} else {
				// Local path — symlink or copy.
				absSource, err := filepath.Abs(source)
				if err != nil {
					return err
				}
				info, err := os.Stat(absSource)
				if err != nil {
					return fmt.Errorf("source not found: %s", source)
				}
				if !info.IsDir() {
					return fmt.Errorf("source must be a directory: %s", source)
				}
				name := filepath.Base(absSource)
				dest := filepath.Join(pluginsDir, name)
				if _, err := os.Stat(dest); err == nil {
					return fmt.Errorf("plugin already exists: %s", dest)
				}
				// Create symlink.
				if err := os.Symlink(absSource, dest); err != nil {
					return fmt.Errorf("symlink failed: %w (try copying manually)", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Installed plugin (symlink): %s -> %s\n", name, dest)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "Install to ~/.forge/plugins/ instead of project .forge/plugins/")
	return cmd
}

func newPluginEnableCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a disabled plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			state := plugins.LoadEnabledState(cwd)
			name := args[0]
			if !state.Disabled[name] {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is already enabled.\n", name)
				return nil
			}
			delete(state.Disabled, name)
			if err := plugins.SaveEnabledState(cwd, state); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Enabled: %s (restart forge to apply)\n", name)
			return nil
		},
	}
}

func newPluginDisableCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable a plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			state := plugins.LoadEnabledState(cwd)
			name := args[0]
			state.Disabled[name] = true
			if err := plugins.SaveEnabledState(cwd, state); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Disabled: %s (restart forge to apply)\n", name)
			return nil
		},
	}
}

func newPluginRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			name := args[0]
			// Only remove from .forge/plugins/.
			dest := filepath.Join(cwd, ".forge", "plugins", name)
			info, err := os.Lstat(dest)
			if err != nil {
				return fmt.Errorf("plugin not found: %s", name)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				// Remove symlink.
				if err := os.Remove(dest); err != nil {
					return err
				}
			} else {
				if err := os.RemoveAll(dest); err != nil {
					return err
				}
			}
			// Also remove from disabled state.
			state := plugins.LoadEnabledState(cwd)
			delete(state.Disabled, name)
			_ = plugins.SaveEnabledState(cwd, state)
			fmt.Fprintf(cmd.OutOrStdout(), "Removed: %s\n", name)
			return nil
		},
	}
}

func newPluginInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show details of a plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			mgr := plugins.NewManager(cwd)
			found, err := mgr.Discover()
			if err != nil {
				return err
			}
			name := args[0]
			for _, p := range found {
				if p.Name != name {
					continue
				}
				state := plugins.LoadEnabledState(cwd)
				status := "enabled"
				if state.Disabled[p.Name] {
					status = "disabled"
				}

				info := map[string]string{
					"name":    p.Name,
					"path":    p.Path,
					"source":  p.Source,
					"status":  status,
					"version": p.Version,
				}
				if p.Description != "" {
					info["description"] = p.Description
				}
				// Check for components.
				components := []string{}
				for _, c := range []string{"skills", "commands", "agents", "hooks", ".mcp.json", "bin", "output-styles"} {
					if _, err := os.Stat(filepath.Join(p.Path, c)); err == nil {
						components = append(components, c)
					}
				}
				info["components"] = strings.Join(components, ", ")

				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			return fmt.Errorf("plugin not found: %s", name)
		},
	}
}
