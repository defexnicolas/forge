package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var execCommandContext = exec.CommandContext
var httpClient = http.DefaultClient
var userHomeDir = os.UserHomeDir

type Options struct {
	CLI          string
	Repositories []string
	Agent        string
	InstallScope string
	Copy         bool
	DirectoryURL string
	Installer    string // legacy; no longer used for Skills CLI operations
}

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Installs    int    `json:"installs,omitempty"`
	Installed   bool   `json:"installed"`
	Repo        string `json:"repo,omitempty"`
	InstallPath string `json:"installPath,omitempty"`
	Source      string `json:"source,omitempty"`
}

type Manager struct {
	cwd     string
	options Options
}

type repositoryCache struct {
	Repo      string    `json:"repo"`
	UpdatedAt time.Time `json:"updated_at"`
	Skills    []Skill   `json:"skills"`
}

type directoryCache struct {
	UpdatedAt time.Time `json:"updated_at"`
	Skills    []Skill   `json:"skills"`
}

type directoryResponse struct {
	Skills  []directorySkill `json:"skills"`
	HasMore bool             `json:"hasMore"`
}

type directorySkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Installs    int    `json:"installs"`
	TopSource   string `json:"topSource"`
}

type CacheInfo struct {
	Repo      string
	Path      string
	Exists    bool
	UpdatedAt time.Time
	Count     int
	Error     string
}

func NewManager(cwd string, options Options) *Manager {
	options = normalizeOptions(options)
	return &Manager{cwd: cwd, options: options}
}

func normalizeOptions(options Options) Options {
	if options.CLI == "" {
		options.CLI = "npx"
	}
	if len(options.Repositories) == 0 {
		options.Repositories = []string{"vercel-labs/agent-skills", "vercel-labs/skills"}
	}
	if options.Agent == "" {
		options.Agent = "codex"
	}
	if options.InstallScope == "" {
		options.InstallScope = "project"
	}
	if options.DirectoryURL == "" {
		options.DirectoryURL = "https://skills.sh/"
	}
	return options
}

func (m *Manager) Options() Options {
	return m.options
}

func (m *Manager) Repositories() []string {
	return append([]string(nil), m.options.Repositories...)
}

func (m *Manager) CacheInfo(repos []string) []CacheInfo {
	repos = m.normalizeRepositories(repos)
	out := make([]CacheInfo, 0, len(repos))
	for _, repo := range repos {
		info := CacheInfo{Repo: repo, Path: m.repositoryCachePath(repo)}
		data, err := os.ReadFile(info.Path)
		if err != nil {
			if !os.IsNotExist(err) {
				info.Error = err.Error()
			}
			out = append(out, info)
			continue
		}
		var cache repositoryCache
		if err := json.Unmarshal(data, &cache); err != nil {
			info.Exists = true
			info.Error = err.Error()
			out = append(out, info)
			continue
		}
		info.Exists = true
		info.UpdatedAt = cache.UpdatedAt
		info.Count = len(cache.Skills)
		out = append(out, info)
	}
	return out
}

// LoadAllInstalled loads all locally installed skills and returns their parsed details.
func (m *Manager) LoadAllInstalled() []SkillDetail {
	local := m.ScanLocal()
	var details []SkillDetail
	for _, s := range local {
		detail, err := m.LoadSkill(s.Name)
		if err != nil {
			continue
		}
		details = append(details, *detail)
	}
	return details
}

func (m *Manager) List(ctx context.Context) ([]Skill, error) {
	if cached, ok := m.ListDirectoryCached(); ok {
		return cached, nil
	}
	return m.RefreshDirectoryCache(ctx)
}

func (m *Manager) ListCached(repos []string) ([]Skill, bool) {
	repos = m.normalizeRepositories(repos)
	var out []Skill
	installed := installedByName(m.ScanLocal())
	allCached := true
	for _, repo := range repos {
		listed, ok := m.readRepositoryCache(repo)
		if !ok {
			allCached = false
			continue
		}
		m.markInstalled(listed, installed)
		out = append(out, listed...)
	}
	return out, allCached
}

func (m *Manager) ListDirectoryCached() ([]Skill, bool) {
	listed, ok := m.readDirectoryCache()
	if !ok {
		return nil, false
	}
	m.markInstalled(listed, installedByName(m.ScanLocal()))
	return listed, true
}

