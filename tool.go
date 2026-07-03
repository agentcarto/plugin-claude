package claude

import (
	"encoding/json"
	"strings"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
)

// This file normalizes Claude's tool-call payloads (JSON tool_use inputs) and
// task notifications into the display fields the host renders generically
// (ToolArg/ToolDetail/Changes, EventTask).

func jsonObject(raw string) map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) == nil {
		return m
	}
	return nil
}

func stringArg(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// annotateTool fills ToolArg/ToolDetail/Changes on a tool_call event from
// Claude's JSON tool input.
func annotateTool(e *domain.Event) {
	inp := jsonObject(e.Text)
	if inp == nil {
		return
	}
	if editTools[e.ToolName] {
		if fc, ok := editChange(e.ToolName, inp); ok {
			e.Changes = []domain.FileChange{fc}
			e.ToolArg = fc.Path
		}
		return
	}
	if e.ToolName == "Bash" {
		if cmd := strings.TrimSpace(stringArg(inp, "command")); cmd != "" {
			e.ToolArg = "$ " + strings.Join(strings.Fields(cmd), " ")
			e.ToolDetail = cmd
			if b, ok := inp["run_in_background"].(bool); ok && b {
				e.ToolArg += " &"
				e.ToolDetail += "\n\n(run in background)"
			}
			return
		}
	}
	e.ToolArg = common.ToolArgFromJSON(e.Text)
}

// editChange builds the normalized FileChange for one edit-tool invocation,
// reconstructing apply_patch hunks from the old/new strings in the input.
func editChange(name string, inp map[string]any) (domain.FileChange, bool) {
	path := stringArg(inp, "file_path")
	if path == "" {
		path = stringArg(inp, "notebook_path")
	}
	if path == "" {
		return domain.FileChange{}, false
	}
	var lines []string
	added, removed := 0, 0
	switch name {
	case "Write":
		lines, added, removed = unifiedHunks("", stringArg(inp, "content"))
	case "NotebookEdit":
		lines, added, removed = unifiedHunks(stringArg(inp, "old_source"), stringArg(inp, "new_source"))
	case "MultiEdit":
		if edits, ok := inp["edits"].([]any); ok {
			for _, v := range edits {
				ed, ok := v.(map[string]any)
				if !ok {
					continue
				}
				h, a, r := unifiedHunks(stringArg(ed, "old_string"), stringArg(ed, "new_string"))
				lines = append(lines, h...)
				added += a
				removed += r
			}
		}
	default:
		lines, added, removed = unifiedHunks(stringArg(inp, "old_string"), stringArg(inp, "new_string"))
	}
	op := "update"
	if name == "Write" {
		// Whether the file existed before is not recorded; render as an addition.
		op = "add"
	}
	return domain.FileChange{Path: path, Op: op, Added: added, Removed: removed, Diff: strings.Join(lines, "\n")}, true
}

type taskNotification struct {
	id, status, summary, result string
}

// parseTaskNotification extracts the fields of a <task-notification> block.
// The prefix requirement keeps prompts that merely quote the tag from
// being classified as task notices.
func parseTaskNotification(text string) (taskNotification, bool) {
	if !strings.HasPrefix(strings.TrimSpace(text), "<task-notification>") {
		return taskNotification{}, false
	}
	grab := func(tag string) string {
		open := "<" + tag + ">"
		close := "</" + tag + ">"
		i := strings.Index(text, open)
		if i < 0 {
			return ""
		}
		i += len(open)
		j := strings.Index(text[i:], close)
		if j < 0 {
			return ""
		}
		return strings.TrimSpace(text[i : i+j])
	}
	return taskNotification{id: grab("task-id"), status: grab("status"), summary: grab("summary"), result: grab("result")}, true
}

func shortID(s string) string {
	if s == "" {
		return "????????"
	}
	r := []rune(s)
	if len(r) > 8 {
		r = r[:8]
	}
	return string(r)
}

// taskEvent rewrites a <task-notification> user event into a normalized
// EventTask with the display label in ToolArg and the body in ToolDetail.
func taskEvent(e *domain.Event, t taskNotification) {
	e.Kind = domain.EventTask
	e.ToolArg = shortID(t.id)
	if t.status != "" {
		e.ToolArg += " [" + t.status + "]"
	}
	body := ""
	if t.summary != "" {
		body = t.summary + "\n\n"
	}
	if t.result != "" {
		body += t.result
	} else {
		body += "(no result body)"
	}
	e.ToolDetail = body
}
