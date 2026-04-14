package app

import (
	"encoding/json"
	"os"

	"forge/internal/tools"

	"github.com/spf13/cobra"
)

func newToolsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "List registered tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := tools.NewRegistry()
			tools.RegisterBuiltins(registry)
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := tools.RegisterExternal(registry, cwd); err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(registry.Describe())
		},
	}
	cmd.AddCommand(newToolNewCommand())
	return cmd
}