func (m *Manager) RefreshDirectoryCache(ctx context.Context) ([]Skill, error) {
	listed, err := m.ListDirectory(ctx)
	if err != nil {
		return nil, err
	}
	_ = m.writeDirectoryCache(listed)
	m.markInstalled(listed, installedByName(m.ScanLocal()))
	return listed, nil
}

func (m *Manager) ListDirectory(ctx context.Context) ([]Skill, error) {
	if !strings.Contains(m.options.DirectoryURL, "/api/") {
		return m.ListDirectoryHTML(ctx)
	}
	var all []Skill
	seen := map[string]bool{}
	offset := 0
	for page := 0; page < 20; page++ {
		response, err := m.fetchDirectoryPage(ctx, offset)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, item := range response.Skills {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = strings.TrimSpace(item.ID)
			}
			repo := strings.TrimSpace(item.TopSource)
			key := repo + "@" + name
			if name == "" || repo == "" || seen[key] {
				continue
			}
			seen[key] = true
			added++
			all = append(all, Skill{
				Name:        name,
				Description: directoryDescription(item),
				Installs:    item.Installs,
				Repo:        repo,
				Source:      "skills.sh",
			})
		}
		if !response.HasMore || added == 0 {
			break
		}
		offset += len(response.Skills)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("skills.sh directory returned no skills")
	}
	return all, nil
}

func (m *Manager) ListDirectoryHTML(ctx context.Context) ([]Skill, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.options.DirectoryURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("skills.sh directory failed: %s", resp.Status)
	}
	found := ParseDirectoryHTML(string(body))
	if len(found) == 0 {
		return nil, fmt.Errorf("skills.sh directory returned no parseable skills")
	}
	return found, nil
}

func (m *Manager) fetchDirectoryPage(ctx context.Context, offset int) (directoryResponse, error) {
	endpoint := m.options.DirectoryURL
	if offset > 0 {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return directoryResponse{}, err
		}
		query := parsed.Query()
		query.Set("offset", strconv.Itoa(offset))
		parsed.RawQuery = query.Encode()
		endpoint = parsed.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return directoryResponse{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return directoryResponse{}, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if readErr != nil {
		return directoryResponse{}, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return directoryResponse{}, fmt.Errorf("skills.sh directory failed: %s\n%s", resp.Status, strings.TrimSpace(string(body)))
	}
	var parsed directoryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return directoryResponse{}, err
	}
	return parsed, nil
}

func (m *Manager) RefreshCache(ctx context.Context, repos []string) ([]Skill, error) {
	repos = m.normalizeRepositories(repos)
	var out []Skill
	installed := installedByName(m.ScanLocal())
	for _, repo := range repos {
		listed, err := m.ListRepository(ctx, repo)
		if err != nil {
			return nil, err
		}
		_ = m.writeRepositoryCache(repo, listed)
		m.markInstalled(listed, installed)
		out = append(out, listed...)
	}
	return out, nil
}

func (m *Manager) ListRepository(ctx context.Context, repo string) ([]Skill, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, fmt.Errorf("skills repository is required")
	}
	cmdName, args := m.ListCommand(repo)
	cmd := execCommandContext(ctx, cmdName, args...)
	cmd.Dir = m.cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %w\n%s", cmdName, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return ParseListOutput(repo, string(out)), nil
}

func (m *Manager) normalizeRepositories(repos []string) []string {
	if len(repos) == 0 {
		repos = m.options.Repositories
	}
	seen := map[string]bool{}
	var out []string
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" || seen[repo] {
			continue
		}
		seen[repo] = true
		out = append(out, repo)
	}
	return out
}

func (m *Manager) markInstalled(listed []Skill, installed map[string]Skill) {
	for i := range listed {
		if installedSkill, ok := installed[listed[i].Name]; ok {
			listed[i].Installed = true
			listed[i].InstallPath = installedSkill.InstallPath
			if listed[i].Source == "" {
				listed[i].Source = installedSkill.Source
			}
		}
	}
}

func (m *Manager) Install(ctx context.Context, skill Skill) error {
	if strings.TrimSpace(skill.Name) == "" {
		return fmt.Errorf("skill name is required")
	}
	if strings.TrimSpace(skill.Repo) == "" {
		return fmt.Errorf("skill repo is required")
	}
	cmdName, args := m.InstallCommandForSkill(skill)
	cmd := execCommandContext(ctx, cmdName, args...)
	cmd.Dir = m.cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %w\n%s", cmdName, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) InstallAndVerify(ctx context.Context, skill Skill) (Skill, error) {
	if err := m.Install(ctx, skill); err != nil {
		return Skill{}, err
	}
	installed, ok := m.FindInstalled(skill.Name)
	if !ok {
		return Skill{}, fmt.Errorf("install completed, but %s/SKILL.md was not found in .agents/skills, ~/.codex/skills, or legacy skill dirs", skill.Name)
	}
	if _, err := m.LoadSkill(skill.Name); err != nil {
		return Skill{}, fmt.Errorf("install completed, but SKILL.md could not be loaded: %w", err)
	}
	installed.Repo = skill.Repo
	return installed, nil
}

