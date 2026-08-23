// Package filechanges computes a durable, workspace-relative best-effort view
// of files changed by successful tool calls. Dedicated write_file and edit_file
// calls are accounted from their structured arguments/results. Shell coverage
// is intentionally limited to recognized output-redirection syntax; arbitrary
// successful shell mutators, scripts, compilers, and generators may be absent.
// The record is therefore a review aid, not a complete filesystem audit log.
//
// Two correctness rules drive this package:
//
//  1. Only successful operations count. A failed write_file/edit_file (for
//     example "old_text not found") or a shell command that exits non-zero must
//     not appear as a file change.
//  2. Paths are normalized against the operation's working directory and the
//     session workspace root so recognized paths match the files on disk. Shell
//     redirects like "../reports/out.txt" run from a child workdir resolve to
//     the same workspace-relative path the file actually lives at.
package filechanges

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"aegis-agent/internal/session"
)

// FileChange re-exports the durable session type so callers can depend on this
// package alone.
type FileChange = session.FileChange

type accumulator struct {
	FileChange
	firstSeen int
}

// Collector aggregates the recognized per-file mutation hints from a session
// message stream. It pairs assistant tool calls with their tool results so only
// successful operations count and normalizes recognized paths relative to the
// workspace root. It does not observe arbitrary shell filesystem effects.
type Collector struct {
	workdir string
	changes map[string]*accumulator
	order   int
	// pending holds tool calls awaiting their result, keyed by tool_call_id.
	pending map[string]session.ToolCall
}

// NewCollector returns a Collector that normalizes paths relative to workdir.
// workdir may be empty, in which case paths are only cleaned, not rebased.
func NewCollector(workdir string) *Collector {
	return &Collector{
		workdir: strings.TrimSpace(workdir),
		changes: map[string]*accumulator{},
		pending: map[string]session.ToolCall{},
	}
}

// AddMessage feeds one message into the collector. Assistant messages register
// pending tool calls; tool messages resolve them and, on success, apply the
// derived file changes.
func (c *Collector) AddMessage(msg session.Message) {
	for _, call := range msg.ToolCalls {
		id := strings.TrimSpace(call.ID)
		if id == "" || !isFileMutatingTool(call.Name) {
			continue
		}
		c.pending[id] = call
	}
	for _, result := range msg.ToolResults {
		id := strings.TrimSpace(result.ToolCallID)
		if id == "" {
			continue
		}
		call, ok := c.pending[id]
		if !ok {
			continue
		}
		delete(c.pending, id)
		if result.IsError {
			continue
		}
		c.applyCall(call, result)
	}
}

func (c *Collector) applyCall(call session.ToolCall, result session.ToolResult) {
	var args map[string]any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	switch call.Name {
	case "write_file":
		path := c.resolvePath(resultMetadataPath(result), pathArg(args), "")
		item := c.ensure(path)
		if item == nil {
			return
		}
		item.Writes++
		if content, ok := args["content"].(string); ok {
			item.LinesAdded += CountTextLines(content)
		}
	case "edit_file":
		path := c.resolvePath(resultMetadataPath(result), pathArg(args), "")
		item := c.ensure(path)
		if item == nil {
			return
		}
		item.Edits++
		oldText, _ := args["old_text"].(string)
		newText, _ := args["new_text"].(string)
		oldLines := CountTextLines(oldText)
		newLines := CountTextLines(newText)
		if newLines > oldLines {
			item.LinesAdded += newLines - oldLines
		}
		if oldLines > newLines {
			item.LinesRemoved += oldLines - newLines
		}
	case "shell":
		command, _ := args["command"].(string)
		opCwd, _ := args["workdir"].(string)
		for _, redirect := range CollectShellRedirectTargets(command) {
			path := c.resolvePath("", redirect.Path, opCwd)
			item := c.ensure(path)
			if item == nil {
				continue
			}
			if redirect.Append {
				item.Edits++
			} else {
				item.Writes++
			}
		}
	}
}

