package tui

import (
	"context"
	"fmt"
	"time"
)

func (m *model) handleAnalyzeCommand(fields []string) string {
	t := m.theme
	svc := m.options.ProjectState
	if svc == nil {
		return t.ErrorStyle.Render("Project state service unavailable (forge.db failed to open).")
	}
	sub := ""
	if len(fields) >= 2 {
		sub = fields[1]
	}
	switch sub {
	case "show":
		snap, ok := svc.Current()
		if !ok {
			return t.Muted.Render("No snapshot yet. Run /analyze refresh.")
		}
		return snap.Summary()
	case "refresh", "":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snap, err := svc.Rescan(ctx, m.options.CWD)
		if err != nil {
			return t.ErrorStyle.Render("analyze: " + err.Error())
		}
		return t.Success.Render(fmt.Sprintf("Project snapshot refreshed (%d langs, %d manifests).", len(snap.Languages), len(snap.Manifests))) +
			"\n\n" + snap.Summary()
	default:
		return "Usage: /analyze [refresh|show]"
	}
}
