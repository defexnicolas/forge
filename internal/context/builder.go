package contextbuilder

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"forge/internal/config"
	"forge/internal/lsp"
	"forge/internal/skills"
	"forge/internal/tools"
	"forge/internal/yarn"
)

var mentionPattern = regexp.MustCompile(`@([A-Za-z0-9_./\\:-]+)`)

type Builder struct {
	CWD     string
	Config  config.Config
	Tools   *tools.Registry
	Yarn    *yarn.Store
	Tray    *Tray
	History HistorySource
	Skills  *skills.Manager
	LSP     lsp.Client
}

type HistorySource interface {
	ContextText(limit int) string
}

type Item struct {
	Kind    string
	Path    string
	Content string
	Error   string
	Source  string
	Mode    string
}

type Snapshot struct {
	CWD               string
	Provider          string
	Model             string
	ContextEngine     string
	ReadOnlyToolNames []string
	Items             []Item
	TokensUsed        int
	TokensBudget      int
}

type BuildOptions struct {
	RecordYarn bool
}

func NewBuilder(cwd string, cfg config.Config, registry *tools.Registry) *Builder {
	return &Builder{CWD: cwd, Config: cfg, Tools: registry, Yarn: yarn.New(cwd), Tray: NewTray(cwd)}
}

func (b *Builder) Build(userMessage string) Snapshot {
	return b.BuildWithOptions(userMessage, BuildOptions{RecordYarn: true})
}

func (b *Builder) BuildWithOptions(userMessage string, opts BuildOptions) Snapshot {
	model := b.Config.Models["chat"]
	snapshot := Snapshot{
		CWD:               b.CWD,
		Provider:          b.Config.Providers.Default.Name,
		Model:             model,
		ContextEngine:     b.Config.Context.Engine,
		ReadOnlyToolNames: ReadOnlyToolNames(),
	}

	// Load installed skills into context.
	if b.Skills != nil {
		for _, detail := range b.Skills.LoadAllInstalled() {
			if detail.Meta.Content != "" {
				snapshot.Items = append(snapshot.Items, Item{
					Kind:    "skill",
					Path:    detail.Meta.Name,
					Content: detail.Meta.Content,
					Source:  "skill",
					Mode:    "always",
				})
			}
		}
	}

	snapshot.TokensBudget = b.Config.Context.BudgetTokens
	if strings.EqualFold(b.Config.Context.Engine, "yarn") {
		return b.enforceTokenBudget(b.buildYarn(userMessage, snapshot, opts))
	}
	return b.enforceTokenBudget(b.buildSimple(userMessage, snapshot))
}

func (b *Builder) buildSimple(userMessage string, snapshot Snapshot) Snapshot {
	snapshot.Items = append(snapshot.Items, b.directItems(userMessage)...)
	return snapshot
}

func (b *Builder) buildYarn(userMessage string, snapshot Snapshot, opts BuildOptions) Snapshot {
	always, scored := b.yarnItems(userMessage)
	snapshot.Items = append(snapshot.Items, always...)

	if b.Yarn == nil {
		snapshot.Items = append(snapshot.Items, scored...)
		return snapshot
	}

	for _, item := range append(append([]Item{}, always...), scored...) {
		if item.Error != "" || item.Content == "" {
			if item.Mode != "always" {
				snapshot.Items = append(snapshot.Items, item)
			}
			continue
		}
		if opts.RecordYarn {
			_ = b.Yarn.Upsert(yarn.Node{
				Kind:    item.Kind,
				Path:    item.Path,
				Summary: item.Kind + " " + item.Path,
				Content: item.Content,
			})
		}
	}
	if b.History != nil && b.Config.Context.Yarn.HistoryEvents > 0 {
		history := strings.TrimSpace(b.History.ContextText(b.Config.Context.Yarn.HistoryEvents))
		if history != "" && history != "No session events yet." {
			if opts.RecordYarn {
				_ = b.Yarn.Upsert(yarn.Node{
					Kind:    "session",
					Summary: "recent session timeline",
					Content: history,
				})
			}
		}
	}

	budgetBytes := b.Config.Context.BudgetTokens * 4
	maxNodes := b.Config.Context.Yarn.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 8
	}
	nodes, err := b.selectYarnNodes(userMessage, budgetBytes, maxNodes+len(always), append(append([]Item{}, always...), scored...), opts.RecordYarn)
	if err != nil {
		snapshot.Items = append(snapshot.Items, Item{Kind: "yarn", Path: filepath.ToSlash(b.Yarn.Path()), Error: err.Error()})
		snapshot.Items = append(snapshot.Items, scored...)
		return snapshot
	}
	seen := itemKeys(snapshot.Items)
	added := 0
	for _, node := range nodes {
		key := itemKey("yarn:"+node.Kind, node.Path)
		sourceKey := itemKey(node.Kind, node.Path)
		if seen[key] || seen[sourceKey] {
			continue
		}
		snapshot.Items = append(snapshot.Items, Item{
			Kind:    "yarn:" + node.Kind,
			Path:    node.Path,
			Content: renderYarnNode(node),
			Source:  "yarn",
			Mode:    "scored",
		})
		seen[key] = true
		added++
		if added >= maxNodes {
			break
		}
	}
	return snapshot
}