func (c *Collector) ensure(pathValue string) *accumulator {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return nil
	}
	if current := c.changes[pathValue]; current != nil {
		return current
	}
	current := &accumulator{
		FileChange: FileChange{Path: pathValue},
		firstSeen:  c.order,
	}
	c.order++
	c.changes[pathValue] = current
	return current
}

// resolvePath produces a stable, workspace-relative display path. It prefers an
// absolute path the tool already resolved (metaPath), then the raw argument
// path interpreted relative to opCwd (which itself may be relative to the
// workspace root).
func (c *Collector) resolvePath(metaPath, argPath, opCwd string) string {
	candidate := strings.TrimSpace(metaPath)
	if candidate == "" {
		candidate = strings.TrimSpace(argPath)
	}
	if candidate == "" {
		return ""
	}
	abs := candidate
	if !filepath.IsAbs(abs) {
		base := c.workdir
		if cwd := strings.TrimSpace(opCwd); cwd != "" {
			if filepath.IsAbs(cwd) {
				base = cwd
			} else if base != "" {
				base = filepath.Join(base, cwd)
			} else {
				base = cwd
			}
		}
		if base != "" {
			abs = filepath.Join(base, candidate)
		} else {
			abs = filepath.Clean(candidate)
		}
	}
	if c.workdir != "" {
		if rel, err := filepath.Rel(c.workdir, abs); err == nil {
			rel = filepath.ToSlash(rel)
			if rel != "." {
				return rel
			}
		}
	}
	return filepath.ToSlash(filepath.Clean(abs))
}

// Summaries returns the accumulated recognized changes ordered by first
// appearance.
func (c *Collector) Summaries() []FileChange {
	if len(c.changes) == 0 {
		return nil
	}
	out := make([]FileChange, 0, len(c.changes))
	for _, item := range c.changes {
		out = append(out, item.FileChange)
	}
	sort.Slice(out, func(i, j int) bool {
		left := c.changes[out[i].Path]
		right := c.changes[out[j].Path]
		if left.firstSeen != right.firstSeen {
			return left.firstSeen < right.firstSeen
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// FromMessages computes the best-effort file-change summary for a message
// slice.
func FromMessages(workdir string, messages []session.Message) []FileChange {
	collector := NewCollector(workdir)
	for _, msg := range messages {
		collector.AddMessage(msg)
	}
	return collector.Summaries()
}

// FromCall computes the recognized file-change deltas a single successful tool
// call produced. It returns nil for failed results or non-mutating tools.
func FromCall(workdir string, call session.ToolCall, result session.ToolResult) []FileChange {
	if result.IsError || !isFileMutatingTool(call.Name) {
		return nil
	}
	collector := NewCollector(workdir)
	collector.applyCall(call, result)
	return collector.Summaries()
}

// Merge folds additions into base, summing counts per path and preserving the
// existing ordering (new paths are appended in arrival order). It returns a new
// slice and does not mutate its inputs.
func Merge(base, additions []FileChange) []FileChange {
	out := make([]FileChange, len(base))
	copy(out, base)
	index := make(map[string]int, len(out))
	for i, item := range out {
		index[item.Path] = i
	}
	for _, add := range additions {
		path := strings.TrimSpace(add.Path)
		if path == "" {
			continue
		}
		if i, ok := index[path]; ok {
			out[i].Writes += add.Writes
			out[i].Edits += add.Edits
			out[i].LinesAdded += add.LinesAdded
			out[i].LinesRemoved += add.LinesRemoved
			continue
		}
		add.Path = path
		index[path] = len(out)
		out = append(out, add)
	}
	return out
}

func isFileMutatingTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "write_file", "edit_file", "shell":
		return true
	}
	return false
}

func pathArg(args map[string]any) string {
	if args == nil {
		return ""
	}
	value, _ := args["path"].(string)
	return value
}

func resultMetadataPath(result session.ToolResult) string {
	if result.Metadata == nil {
		return ""
	}
	value, _ := result.Metadata["path"].(string)
	return value
}

// CountTextLines counts the lines a body of text contributes.
func CountTextLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}
