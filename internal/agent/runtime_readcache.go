package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"

	"forge/internal/tools"
)

// Per-turn cache for read_file results. The agent occasionally re-reads the
// same file across consecutive steps (e.g. after a search to confirm context
// before editing); serving the second read from cache saves a tool round-trip
// and the prefill of its content into the next prompt.
//
// Lifetime is bounded to a single turn: r.resetReadCache() is called at the
// top of run() so cross-turn drift cannot serve stale bytes. Within a turn,
// any tool that reports ChangedFiles invalidates the matching entries; a
// successful run_command flushes the entire cache because we cannot tell what
// arbitrary commands wrote.

type readCacheEntry struct {
	result      tools.Result
	observation string
}

type readCacheStore struct {
	mu      sync.Mutex
	entries map[string]*readCacheEntry
	hits    int
}

func newReadCache() *readCacheStore {
	return &readCacheStore{entries: map[string]*readCacheEntry{}}
}

// canonReadPath normalizes the {"path": "..."} field of a read_file input to
// the absolute path used as the cache key. Returns "" if the input is not a
// valid read_file payload (e.g. parse error) — the caller falls through to
// the live tool dispatch in that case.
func (r *Runtime) canonReadPath(input json.RawMessage) string {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return ""
	}
	p := strings.TrimSpace(req.Path)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.CWD, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return abs
}

func (r *Runtime) resetReadCache() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.readCache = newReadCache()
	r.mu.Unlock()
}

func (r *Runtime) lookupReadCache(input json.RawMessage) (*tools.Result, string, bool) {
	if r == nil || r.readCache == nil {
		return nil, "", false
	}
	key := r.canonReadPath(input)
	if key == "" {
		return nil, "", false
	}
	r.readCache.mu.Lock()
	defer r.readCache.mu.Unlock()
	entry, ok := r.readCache.entries[key]
	if !ok {
		return nil, "", false
	}
	r.readCache.hits++
	// Return a defensive copy so callers can mutate without poisoning cache.
	res := entry.result
	return &res, entry.observation, true
}

func (r *Runtime) storeReadCache(input json.RawMessage, result *tools.Result, observation string) {
	if r == nil || r.readCache == nil || result == nil {
		return
	}
	key := r.canonReadPath(input)
	if key == "" {
		return
	}
	r.readCache.mu.Lock()
	defer r.readCache.mu.Unlock()
	r.readCache.entries[key] = &readCacheEntry{
		result:      *result,
		observation: observation,
	}
}

// invalidateReadCachePaths drops cache entries for the given paths. Paths may
// be absolute or workspace-relative; both are normalized the same way as the
// read_file inputs so an edit_file with a relative path correctly invalidates
// a read that used the same relative path.
func (r *Runtime) invalidateReadCachePaths(paths []string) {
	if r == nil || r.readCache == nil || len(paths) == 0 {
		return
	}
	r.readCache.mu.Lock()
	defer r.readCache.mu.Unlock()
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(r.CWD, p)
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		delete(r.readCache.entries, abs)
	}
}

// flushReadCache drops all entries — used after run_command, which can write
// arbitrary files we cannot enumerate from the tool result.
func (r *Runtime) flushReadCache() {
	if r == nil || r.readCache == nil {
		return
	}
	r.readCache.mu.Lock()
	defer r.readCache.mu.Unlock()
	r.readCache.entries = map[string]*readCacheEntry{}
}

func (r *Runtime) readCacheHits() int {
	if r == nil || r.readCache == nil {
		return 0
	}
	r.readCache.mu.Lock()
	defer r.readCache.mu.Unlock()
	return r.readCache.hits
}
