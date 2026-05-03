package yarn

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Store struct {
	cwd  string
	path string
}

type Node struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Path      string    `json:"path,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Content   string    `json:"content"`
	Links     []string  `json:"links,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func New(cwd string) *Store {
	return &Store{
		cwd:  cwd,
		path: filepath.Join(cwd, ".forge", "yarn", "nodes.jsonl"),
	}
}

// NewAtPath creates a Store rooted at an absolute directory rather
// than under a workspace cwd's .forge/yarn subdirectory. Used for
// global stores like ~/.forge/yarn-claw/ where there is no enclosing
// workspace and the standard cwd/.forge/yarn convention doesn't apply.
func NewAtPath(dir string) *Store {
	return &Store{
		cwd:  dir,
		path: filepath.Join(dir, "nodes.jsonl"),
	}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Upsert(node Node) error {
	if strings.TrimSpace(node.Kind) == "" {
		return fmt.Errorf("yarn node kind is required")
	}
	node.ID = stableID(node)
	node.UpdatedAt = time.Now().UTC()

	nodes, err := s.Load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range nodes {
		if nodes[i].ID == node.ID {
			nodes[i] = node
			replaced = true
			break
		}
	}
	if !replaced {
		nodes = append(nodes, node)
	}
	return s.write(nodes)
}

func (s *Store) Load() ([]Node, error) {
	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var nodes []Node
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var node Node
		if err := json.Unmarshal([]byte(line), &node); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}

func (s *Store) Select(query string, budgetBytes, limit int) ([]Node, error) {
	nodes, err := s.Load()
	if err != nil {
		return nil, err
	}
	return SelectNodes(nodes, query, budgetBytes, limit), nil
}

func SelectNodes(nodes []Node, query string, budgetBytes, limit int) []Node {
	terms := terms(query)
	type scored struct {
		node  Node
		score int
	}
	scoredNodes := make([]scored, 0, len(nodes))
	for _, node := range nodes {
		score := scoreNode(node, terms)
		if score == 0 && node.Kind == "instructions" {
			score = 1
		}
		if score == 0 {
			continue
		}
		scoredNodes = append(scoredNodes, scored{node: node, score: score})
	}
	sort.SliceStable(scoredNodes, func(i, j int) bool {
		if scoredNodes[i].score == scoredNodes[j].score {
			return scoredNodes[i].node.UpdatedAt.After(scoredNodes[j].node.UpdatedAt)
		}
		return scoredNodes[i].score > scoredNodes[j].score
	})
	if budgetBytes <= 0 {
		budgetBytes = 48000
	}
	if limit <= 0 {
		limit = 12
	}
	selected := make([]Node, 0, min(limit, len(scoredNodes)))
	used := 0
	for _, scored := range scoredNodes {
		if len(selected) >= limit {
			break
		}
		size := len(scored.node.Content) + len(scored.node.Summary) + len(scored.node.Path)
		if size == 0 {
			continue
		}
		if used > 0 && used+size > budgetBytes {
			continue
		}
		used += size
		selected = append(selected, scored.node)
	}
	return selected
}

func (s *Store) write(nodes []Node) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, node := range nodes {
		data, err := json.Marshal(node)
		if err != nil {
			return err
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func stableID(node Node) string {
	key := node.Kind + ":" + node.Path
	if node.Path == "" {
		sum := sha1.Sum([]byte(node.Kind + ":" + node.Content))
		key = node.Kind + ":" + hex.EncodeToString(sum[:8])
	}
	return key
}

func scoreNode(node Node, queryTerms map[string]bool) int {
	if len(queryTerms) == 0 {
		if node.Kind == "instructions" || node.Kind == "session" {
			return 1
		}
		return 0
	}
	textTerms := terms(node.Kind + " " + node.Path + " " + node.Summary + " " + node.Content)
	score := 0
	for term := range queryTerms {
		for textTerm := range textTerms {
			if termMatches(term, textTerm) {
				score++
				break
			}
		}
	}
	if node.Kind == "instructions" && score > 0 {
		score++
	}
	return score
}

func termMatches(query, text string) bool {
	if query == text {
		return true
	}
	if len(query) >= 4 && strings.HasPrefix(text, query) {
		return true
	}
	if len(text) >= 4 && strings.HasPrefix(query, text) {
		return true
	}
	query = strings.TrimSuffix(query, "s")
	text = strings.TrimSuffix(text, "s")
	return query == text
}

func terms(text string) map[string]bool {
	out := map[string]bool{}
	var b strings.Builder
	flush := func() {
		if b.Len() < 2 {
			b.Reset()
			return
		}
		out[b.String()] = true
		b.Reset()
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '/' || r == '.' {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