func (m *Manager) ListCommand(repo string) (string, []string) {
	return m.options.CLI, []string{"skills", "add", repo, "--list"}
}

func (m *Manager) InstallCommand(repo, name string) (string, []string) {
	args := []string{"skills", "add", repo, "--skill", name, "--agent", m.options.Agent}
	if strings.EqualFold(m.options.InstallScope, "global") {
		args = append(args, "--global")
	}
	if m.options.Copy {
		args = append(args, "--copy")
	}
	args = append(args, "-y")
	return m.options.CLI, args
}

func (m *Manager) InstallCommandForSkill(skill Skill) (string, []string) {
	repo := strings.TrimSpace(skill.Repo)
	if skill.Source == "skills.sh" && !strings.Contains(repo, "://") {
		repo = "https://github.com/" + repo
	}
	return m.InstallCommand(repo, skill.Name)
}

func (m *Manager) HasCLI(ctx context.Context) bool {
	cmd := execCommandContext(ctx, m.options.CLI, "--version")
	cmd.Dir = m.cwd
	return cmd.Run() == nil
}

func installedByName(items []Skill) map[string]Skill {
	out := map[string]Skill{}
	for _, item := range items {
		out[item.Name] = item
	}
	return out
}

func (m *Manager) readRepositoryCache(repo string) ([]Skill, bool) {
	path := m.repositoryCachePath(repo)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cache repositoryCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, false
	}
	if cache.Repo == "" {
		cache.Repo = repo
	}
	for i := range cache.Skills {
		cache.Skills[i].Repo = cache.Repo
		if cache.Skills[i].Source == "" {
			cache.Skills[i].Source = "skills-cli"
		}
		cache.Skills[i].Installed = false
		cache.Skills[i].InstallPath = ""
	}
	return cache.Skills, true
}

func (m *Manager) readDirectoryCache() ([]Skill, bool) {
	data, err := os.ReadFile(m.directoryCachePath())
	if err != nil {
		return nil, false
	}
	var cache directoryCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, false
	}
	for i := range cache.Skills {
		if cache.Skills[i].Source == "" {
			cache.Skills[i].Source = "skills.sh"
		}
		cache.Skills[i].Installed = false
		cache.Skills[i].InstallPath = ""
	}
	return cache.Skills, true
}

func (m *Manager) writeDirectoryCache(listed []Skill) error {
	dir := filepath.Join(m.cwd, ".forge", "cache", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cache := directoryCache{
		UpdatedAt: time.Now(),
		Skills:    cacheableDirectorySkills(listed),
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.directoryCachePath(), data, 0o644)
}

func (m *Manager) DirectoryCacheInfo() CacheInfo {
	info := CacheInfo{Repo: "skills.sh", Path: m.directoryCachePath()}
	data, err := os.ReadFile(info.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			info.Error = err.Error()
		}
		return info
	}
	var cache directoryCache
	if err := json.Unmarshal(data, &cache); err != nil {
		info.Exists = true
		info.Error = err.Error()
		return info
	}
	info.Exists = true
	info.UpdatedAt = cache.UpdatedAt
	info.Count = len(cache.Skills)
	return info
}

func (m *Manager) directoryCachePath() string {
	return filepath.Join(m.cwd, ".forge", "cache", "skills", "skills_sh.json")
}

