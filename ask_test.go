package claude

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agentcarto/core/domain"
)

// askSession is a prompt answered by one AskUserQuestion call with the given input, in the shape
// Claude Code logs it: the call alone in its record, which carries stop_reason "tool_use".
func askSession(input string) []string {
	return []string{
		`{"uuid":"u1","parentUuid":null,"timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","content":"tidy the chapters"}}`,
		`{"uuid":"a1","parentUuid":"u1","timestamp":"2026-01-01T00:00:05Z","type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":` + input + `}]}}`,
	}
}

// What the agent asks the user is conversation: the readable question, with nothing of a tool call
// about it.
func TestParseAskCallIsAQuestion(t *testing.T) {
	src := filepath.Join(t.TempDir(), "s.jsonl")
	writeSession(t, src, askSession(`{"questions":[`+
		`{"question":"Which chapters should change?","header":"Scope","multiSelect":false,"options":[`+
		`{"label":"All","description":"every chapter","preview":"ch01.md\nch02.md"},`+
		`{"label":"One","description":"only the first"}]},`+
		`{"question":"Which checks should run?","header":"Checks","multiSelect":true,"options":[`+
		`{"label":"proofread","description":"spelling and notation"},`+
		`{"label":"verify","description":""}]}]}`)...)
	ev, nodes, _, _, _, _, _, _ := parse(context.Background(), src)

	want := "Scope: Which chapters should change?\n" +
		"- All: every chapter\n" +
		"    ch01.md\n" +
		"    ch02.md\n" +
		"- One: only the first\n" +
		"\n" +
		"Checks: Which checks should run? (multi-select)\n" +
		"- proofread: spelling and notation\n" +
		"- verify"
	if len(nodes) != 2 || len(nodes[1].Events) != 1 {
		t.Fatalf("nodes = %#v, want the prompt and the question", nodes)
	}
	q := nodes[1].Events[0]
	if q.Kind != domain.EventQuestion || q.Text != want {
		t.Fatalf("kind = %q, text:\n%s\nwant a question reading:\n%s", q.Kind, q.Text, want)
	}
	if q.ToolName != "" || q.ToolArg != "" || q.ToolDetail != "" {
		t.Fatalf("a question should carry no tool fields: %#v", q)
	}
	// The record's stop_reason says a tool is being used; the question is that tool, so no call
	// is made up to stand for one.
	if hasEventKind(ev, domain.EventToolCall) {
		t.Fatalf("events hold a tool call nobody made: %#v", ev)
	}
}

// A call that cannot be read whole (a question without its text, an option without its label)
// stays what it was, a tool call with its raw input, so nothing the input holds goes missing.
func TestParseAskCallWithoutReadableQuestionsStaysAToolCall(t *testing.T) {
	for _, input := range []string{
		`{}`,
		`{"questions":"which?"}`,
		`{"questions":[{"header":"Scope","options":[{"label":"All"}]}]}`,
		`{"questions":[{"options":[{"description":"every chapter"}],"question":"Which chapters?"}]}`,
		`{"questions":[{"options":["All","One"],"question":"Which chapters?"}]}`,
		`{"questions":[{"question":"Which chapters?"},{"header":"Checks"}]}`,
	} {
		src := filepath.Join(t.TempDir(), "s.jsonl")
		writeSession(t, src, askSession(input)...)
		_, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
		if len(nodes) != 2 || len(nodes[1].Events) != 1 {
			t.Fatalf("%s: nodes = %#v", input, nodes)
		}
		if e := nodes[1].Events[0]; e.Kind != domain.EventToolCall || e.ToolName != "AskUserQuestion" || e.Text != input {
			t.Fatalf("%s: got %#v, want the tool call with its input", input, e)
		}
	}
}

// A session that stops at a question is waiting on the user exactly as it was when the question
// was a tool call, so the list keeps reporting a tool in progress.
func TestScanSessionEndingInAQuestionReportsAToolInProgress(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "proj", "s.jsonl")
	writeSession(t, src, askSession(`{"questions":[{"question":"Which chapters should change?","header":"Scope","options":[{"label":"All"}]}]}`)...)
	if got := scanOne(t, root, src).LastKind; got != domain.EventToolCall {
		t.Fatalf("LastKind = %q, want %q", got, domain.EventToolCall)
	}
}
