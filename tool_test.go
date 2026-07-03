package claude

import (
	"strings"
	"testing"

	"github.com/agentcarto/core/domain"
)

func TestAnnotateToolBash(t *testing.T) {
	e := domain.Event{Kind: domain.EventToolCall, ToolName: "Bash", Text: `{"command":"make \n check","run_in_background":true}`}
	annotateTool(&e)
	if e.ToolArg != "$ make check &" {
		t.Fatalf("arg=%q", e.ToolArg)
	}
	if !strings.Contains(e.ToolDetail, "make \n check") || !strings.Contains(e.ToolDetail, "(run in background)") {
		t.Fatalf("detail=%q", e.ToolDetail)
	}
}

func TestAnnotateToolEdit(t *testing.T) {
	e := domain.Event{Kind: domain.EventToolCall, ToolName: "Edit", Text: `{"file_path":"a.go","old_string":"x\ny","new_string":"x\nz"}`}
	annotateTool(&e)
	if len(e.Changes) != 1 {
		t.Fatalf("changes=%+v", e.Changes)
	}
	fc := e.Changes[0]
	if fc.Path != "a.go" || fc.Op != "update" || fc.Added != 1 || fc.Removed != 1 {
		t.Fatalf("fc=%+v", fc)
	}
	if !strings.Contains(fc.Diff, "-y") || !strings.Contains(fc.Diff, "+z") || !strings.Contains(fc.Diff, "@@") {
		t.Fatalf("diff=%q", fc.Diff)
	}
}

func TestAnnotateToolWriteIsAdd(t *testing.T) {
	e := domain.Event{Kind: domain.EventToolCall, ToolName: "Write", Text: `{"file_path":"b.txt","content":"one\ntwo"}`}
	annotateTool(&e)
	if len(e.Changes) != 1 || e.Changes[0].Op != "add" || e.Changes[0].Added != 2 || e.Changes[0].Removed != 0 {
		t.Fatalf("changes=%+v", e.Changes)
	}
}

func TestAnnotateToolGenericArg(t *testing.T) {
	e := domain.Event{Kind: domain.EventToolCall, ToolName: "Grep", Text: `{"pattern":"needle","path":"/repo"}`}
	annotateTool(&e)
	// "path" precedes "pattern" in the salient-key order.
	if e.ToolArg != "/repo" {
		t.Fatalf("arg=%q", e.ToolArg)
	}
}

func TestTaskNotificationBecomesTaskEvent(t *testing.T) {
	text := "<task-notification>\n<task-id>abcdef123456</task-id>\n<status>completed</status>\n<summary>did work</summary>\n<result>line1\nline2</result>\n</task-notification>"
	es := []domain.Event{{Kind: domain.EventUser, Text: text}}
	annotate(es)
	e := es[0]
	if e.Kind != domain.EventTask || e.Prompt != "" {
		t.Fatalf("event=%+v", e)
	}
	if e.ToolArg != "abcdef12 [completed]" {
		t.Fatalf("arg=%q", e.ToolArg)
	}
	if !strings.HasPrefix(e.ToolDetail, "did work\n\nline1") {
		t.Fatalf("detail=%q", e.ToolDetail)
	}
}

func TestQuotedTaskTagStaysUserPrompt(t *testing.T) {
	es := []domain.Event{{Kind: domain.EventUser, Text: "why does <task-notification> render oddly?"}}
	annotate(es)
	if es[0].Kind != domain.EventUser || es[0].Prompt == "" {
		t.Fatalf("event=%+v", es[0])
	}
}

func TestUnifiedHunksCounts(t *testing.T) {
	lines, a, r := unifiedHunks("a\nb\nc", "a\nx\nc")
	if a != 1 || r != 1 {
		t.Fatalf("a=%d r=%d", a, r)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "@@") || !strings.Contains(joined, "-b") || !strings.Contains(joined, "+x") || !strings.Contains(joined, " a") {
		t.Fatalf("hunks=%q", joined)
	}
}