func (m *Manager) writeRepositoryCache(repo string, listed []Skill) error {
	dir := filepath.Join(m.cwd, ".forge", "cache", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cache := repositoryCache{
		Repo:      repo,
		UpdatedAt: time.Now(),
		Skills:    cacheableSkills(repo, listed),
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.repositoryCachePath(repo), data, 0o644)
}

func (m *Manager) repositoryCachePath(repo string) string {
	return filepath.Join(m.cwd, ".forge", "cache", "skills", safeCacheName(repo)+".json")
}

func cacheableSkills(repo string, listed []Skill) []Skill {
	out := make([]Skill, 0, len(listed))
	for _, item := range listed {
		out = append(out, Skill{
			Name:        item.Name,
			Description: item.Description,
			Version:     item.Version,
			Repo:        repo,
			Source:      "skills-cli",
		})
	}
	return out
}

func cacheableDirectorySkills(listed []Skill) []Skill {
	out := make([]Skill, 0, len(listed))
	for _, item := range listed {
		out = append(out, Skill{
			Name:        item.Name,
			Description: item.Description,
			Version:     item.Version,
			Installs:    item.Installs,
			Repo:        item.Repo,
			Source:      "skills.sh",
		})
	}
	return out
}

func directoryDescription(item directorySkill) string {
	description := strings.TrimSpace(item.Description)
	if item.Installs > 0 {
		installs := strconv.Itoa(item.Installs) + " installs"
		if description == "" {
			return installs
		}
		return description + " (" + installs + ")"
	}
	return description
}

func safeCacheName(repo string) string {
	var b strings.Builder
	for _, r := range repo {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func ParseListOutput(repo, output string) []Skill {
	var skills []Skill
	seen := map[string]bool{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(ansiPattern.ReplaceAllString(raw, ""))
		if line == "" || shouldSkipListLine(line) {
			continue
		}
		name, description, ok := parseSkillLine(line)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		skills = append(skills, Skill{
			Name:        name,
			Description: description,
			Repo:        repo,
			Source:      "skills-cli",
		})
	}
	return skills
}

func shouldSkipListLine(line string) bool {
	lower := strings.ToLower(line)
	prefixSkips := []string{
		"available skills",
		"skills in",
		"select skills",
		"use arrow",
	}
	for _, skip := range prefixSkips {
		if strings.HasPrefix(lower, skip) {
			return true
		}
	}
	compact := strings.ReplaceAll(lower, "|", " ")
	compact = strings.Join(strings.Fields(compact), " ")
	return lower == "repository" || lower == "name description" || compact == "name description" ||
		strings.Contains(lower, "---") ||
		strings.HasPrefix(line, "npx ") || strings.HasPrefix(line, "#") || strings.HasPrefix(lower, "install ")
}

func parseSkillLine(line string) (string, string, bool) {
	line = strings.TrimLeftFunc(line, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
	line = strings.TrimSpace(strings.TrimPrefix(line, "[ ]"))
	line = strings.TrimSpace(strings.TrimPrefix(line, "[x]"))
	if line == "" {
		return "", "", false
	}

	for _, sep := range []string{" - ", ": ", " | ", "\t"} {
		if idx := strings.Index(line, sep); idx > 0 {
			name := strings.TrimSpace(line[:idx])
			description := cleanDescription(line[idx+len(sep):])
			if validSkillName(name) {
				return name, description, true
			}
		}
	}

	fields := strings.Fields(line)
	if len(fields) == 0 || !validSkillName(fields[0]) {
		return "", "", false
	}
	description := ""
	if len(fields) > 1 {
		description = cleanDescription(strings.Join(fields[1:], " "))
	}
	return fields[0], description, true
}

func cleanDescription(description string) string {
	description = strings.TrimSpace(description)
	description = strings.Trim(description, "|")
	return strings.TrimSpace(description)
}

var directoryAnchorPattern = regexp.MustCompile(`href="/([^"/?#]+/[^"/?#]+/[^"/?#]+)"`)

func ParseDirectoryHTML(html string) []Skill {
	seen := map[string]bool{}
	var out []Skill
	for _, match := range directoryAnchorPattern.FindAllStringSubmatch(html, -1) {
		if len(match) < 2 {
			continue
		}
		parts := strings.Split(match[1], "/")
		if len(parts) != 3 {
			continue
		}
		repo := parts[0] + "/" + parts[1]
		name := parts[2]
		if !validSkillName(name) || strings.HasPrefix(repo, "docs/") || strings.HasPrefix(repo, "api/") {
			continue
		}
		key := repo + "@" + name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Skill{
			Name:        name,
			Description: repo,
			Repo:        repo,
			Source:      "skills.sh",
		})
	}
	return out
}

func validSkillName(name string) bool {
	if name == "" || strings.ContainsAny(name, " /\\") {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func skillSearchDirs(cwd string) []string {
	dirs := []string{
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(cwd, ".forge", "skills"),
	}
	if home, err := userHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".codex", "skills"),
			filepath.Join(home, ".forge", "skills"),
		)
	}
	return dirs
}
