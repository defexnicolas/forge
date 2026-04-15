package agent

import (
	"testing"

	"forge/internal/config"
	"forge/internal/llm"
	"forge/internal/tools"
)

// newTestRuntime wraps NewRuntime and schedules Close() so the SQLite file
// held by tasks.Store is released before t.TempDir cleanup runs. Without this
// Windows refuses to unlink the still-locked forge.db and the test fails.
func newTestRuntime(t *testing.T, cwd string, cfg config.Config, registry *tools.Registry, providers *llm.Registry) *Runtime {
	t.Helper()
	r := NewRuntime(cwd, cfg, registry, providers)
	t.Cleanup(func() { _ = r.Close() })
	return r
}
