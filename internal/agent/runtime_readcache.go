package agent

import (
	"encoding/json"
	"fmt"
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
	// serveCount tracks how many times this entry was served from cache
	// (excluding the original store). Cache lookups annotate the
	// served result with this count so the model gets an explicit
	// signal it just re-read a file it already saw this turn — small
	// local models otherwise loop on read_file calls until they hit
	// the consecutive-read-only guard.
	serveCount int
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
	entry.serveCount++
	// Annotate so the model sees an explicit "you already read this"
	// note in the tool result. The cached entry itself is left clean
	// (the annotation is built per-lookup); only serveCount mutates.
	res := annotateRereadResult(entry.result, entry.serveCount)
	obs := "Tool result for read_file:\n" + summarizeResult(res)
	return &res, obs, true
}

// annotateRereadResult prepends a NOTE block to the cached read_file
// result telling the model "you already saw this file this turn".
// Without this, models like Qwen3.6 cheerfully re-read the same path
// 30+ times in a single session because the prompt response looks
// identical to a fresh read.
func annotateRereadResult(result tools.Result, serveCount int) tools.Result {
	out := result
	if len(out.Content) == 0 {
		return out
	}
	timesWord := "time"
	if serveCount > 1 {
		timesWord = "times"
	}
	note := fmt.Sprintf("[NOTE: you already read this file %d %s in this turn. The content below is unchanged from your earlier read. Re-reading the same path costs a step but adds no new information. If you forgot what was here, write the relevant excerpts down in your reasoning before you move on. If you need a different range of lines, call read_file again with offset+limit instead of repeating the same call.]\n\n", serveCount, timesWord)
	out.Content = append([]tools.ContentBlock(nil), out.Content...)
	first := out.Content[0]
	out.Content[0] = tools.ContentBlock{
		Type: first.Type,
		Text: note + first.Text,
		Path: first.Path,
	}
	return out
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
