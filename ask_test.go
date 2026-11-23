package claude

import (
	"context"
	"path/filepath"
	"strconv"
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

// askedQuestions are the questions the answer tests ask: three, the second of them multi-select.
const askedQuestions = `[` +
	`{"question":"Which chapters should change?","header":"Scope","multiSelect":false,"options":[{"label":"All","description":"every chapter"},{"label":"One","description":"only the first"}]},` +
	`{"question":"Which checks should run?","header":"Checks","multiSelect":true,"options":[{"label":"proofread","description":"spelling"},{"label":"verify","description":"facts"}]},` +
	`{"question":"Which order?","header":"Order","multiSelect":false,"options":[{"label":"By chapter","description":""},{"label":"By check","description":""}]}]`

// answeredBody is what Claude Code tells the model once the user has answered.
const answeredBody = `Your questions have been answered: "Which chapters should change?"="All". You can now continue with these answers in mind.`

// rejectedBody is what Claude Code tells the model when a tool call was turned down.
const rejectedBody = `The user doesn't want to proceed with this tool use. The tool use was rejected.`

// askResult is the record Claude Code writes when the call of askSession returns: one tool_result
// with a string for its body, and beside the message what the record says of that result (fields,
// each with its leading comma).
func askResult(body string, failed bool, fields string) string {
	block := `{"type":"tool_result","tool_use_id":"toolu_1","content":` + strconv.Quote(body)
	if failed {
		block += `,"is_error":true`
	}
	return `{"uuid":"r1","parentUuid":"a1","timestamp":"2026-01-01T00:01:00Z","type":"user","message":{"role":"user","content":[` + block + `}]}` + fields + `}`
}

// answered is the record field holding Claude Code's account of an answered call: the questions
// asked, then the given fields of the account (each with its leading comma).
func answered(fields string) string {
	return `,"toolUseResult":{"questions":` + askedQuestions + fields + `}`
}

// parseAskResult parses a session that asks askedQuestions and gets the given result back, and
// returns the event that result became.
func parseAskResult(t *testing.T, result string) domain.Event {
	t.Helper()
	src := filepath.Join(t.TempDir(), "s.jsonl")
	writeSession(t, src, append(askSession(`{"questions":`+askedQuestions+`}`), result)...)
	_, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if len(nodes) != 3 || len(nodes[1].Events) != 1 || nodes[1].Events[0].Kind != domain.EventQuestion || len(nodes[2].Events) != 1 {
		t.Fatalf("nodes = %#v, want the prompt, the question and its result", nodes)
	}
	return nodes[2].Events[0]
}

// What the user answered is conversation too: a line per question in the order asked, named by
// the question's header, with what the user wrote beside a choice beneath it.
func TestParseAskResultIsTheAnswer(t *testing.T) {
	a := parseAskResult(t, askResult(answeredBody, false, answered(
		`,"answers":{"Which checks should run?":"proofread, verify","Which chapters should change?":"All"}`+
			`,"annotations":{"Which chapters should change?":{"preview":"ch01.md","notes":"skip the appendix"}}`)))

	want := "Scope: All\n" +
		"  note: skip the appendix\n" +
		"Checks: proofread, verify\n" +
		"Order: (no answer)"
	if a.Kind != domain.EventAnswer || a.Text != want {
		t.Fatalf("kind = %q, text:\n%s\nwant an answer reading:\n%s", a.Kind, a.Text, want)
	}
	if a.ToolName != "" || a.ToolArg != "" || a.ToolDetail != "" {
		t.Fatalf("an answer should carry no tool fields: %#v", a)
	}
	// An answer belongs to the turn its question was asked in.
	if a.Prompt != "" || a.Command != "" {
		t.Fatalf("an answer should open no turn: %#v", a)
	}
}

// The record's account of an answer is read whole or not at all. Where it is missing, or holds
// more than a line per question can say (a reply in the user's own words, an answer of another
// shape), the answer is what Claude Code told the model, which leaves nothing out.
func TestParseAskResultNotReadWholeIsAnsweredByItsBody(t *testing.T) {
	for _, c := range []struct{ name, fields string }{
		{"no account", ``},
		{"an account of another shape", `,"toolUseResult":"answered"`},
		{"no answers", answered(``)},
		{"a reply in the user's words", answered(`,"answers":{},"response":"neither, drop the task"`)},
		{"an answer to a question not asked", answered(`,"answers":{"Which files?":"All"}`)},
		{"a list for an answer", answered(`,"answers":{"Which checks should run?":["proofread","verify"]}`)},
		{"a note on a question not asked", answered(`,"answers":{},"annotations":{"Which files?":{"notes":"skip the appendix"}}`)},
		{"a note of another shape", answered(`,"answers":{},"annotations":{"Which order?":{"notes":["skip","the appendix"]}}`)},
		{"a question without its text", `,"toolUseResult":{"questions":[{"header":"Scope"}],"answers":{}}`},
	} {
		a := parseAskResult(t, askResult(answeredBody, false, c.fields))
		if a.Kind != domain.EventAnswer || a.Text != answeredBody {
			t.Fatalf("%s: got %#v, want an answer holding the result's body", c.name, a)
		}
	}
}

// Turning the questions down is the user's response as well. What Claude Code passes on of it
// (the answers given so far, in its own wording) is kept whole.
func TestParseAskTurnedDownByTheUserIsAnAnswer(t *testing.T) {
	feedback := "The user wants to clarify these questions.\n    Questions asked:\n- \"Which chapters should change?\"\n  Answer: All\n- \"Which checks should run?\"\n  (No answer provided)"
	for _, c := range []struct{ fields, want string }{
		{`,"toolUseResult":"Error: rejected","toolDenialKind":"user-rejected","userFeedback":` + strconv.Quote(feedback), feedback},
		{`,"toolUseResult":"User rejected tool use","toolDenialKind":"user-rejected"`, "(declined to answer)"},
	} {
		a := parseAskResult(t, askResult(rejectedBody, true, c.fields))
		if a.Kind != domain.EventAnswer || a.Text != c.want {
			t.Fatalf("%s: got %#v, want an answer reading %q", c.fields, a, c.want)
		}
	}
}

// A call that failed without the user turning it down was never answered: its result stays a tool
// result.
func TestParseAskFailedOtherwiseStaysAToolResult(t *testing.T) {
	for _, fields := range []string{
		``,
		`,"toolUseResult":"Error: interrupted","toolDenialKind":"interrupted"`,
		`,"toolUseResult":"Error: denied","toolDenialKind":"permission-rule","userFeedback":"not now"`,
	} {
		a := parseAskResult(t, askResult(rejectedBody, true, fields))
		if a.Kind != domain.EventToolResult || a.Text != rejectedBody {
			t.Fatalf("%s: got %#v, want the tool result", fields, a)
		}
	}
}

// A call left a tool call (its input could not be read) keeps its result a tool result, so the
// pair reads as it always did.
func TestParseResultOfAnAskCallLeftAToolCallStaysAToolResult(t *testing.T) {
	src := filepath.Join(t.TempDir(), "s.jsonl")
	writeSession(t, src, append(askSession(`{"questions":"which?"}`),
		askResult(answeredBody, false, answered(`,"answers":{"Which chapters should change?":"All"}`)))...)
	_, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if len(nodes) != 3 || len(nodes[2].Events) != 1 {
		t.Fatalf("nodes = %#v", nodes)
	}
	if e := nodes[2].Events[0]; e.Kind != domain.EventToolResult || e.Text != answeredBody {
		t.Fatalf("got %#v, want the tool result", e)
	}
}

// The record's fields describe one result. An ask result sharing its record with another result
// is read from its own block alone: answered by its body, or, having failed, not taken for the
// user's refusal.
func TestParseAskResultSharingItsRecordIsReadFromItsBlock(t *testing.T) {
	for _, c := range []struct {
		name, block, fields string
		want                domain.Event
	}{
		{"answered", `"content":` + strconv.Quote(answeredBody), answered(`,"answers":{"Which chapters should change?":"One"}`),
			domain.Event{Kind: domain.EventAnswer, Text: answeredBody}},
		{"failed", `"content":` + strconv.Quote(rejectedBody) + `,"is_error":true`, `,"toolDenialKind":"user-rejected","userFeedback":"not now"`,
			domain.Event{Kind: domain.EventToolResult, Text: rejectedBody}},
	} {
		src := filepath.Join(t.TempDir(), "s.jsonl")
		writeSession(t, src,
			`{"uuid":"u1","parentUuid":null,"timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","content":"tidy the chapters"}}`,
			`{"uuid":"a1","parentUuid":"u1","timestamp":"2026-01-01T00:00:05Z","type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[`+
				`{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{"questions":`+askedQuestions+`}},`+
				`{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"ls"}}]}}`,
			`{"uuid":"r1","parentUuid":"a1","timestamp":"2026-01-01T00:01:00Z","type":"user","message":{"role":"user","content":[`+
				`{"type":"tool_result","tool_use_id":"toolu_1",`+c.block+`},`+
				`{"type":"tool_result","tool_use_id":"toolu_2","content":"ch01.md"}]}`+c.fields+`}`)
		_, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
		if len(nodes) != 3 || len(nodes[2].Events) != 2 {
			t.Fatalf("%s: nodes = %#v", c.name, nodes)
		}
		if got := nodes[2].Events[0]; got.Kind != c.want.Kind || got.Text != c.want.Text {
			t.Fatalf("%s: got %#v, want a %s holding the block's body", c.name, got, c.want.Kind)
		}
		if other := nodes[2].Events[1]; other.Kind != domain.EventToolResult || other.Text != "ch01.md" {
			t.Fatalf("%s: the other result became %#v", c.name, other)
		}
	}
}

// Newer Claude Code also logs each request it sends: a copy of the call and of its result, with
// no uuid, ahead of the records themselves. The copies are no part of the conversation, which
// holds the question and the answer once, and they leave what the list reports as it was.
func TestParseAskCopiedIntoARequestIsAskedAndAnsweredOnce(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "proj", "s.jsonl")
	session := askSession(`{"questions":` + askedQuestions + `}`)
	writeSession(t, src,
		session[0],
		`{"type":"api-request-blob","hash":"h1","sessionId":"s","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{"questions":`+askedQuestions+`}}]}}`,
		`{"type":"api-request-blob","hash":"h2","sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":`+strconv.Quote(answeredBody)+`}]}}`,
		session[1],
		askResult(answeredBody, false, answered(`,"answers":{"Which chapters should change?":"One"}`)))
	_, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if len(nodes) != 3 || len(nodes[1].Events) != 1 || len(nodes[2].Events) != 1 {
		t.Fatalf("nodes = %#v, want the prompt, one question and one answer", nodes)
	}
	if q := nodes[1].Events[0]; q.Kind != domain.EventQuestion {
		t.Fatalf("got %#v, want the question", q)
	}
	want := "Scope: One\nChecks: (no answer)\nOrder: (no answer)"
	if a := nodes[2].Events[0]; a.Kind != domain.EventAnswer || a.Text != want {
		t.Fatalf("got %#v, want the answer reading %q", a, want)
	}
	if got := scanOne(t, root, src).LastKind; got != domain.EventReasoning {
		t.Fatalf("LastKind = %q, want %q", got, domain.EventReasoning)
	}
}

// Once the answer is in, the model is at work on it, as it is after any tool result.
func TestScanSessionEndingInAnAnswerReportsTheModelAtWork(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "proj", "s.jsonl")
	writeSession(t, src, append(askSession(`{"questions":`+askedQuestions+`}`),
		askResult(answeredBody, false, answered(`,"answers":{"Which chapters should change?":"All"}`)))...)
	if got := scanOne(t, root, src).LastKind; got != domain.EventReasoning {
		t.Fatalf("LastKind = %q, want %q", got, domain.EventReasoning)
	}
}
