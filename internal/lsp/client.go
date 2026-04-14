package lsp

import "fmt"

// Diagnostic represents a code diagnostic from an LSP server.
type Diagnostic struct {
	File     string
	Line     int
	Severity string
	Message  string
}

// Location represents a code location.
type Location struct {
	File  string
	Line  int
	Range string
}

// Client defines the interface for language server interaction.
type Client interface {
	Diagnostics(file string) ([]Diagnostic, error)
	Definition(file string, line, col int) ([]Location, error)
	References(file string, line, col int) ([]Location, error)
	Symbols(file string) ([]string, error)
}

// Stub returns a client that reports LSP as not configured.
func Stub() Client {
	return &stubClient{}
}

type stubClient struct{}

func (s *stubClient) Diagnostics(file string) ([]Diagnostic, error) {
	return nil, fmt.Errorf("LSP not configured. Add language server config to .forge/config.toml")
}

func (s *stubClient) Definition(file string, line, col int) ([]Location, error) {
	return nil, fmt.Errorf("LSP not configured")
}

func (s *stubClient) References(file string, line, col int) ([]Location, error) {
	return nil, fmt.Errorf("LSP not configured")
}

func (s *stubClient) Symbols(file string) ([]string, error) {
	return nil, fmt.Errorf("LSP not configured")
}