func (b *Builder) selectYarnNodes(userMessage string, budgetBytes, limit int, transient []Item, record bool) ([]yarn.Node, error) {
	if record {
		return b.Yarn.Select(userMessage, budgetBytes, limit)
	}
	nodes, err := b.Yarn.Load()
	if err != nil {
		return nil, err
	}
	for _, item := range transient {
		if item.Error != "" || item.Content == "" {
			continue
		}
		nodes = append(nodes, yarn.Node{
			Kind:      item.Kind,
			Path:      item.Path,
			Summary:   item.Kind + " " + item.Path,
			Content:   item.Content,
			UpdatedAt: time.Now().UTC(),
		})
	}
	return yarn.SelectNodes(nodes, userMessage, budgetBytes, limit), nil
}

func (b *Builder) yarnItems(userMessage string) ([]Item, []Item) {
	all := b.directItems(userMessage)
	var always []Item
	var scored []Item
	for _, item := range all {
		source := item.Source
		switch source {
		case "pin":
			switch strings.ToLower(b.Config.Context.Yarn.Pins) {
			case "off":
				if item.Error != "" {
					item.Mode = "always"
					always = append(always, item)
				}
				continue
			case "scored":
				item.Mode = "scored"
				scored = append(scored, item)
			default:
				item.Mode = "always"
				always = append(always, item)
			}
		case "mention":
			if item.Error != "" || strings.EqualFold(b.Config.Context.Yarn.Mentions, "always") {
				item.Mode = "always"
				always = append(always, item)
			} else {
				item.Mode = "scored"
				scored = append(scored, item)
			}
		default:
			item.Mode = "scored"
			scored = append(scored, item)
		}
	}
	return always, scored
}

func (b *Builder) directItems(userMessage string) []Item {
	var direct []Item
	seen := map[string]bool{}
	// Load root AGENTS.md
	if item := b.readOptional("AGENTS.md", "instructions"); item.Path != "" || item.Error == "" && item.Content != "" {
		item.Source = "instructions"
		item.Mode = "direct"
		direct = append(direct, item)
		if item.Path != "" {
			seen[item.Path] = true
		}
	}
	// Load nested AGENTS.md from subdirectories mentioned in the message
	for _, mention := range ParseMentions(userMessage) {
		dir := filepath.Dir(mention)
		if dir == "." || dir == "" {
			continue
		}
		agentsPath := filepath.Join(dir, "AGENTS.md")
		if seen[filepath.ToSlash(agentsPath)] {
			continue
		}
		if item := b.readOptional(agentsPath, "instructions"); item.Content != "" {
			item.Source = "instructions"
			item.Mode = "direct"
			direct = append(direct, item)
			seen[filepath.ToSlash(agentsPath)] = true
		}
	}

	if b.Tray != nil {
		pins, err := b.Tray.Pins()
		if err == nil {
			for _, pin := range pins {
				if seen[pin.Path] {
					continue
				}
				item := b.readOptional(pin.Path, "pinned")
				item.Source = "pin"
				item.Mode = "direct"
				direct = append(direct, item)
				seen[pin.Path] = true
			}
		}
	}

	for _, mention := range ParseMentions(userMessage) {
		if mention == "" || seen[filepath.ToSlash(mention)] {
			continue
		}
		item := b.ResolveMention(mention)
		item.Source = "mention"
		item.Mode = "direct"
		direct = append(direct, item)
		seen[filepath.ToSlash(mention)] = true
	}
	return direct
}

