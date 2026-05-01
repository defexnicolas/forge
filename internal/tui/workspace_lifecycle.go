package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (m *model) close() error {
	if m == nil {
		return nil
	}
	var errs []string
	if m.remoteServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := m.remoteServer.server.Stop(ctx); err != nil {
			errs = append(errs, err.Error())
		}
		cancel()
		m.remoteServer.cancel()
		m.remoteServer = nil
	}
	if m.agentRuntime != nil {
		if err := m.agentRuntime.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
