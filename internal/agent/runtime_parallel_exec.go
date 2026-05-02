package agent

import (
	"context"
	"sync"

	"forge/internal/llm"
	"forge/internal/tools"
)

// preExecutedTool holds the result of a tool call computed ahead of the
// sequential dispatch loop. The loop reads from this map by tool-call ID
// instead of re-invoking the tool, while keeping every existing guard,
// failure-tracking, and message-append step intact.
type preExecutedTool struct {
	result      *tools.Result
	observation string
}

// maybePreExecuteParallelExecuteTasks runs a homogeneous batch of execute_task
// calls concurrently and returns a map of tool-call ID -> result. It returns
// nil (causing the caller to fall back to its sequential path) unless ALL of
// the following hold:
//
//   - The agent is in build mode (only build dispatches execute_task in bulk).
//   - Every call in toolCalls is execute_task. Mixed batches stay sequential
//     because a write_file mid-batch can change what subsequent tasks should
//     see; serializing keeps that contract obvious.
//   - There are at least 2 calls (no point spinning a worker pool for one).
//   - Build.Subagents.Concurrency > 1.
//
// Concurrency is bounded by Build.Subagents.Concurrency, which in turn is
// normalized against ModelLoading.ParallelSlots — so we never spawn more
// concurrent subagents than the LM Studio backend has decode slots for.
func (r *Runtime) maybePreExecuteParallelExecuteTasks(ctx context.Context, toolCalls []llm.ToolCall, events chan<- Event) map[string]preExecutedTool {
	if r == nil || r.Mode != "build" || len(toolCalls) < 2 {
		return nil
	}
	concurrency := r.Config.Build.Subagents.Concurrency
	if concurrency < 2 {
		return nil
	}
	for _, tc := range toolCalls {
		if FromNativeToolCall(tc).Name != "execute_task" {
			return nil
		}
	}

	results := make(map[string]preExecutedTool, len(toolCalls))
	var resultsMu sync.Mutex

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, tc := range toolCalls {
		tc := tc
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			call := FromNativeToolCall(tc)
			result, observation := r.executeTool(ctx, call, events)
			resultsMu.Lock()
			results[tc.ID] = preExecutedTool{result: result, observation: observation}
			resultsMu.Unlock()
		}()
	}
	wg.Wait()
	return results
}