func estimateTokens(text string) int {
	return len(text) / 4
}

func (b *Builder) enforceTokenBudget(snapshot Snapshot) Snapshot {
	budget := snapshot.TokensBudget
	if budget <= 0 {
		budget = 12000
	}
	used := 0
	for i := range snapshot.Items {
		tokens := estimateTokens(snapshot.Items[i].Content)
		if used+tokens > budget && used > 0 {
			remaining := budget - used
			if remaining > 200 {
				// Truncate this item to fit remaining budget.
				charLimit := remaining * 4
				if charLimit < len(snapshot.Items[i].Content) {
					snapshot.Items[i].Content = snapshot.Items[i].Content[:charLimit] + "\n[truncated: token budget exceeded]"
					tokens = remaining
				}
			} else {
				snapshot.Items[i].Content = "[omitted: token budget exceeded]"
				tokens = 10
			}
		}
		used += tokens
	}
	snapshot.TokensUsed = used
	return snapshot
}

func renderYarnNode(node yarn.Node) string {
	var b strings.Builder
	if node.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", node.Summary)
	}
	if len(node.Links) > 0 {
		fmt.Fprintf(&b, "Links: %s\n", strings.Join(node.Links, ", "))
	}
	b.WriteString(node.Content)
	if !strings.HasSuffix(node.Content, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func (s Snapshot) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CWD: %s\n", s.CWD)
	fmt.Fprintf(&b, "Provider: %s\n", s.Provider)
	if s.Model != "" {
		fmt.Fprintf(&b, "Model: %s\n", s.Model)
	}
	fmt.Fprintf(&b, "Context engine: %s\n", s.ContextEngine)
	if s.TokensBudget > 0 {
		fmt.Fprintf(&b, "Tokens: %d/%d\n", s.TokensUsed, s.TokensBudget)
	}
	fmt.Fprintf(&b, "Read-only tools: %s\n", strings.Join(s.ReadOnlyToolNames, ", "))
	for _, item := range s.Items {
		fmt.Fprintf(&b, "\n[%s] %s\n", item.Kind, item.Path)
		if item.Error != "" {
			fmt.Fprintf(&b, "Error: %s\n", item.Error)
			continue
		}
		b.WriteString(item.Content)
		if !strings.HasSuffix(item.Content, "\n") {
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func ParseMentions(message string) []string {
	matches := mentionPattern.FindAllStringSubmatch(message, -1)
	seen := map[string]bool{}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		path := strings.TrimRight(match[1], ".,;:!?)]}")
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func (b *Builder) readOptional(path, kind string) Item {
	item := Item{Kind: kind, Path: filepath.ToSlash(path)}
	resolved, err := workspacePath(b.CWD, path)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) && path == "AGENTS.md" {
			return Item{}
		}
		item.Error = err.Error()
		return item
	}
	maxBytes := b.Config.Context.Yarn.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = 12000
	}
	if len(data) > maxBytes {
		data = data[:maxBytes]
		item.Content = string(data) + "\n[truncated]\n"
		return item
	}
	item.Content = string(data)
	return item
}

func itemKeys(items []Item) map[string]bool {
	out := map[string]bool{}
	for _, item := range items {
		out[itemKey(item.Kind, item.Path)] = true
	}
	return out
}

func itemKey(kind, path string) string {
	return kind + "\x00" + path
}

func workspacePath(cwd, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, rel)
	}
	cleanCWD, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if cleanPath != cleanCWD && !strings.HasPrefix(cleanPath, cleanCWD+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	return cleanPath, nil
}

// LastTokenInfo returns a short string showing last known token usage.
func (b *Builder) LastTokenInfo() string {
	budget := b.Config.Context.BudgetTokens
	if budget <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d max", budget)
}

func ReadOnlyToolNames() []string {
	return []string{
		"read_file",
		"list_files",
		"search_text",
		"search_files",
		"git_status",
		"git_diff",
		"todo_write",
	}
}
