package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"forge/internal/gitops"
)

type Operation struct {
	Path      string
	OldText   string
	NewText   string
	NewFile   bool
	Generated string
}

type Plan struct {
	Operations []Operation
}

type Snapshot struct {
	Path    string
	Exists  bool
	Content []byte
	Patch   string
}

func ExactReplace(cwd, relPath, oldText, newText string) (Plan, error) {
	if relPath == "" {
		return Plan{}, fmt.Errorf("path is required")
	}
	if oldText == "" {
		return Plan{}, fmt.Errorf("old_text is required")
	}
	path, err := WorkspacePath(cwd, relPath)
	if err != nil {
		return Plan{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	content := string(data)
	count := strings.Count(content, oldText)
	if count == 0 {
		return Plan{}, fmt.Errorf("old_text was not found in %s", relPath)
	}
	if count > 1 {
		return Plan{}, fmt.Errorf("old_text matched %d times in %s", count, relPath)
	}
	return Plan{Operations: []Operation{{
		Path:    relPath,
		OldText: content,
		NewText: strings.Replace(content, oldText, newText, 1),
	}}}, nil
}

func NewFile(cwd, relPath, content string) (Plan, error) {
	if relPath == "" {
		return Plan{}, fmt.Errorf("path is required")
	}
	path, err := WorkspacePath(cwd, relPath)
	if err != nil {
		return Plan{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return Plan{}, fmt.Errorf("refusing to overwrite existing file: %s", relPath)
	} else if !os.IsNotExist(err) {
		return Plan{}, err
	}
	return Plan{Operations: []Operation{{
		Path:    relPath,
		NewText: content,
		NewFile: true,
	}}}, nil
}

func UnifiedDiff(cwd, diff string) (Plan, error) {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	lines := strings.Split(diff, "\n")
	var ops []Operation
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "--- ") {
			i++
			continue
		}
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
			return Plan{}, fmt.Errorf("malformed unified diff: missing +++ after ---")
		}
		oldPath := parseDiffPath(lines[i][4:])
		newPath := parseDiffPath(lines[i+1][4:])
		relPath := newPath
		if relPath == "/dev/null" {
			return Plan{}, fmt.Errorf("deleting files is not supported yet")
		}
		if relPath == "" || relPath == "/dev/null" {
			relPath = oldPath
		}
		if relPath == "" {
			return Plan{}, fmt.Errorf("malformed unified diff: empty path")
		}
		i += 2
		var hunks []hunk
		for i < len(lines) && !strings.HasPrefix(lines[i], "--- ") {
			if lines[i] == "" {
				i++
				continue
			}
			if !strings.HasPrefix(lines[i], "@@ ") {
				return Plan{}, fmt.Errorf("malformed unified diff: expected hunk header, got %q", lines[i])
			}
			parsed, next, err := parseHunk(lines, i)
			if err != nil {
				return Plan{}, err
			}
			hunks = append(hunks, parsed)
			i = next
		}
		op, err := buildOperation(cwd, relPath, oldPath == "/dev/null", hunks)
		if err != nil {
			return Plan{}, err
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return Plan{}, fmt.Errorf("no file patches found")
	}
	return Plan{Operations: ops}, nil
}

func Diff(plan Plan) string {
	var b strings.Builder
	for _, op := range plan.Operations {
		oldLines := splitLines(op.OldText)
		newLines := splitLines(op.NewText)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", op.Path, op.Path)
		if op.NewFile {
			fmt.Fprintf(&b, "new file mode 100644\n")
			fmt.Fprintf(&b, "--- /dev/null\n")
			fmt.Fprintf(&b, "+++ b/%s\n", op.Path)
		} else {
			fmt.Fprintf(&b, "--- a/%s\n", op.Path)
			fmt.Fprintf(&b, "+++ b/%s\n", op.Path)
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", hunkStart(oldLines), len(oldLines), hunkStart(newLines), len(newLines))
		lcs := longestCommonSubsequence(oldLines, newLines)
		oldIdx, newIdx := 0, 0
		for _, pair := range lcs {
			for oldIdx < pair.old {
				fmt.Fprintf(&b, "-%s\n", oldLines[oldIdx])
				oldIdx++
			}
			for newIdx < pair.new {
				fmt.Fprintf(&b, "+%s\n", newLines[newIdx])
				newIdx++
			}
			fmt.Fprintf(&b, " %s\n", oldLines[pair.old])
			oldIdx = pair.old + 1
			newIdx = pair.new + 1
		}
		for oldIdx < len(oldLines) {
			fmt.Fprintf(&b, "-%s\n", oldLines[oldIdx])
			oldIdx++
		}
		for newIdx < len(newLines) {
			fmt.Fprintf(&b, "+%s\n", newLines[newIdx])
			newIdx++
		}
	}
	return b.String()
}

func Apply(cwd string, plan Plan) ([]Snapshot, error) {
	if len(plan.Operations) == 0 {
		fmt.Fprintf(os.Stderr, "patch.Apply: plan has 0 operations - nothing to write\n")
		return nil, nil
	}
	diff := Diff(plan)
	for _, op := range plan.Operations {
		path, err := WorkspacePath(cwd, op.Path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	if err := gitops.ApplyPatch(cwd, diff); err != nil {
		return nil, err
	}
	return []Snapshot{{Patch: diff}}, nil
}

func Undo(cwd string, snapshots []Snapshot) error {
	if len(snapshots) == 1 && strings.TrimSpace(snapshots[0].Patch) != "" {
		return gitops.ReversePatch(cwd, snapshots[0].Patch)
	}
	for i := len(snapshots) - 1; i >= 0; i-- {
		snapshot := snapshots[i]
		path, err := WorkspacePath(cwd, snapshot.Path)
		if err != nil {
			return err
		}
		if !snapshot.Exists {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, snapshot.Content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func WorkspacePath(cwd, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	rel = strings.TrimPrefix(filepath.FromSlash(rel), string(os.PathSeparator))
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
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

type hunk struct {
	oldStart int
	lines    []string
}

func parseHunk(lines []string, start int) (hunk, int, error) {
	header := lines[start]
	oldStart, err := parseOldStart(header)
	if err != nil {
		return hunk{}, 0, err
	}
	out := hunk{oldStart: oldStart}
	i := start + 1
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "--- ") {
			break
		}
		if strings.HasPrefix(line, `\ No newline at end of file`) {
			i++
			continue
		}
		if line == "" && i == len(lines)-1 {
			break
		}
		if line == "" {
			out.lines = append(out.lines, " ")
			i++
			continue
		}
		prefix := line[0]
		if prefix != ' ' && prefix != '+' && prefix != '-' {
			break
		}
		out.lines = append(out.lines, line)
		i++
	}
	return out, i, nil
}

func parseOldStart(header string) (int, error) {
	parts := strings.Split(header, " ")
	if len(parts) < 2 || !strings.HasPrefix(parts[1], "-") {
		return 0, fmt.Errorf("malformed hunk header: %s", header)
	}
	oldRange := strings.TrimPrefix(parts[1], "-")
	oldStart := strings.Split(oldRange, ",")[0]
	n, err := strconv.Atoi(oldStart)
	if err != nil {
		return 0, fmt.Errorf("malformed hunk header: %s", header)
	}
	return n, nil
}

func buildOperation(cwd, relPath string, newFile bool, hunks []hunk) (Operation, error) {
	var oldText string
	if !newFile {
		path, err := WorkspacePath(cwd, relPath)
		if err != nil {
			return Operation{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Operation{}, err
		}
		oldText = string(data)
	}
	oldLines := splitLines(oldText)
	newLines := append([]string(nil), oldLines...)
	offset := 0
	for _, h := range hunks {
		idx := h.oldStart - 1 + offset
		if idx < 0 {
			idx = 0
		}
		end := idx
		var replacement []string
		for _, line := range h.lines {
			switch line[0] {
			case ' ':
				if end >= len(newLines) || newLines[end] != line[1:] {
					return Operation{}, fmt.Errorf("hunk context mismatch in %s", relPath)
				}
				replacement = append(replacement, line[1:])
				end++
			case '-':
				if end >= len(newLines) || newLines[end] != line[1:] {
					return Operation{}, fmt.Errorf("hunk removal mismatch in %s", relPath)
				}
				end++
			case '+':
				replacement = append(replacement, line[1:])
			}
		}
		newLines = append(append(newLines[:idx], replacement...), newLines[end:]...)
		offset += len(replacement) - (end - idx)
	}
	return Operation{Path: relPath, OldText: oldText, NewText: joinLines(newLines), NewFile: newFile}, nil
}

func parseDiffPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	if fields := strings.Fields(path); len(fields) > 0 {
		path = fields[0]
	}
	return filepath.ToSlash(path)
}

func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func hunkStart(lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	return 1
}

type lcsPair struct {
	old int
	new int
}

func longestCommonSubsequence(a, b []string) []lcsPair {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []lcsPair
	for i, j := 0, 0; i < len(a) && j < len(b); {
		if a[i] == b[j] {
			out = append(out, lcsPair{old: i, new: j})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
