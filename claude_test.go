package claude

import (
	"context"
	"encoding/json"
	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/conversation"
	"github.com/agentcarto/core/domain"
	"github.com/agentcarto/core/plugin"
	"github.com/agentcarto/core/transaction"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlug(t *testing.T) {
	if got := ClaudeSlug("/home/u/.x"); got != "-home-u--x" {
		t.Fatal(got)
	}
}
func TestRelocatePlanAndApply(t *testing.T) {
	d := t.TempDir()
	p := &Plugin{id: "claude", o: Options{ProjectsDir: d}}
	old, new := "/a/x", "/b/y"
	dir := filepath.Join(d, ClaudeSlug(old))
	_ = os.MkdirAll(filepath.Join(dir, "s", "subagents"), 0700)
	src := filepath.Join(dir, "s.jsonl")
	_ = os.WriteFile(src, []byte(`{"cwd":"/a/x","uuid":"u"}`+"\n"), 0600)
	sub := filepath.Join(dir, "s", "subagents", "x.jsonl")
	_ = os.WriteFile(sub, []byte(`{"cwd":"/a/x"}`+"\n"), 0600)
	plan, e := p.PlanRelocate(context.Background(), old, new, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = transaction.Apply(context.Background(), plan); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(d, ClaudeSlug(new), "s.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	var o map[string]any
	_ = json.Unmarshal(b, &o)
	if o["cwd"] != new {
		t.Fatal(o)
	}
}
func TestForkDoesNotModifySource(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte("{\"uuid\":\"a\",\"parentUuid\":null}\n{\"uuid\":\"b\",\"parentUuid\":\"a\"}\n{\"uuid\":\"side\",\"parentUuid\":\"a\"}\n")
	_ = os.WriteFile(src, data, 0600)
	p := &Plugin{id: "claude", o: Options{ProjectsDir: d, Executable: "claude"}}
	plan, _, e := p.PlanFork(context.Background(), domain.Session{SourceRef: domain.SessionRef{Source: src}}, domain.ForkTarget{NodeID: "b"})
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Writes) != 1 {
		t.Fatal(plan)
	}
	now, _ := os.ReadFile(src)
	if string(now) != string(data) {
		t.Fatal("source changed")
	}
	if _, e = transaction.Apply(context.Background(), plan); e != nil {
		t.Fatal(e)
	}
}

func TestScanClaudeSubagentForkMetadata(t *testing.T) {
	d := t.TempDir()
	parentDir := filepath.Join(d, "proj")
	subDir := filepath.Join(parentDir, "parent", "subagents")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(parentDir, "parent.jsonl")
	if err := os.WriteFile(parentPath, []byte(`{"uuid":"p1","sessionId":"parent","cwd":"/work","timestamp":"2026-06-23T00:00:00Z","message":{"role":"user","content":"parent q"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	forkPath := filepath.Join(subDir, "agent-x.jsonl")
	if err := os.WriteFile(forkPath, []byte(`{"uuid":"f1","sessionId":"child","parentSessionId":"parent","parentLastUuid":"p1","cwd":"/work","timestamp":"2026-06-23T00:01:00Z","message":{"role":"user","content":"fork q"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{ProjectsDir: d}}
	res, err := p.Scan(context.Background(), plugin.ScanInput{})
	ss := res.Sessions
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range ss {
		if s.SourceRef.Source == forkPath && s.ParentSessionID == "parent" && s.ForkAt == "p1" {
			found = true
			if s.SessionID != "agent-x" {
				t.Fatalf("fork SessionID = %q, want filename-based agent-x", s.SessionID)
			}
		}
	}
	if !found {
		t.Fatalf("fork session missing: %#v", ss)
	}
}

// Sessions that are only /clear, or have no real utterances, are excluded from the list.
// Sessions with real utterances are kept.
func TestScanExcludesClearOnlyAndEmptySessions(t *testing.T) {
	d := t.TempDir()
	dir := filepath.Join(d, "proj")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	clearOnly := filepath.Join(dir, "clear-only.jsonl")
	if err := os.WriteFile(clearOnly, []byte(`{"uuid":"c1","cwd":"/work","timestamp":"2026-06-23T00:00:00Z","message":{"role":"user","content":"<command-message>clear</command-message>\n<command-name>/clear</command-name>"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// An empty session with only isMeta nodes (no display events).
	emptyMeta := filepath.Join(dir, "empty-meta.jsonl")
	if err := os.WriteFile(emptyMeta, []byte(`{"uuid":"m1","cwd":"/work","timestamp":"2026-06-23T00:00:00Z","isMeta":true,"message":{"role":"user","content":"expanded"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real.jsonl")
	if err := os.WriteFile(real, []byte(`{"uuid":"r1","cwd":"/work","timestamp":"2026-06-23T00:00:00Z","message":{"role":"user","content":"real question"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{ProjectsDir: d}}
	res, err := p.Scan(context.Background(), plugin.ScanInput{})
	ss := res.Sessions
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if s.SourceRef.Source == clearOnly {
			t.Errorf("clear-only session should be excluded: %#v", s)
		}
		if s.SourceRef.Source == emptyMeta {
			t.Errorf("empty meta-only session should be excluded: %#v", s)
		}
	}
	found := false
	for _, s := range ss {
		if s.SourceRef.Source == real {
			found = true
		}
	}
	if !found {
		t.Fatalf("real session missing: %#v", ss)
	}
}

// Fork files under subagents/ inherit the parent's id in their sessionId. Using that as the
// fork's SessionID would collide with the main session's key, causing cache overwrites, duplicate
// rows, and a display that flips on every reload. The fork id is taken from the file name and is
// guaranteed to differ from the parent's.
func TestScanClaudeSubagentForkSessionIDDoesNotCollideWithParent(t *testing.T) {
	d := t.TempDir()
	parentDir := filepath.Join(d, "proj")
	subDir := filepath.Join(parentDir, "sess", "subagents")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(parentDir, "sess.jsonl")
	if err := os.WriteFile(parentPath, []byte(`{"uuid":"p1","sessionId":"sess","cwd":"/work","timestamp":"2026-06-23T00:00:00Z","message":{"role":"user","content":"parent q"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The fork file's sessionId equals the parent's (Claude's actual subagent-fork behavior).
	forkPath := filepath.Join(subDir, "agent-abfbfb84.jsonl")
	if err := os.WriteFile(forkPath, []byte(`{"uuid":"f1","sessionId":"sess","parentSessionId":"sess","parentLastUuid":"p1","isSidechain":true,"cwd":"/work","timestamp":"2026-06-23T00:01:00Z","message":{"role":"user","content":"fork q"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{ProjectsDir: d}}
	res, err := p.Scan(context.Background(), plugin.ScanInput{})
	ss := res.Sessions
	if err != nil {
		t.Fatal(err)
	}
	var main, fork *domain.Session
	for i := range ss {
		switch ss[i].SourceRef.Source {
		case parentPath:
			main = &ss[i]
		case forkPath:
			fork = &ss[i]
		}
	}
	if main == nil || fork == nil {
		t.Fatalf("expected both main and fork sessions: %#v", ss)
	}
	if main.SessionID == fork.SessionID {
		t.Fatalf("fork SessionID %q collides with main %q", fork.SessionID, main.SessionID)
	}
	if fork.SessionID != "agent-abfbfb84" {
		t.Fatalf("fork SessionID = %q, want agent-abfbfb84", fork.SessionID)
	}
	if fork.ParentSessionID != "sess" {
		t.Fatalf("fork ParentSessionID = %q, want sess", fork.ParentSessionID)
	}
	// A subagent fork with a synthetic id can't be resumed on its own (the app won't offer resume).
	if !fork.Unresumable {
		t.Fatal("fork should be marked Unresumable")
	}
	if main.Unresumable {
		t.Fatal("main session should be resumable")
	}
}

func TestParseContentListStringBlock(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"u","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":["hello from string block",{"type":"text","text":"and object block"}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if len(nodes) != 1 || len(nodes[0].Events) != 2 {
		t.Fatalf("nodes=%#v", nodes)
	}
	if nodes[0].Events[0].Text != "hello from string block" {
		t.Fatalf("missing string block: %#v", nodes[0].Events)
	}
}

// Thinking whose plaintext was never logged still produces a reasoning event, as
// a redacted placeholder: the ciphertext (signature, or data for
// redacted_thinking) proves the model reasoned there. Only a block with neither
// text nor ciphertext is dropped.
func TestParseRedactsEncryptedThinkingBlock(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","usage":{"input_tokens":100,"cache_read_input_tokens":20,"cache_creation_input_tokens":5,"output_tokens":948},"content":[{"type":"thinking","thinking":"","signature":"` + strings.Repeat("x", 2048) + `"},{"type":"redacted_thinking","data":"ciphertext"},{"type":"thinking","thinking":""},{"type":"thinking","thinking":"real thought"},{"type":"text","text":"answer"}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, _, _, _, _, _, _, _ := parse(context.Background(), src)
	var reasoning []domain.Event
	for _, e := range ev {
		if e.Kind == domain.EventReasoning {
			reasoning = append(reasoning, e)
		}
	}
	if len(reasoning) != 3 {
		t.Fatalf("expected 2 redacted + 1 plaintext reasoning events, got %#v", reasoning)
	}
	for i, want := range []string{"thinking", "redacted_thinking"} {
		if reasoning[i].Text != "" || reasoning[i].RawType != want {
			t.Fatalf("block %d should be a redacted placeholder with RawType %q: %#v", i, want, reasoning[i])
		}
		// Like an advisor call: the identifier lives in ToolArg, the detail in one line.
		if reasoning[i].ToolArg != "[redacted]" {
			t.Fatalf("block %d ToolArg = %q", i, reasoning[i].ToolArg)
		}
		// Only the output side: the response's input tokens track the context length.
		if !strings.HasSuffix(reasoning[i].ToolDetail, "· 948 tok") {
			t.Fatalf("block %d ToolDetail = %q, want the response output tokens", i, reasoning[i].ToolDetail)
		}
	}
	// Ciphertext size comes from the block, not the message: 2048 B vs 10 B.
	if !strings.HasPrefix(reasoning[0].ToolDetail, "redacted thinking · 2.0 KB") {
		t.Fatalf("signature size not reported: %q", reasoning[0].ToolDetail)
	}
	if !strings.HasPrefix(reasoning[1].ToolDetail, "redacted thinking · 10 B") {
		t.Fatalf("redacted_thinking data size not reported: %q", reasoning[1].ToolDetail)
	}
	if reasoning[2].Text != "real thought" || reasoning[2].ToolArg != "" {
		t.Fatalf("plaintext thinking should stay as-is: %#v", reasoning[2])
	}
}

func TestHumanBytes(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, "0 B"}, {980, "980 B"}, {1023, "1023 B"}, {1024, "1.0 KB"}, {4301, "4.2 KB"}, {235256, "229.7 KB"}, {1 << 21, "2.0 MB"}} {
		if got := humanBytes(c.n); got != c.want {
			t.Fatalf("humanBytes(%d)=%q want %q", c.n, got, c.want)
		}
	}
}

// Each assistant record's model is stamped onto the events it produces, so the
// host can show per-turn models even when the model changes mid-session. The
// session-level model stays the first assistant's model.
func TestParsePerTurnModel(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a1","type":"assistant","timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"first"}]}}` + "\n" +
		`{"uuid":"a2","type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"second"}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, nodes, _, _, _, model, _, _ := parse(context.Background(), src)
	if model != "claude-sonnet-5" {
		t.Fatalf("session model = %q, want first assistant's model", model)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes=%#v", nodes)
	}
	want := []string{"claude-sonnet-5", "claude-opus-4-8"}
	for i, n := range nodes {
		if len(n.Events) == 0 {
			t.Fatalf("node %d has no events", i)
		}
		for _, e := range n.Events {
			if e.Model != want[i] {
				t.Fatalf("node %d event model = %q, want %q", i, e.Model, want[i])
			}
		}
	}
}

// A tool_result whose content has no text blocks (a screenshot, a tool
// reference) must show what is there instead of an empty result.
func TestParseToolResultNonTextPlaceholder(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","data":"iVBOR"}}]},{"type":"tool_result","tool_use_id":"t2","content":[{"type":"tool_reference","tool_name":"WebFetch"}]}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, _, _, _, _, _, _, _ := parse(context.Background(), src)
	var results []string
	for _, e := range ev {
		if e.Kind == domain.EventToolResult {
			results = append(results, e.Text)
		}
	}
	if len(results) != 2 || results[0] != "[image]" || results[1] != "[tool: WebFetch]" {
		t.Fatalf("results=%#v", results)
	}
}

// findKind returns the last event of the given kind, or nil.
func findKind(ev []domain.Event, k domain.EventKind) *domain.Event {
	var got *domain.Event
	for i := range ev {
		if ev[i].Kind == k {
			got = &ev[i]
		}
	}
	return got
}

// The advisor is invoked as a server-side tool (server_tool_use "advisor" +
// advisor_tool_result). It folds into a single EventToolCall — advisor is a
// tool — so the empty call block is dropped and the result is merged into the
// call. Exactly one tool call surfaces, named for the advising model.
func TestParseAdvisorServerToolBlocks(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","advisorModel":"claude-fable-5","message":{"role":"assistant","content":[{"type":"server_tool_use","id":"srv1","name":"advisor","input":{}},{"type":"advisor_tool_result","tool_use_id":"srv1","content":{"type":"advisor_redacted_result","encrypted_content":"` + strings.Repeat("x", 1440) + `"}}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, _, _, _, _, _, _, _ := parse(context.Background(), src)
	if r := findKind(ev, domain.EventToolResult); r != nil {
		t.Fatalf("advisor should fold into one call block, not a separate result, got %#v", r)
	}
	call := findKind(ev, domain.EventToolCall)
	if call == nil || call.ToolName != "ADVISOR" || call.ToolArg != "[claude-fable-5]" {
		t.Fatalf("expected an advisor tool call labeled with the model, got %#v", call)
	}
	// No usage iteration here, so only the ciphertext size is reported.
	if call.ToolDetail != "redacted advice · 1.4 KB" {
		t.Fatalf("expected a redacted-advice detail, got %q", call.ToolDetail)
	}
}

// A successful advisor call records the tokens the advisor generated, from the
// message usage's advisor_message iteration, since the advice text is encrypted.
// Its ciphertext size precedes the tokens, matching how a redacted thinking block
// reports its signature size. The iteration's input_tokens is not shown.
func TestParseAdvisorResultTokens(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","advisorModel":"claude-fable-5","message":{"role":"assistant","usage":{"iterations":[{"type":"message","input_tokens":10},{"type":"advisor_message","input_tokens":37923,"output_tokens":1046}]},"content":[{"type":"advisor_tool_result","tool_use_id":"srv1","content":{"type":"advisor_redacted_result","encrypted_content":"` + strings.Repeat("x", 1440) + `"}}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, _, _, _, _, _, _, _ := parse(context.Background(), src)
	call := findKind(ev, domain.EventToolCall)
	if call == nil || call.ToolArg != "[claude-fable-5]" || call.ToolDetail != "redacted advice · 1.4 KB · 1046 tok" {
		t.Fatalf("expected an advisor call with size and token usage, got %#v", call)
	}
}

// A failed advisor call carries content.type advisor_tool_result_error. The
// record still sets advisorModel, so the error must be detected first — otherwise
// it would render as a redacted success and hide the failure. The call is
// labeled with the model too; the error code lives in the detail.
func TestParseAdvisorToolResultError(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","advisorModel":"claude-fable-5","message":{"role":"assistant","content":[{"type":"server_tool_use","id":"srv1","name":"advisor","input":{}},{"type":"advisor_tool_result","tool_use_id":"srv1","content":{"type":"advisor_tool_result_error","error_code":"unavailable"}}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, _, _, _, _, _, _, _ := parse(context.Background(), src)
	call := findKind(ev, domain.EventToolCall)
	if call == nil || call.ToolName != "ADVISOR" || call.ToolArg != "[claude-fable-5]" {
		t.Fatalf("expected an advisor error call labeled with the model, got %#v", call)
	}
	if call.ToolDetail != "advisor call failed (unavailable)" {
		t.Fatalf("expected a failure detail, got %q", call.ToolDetail)
	}
}

// away_summary system records carry the recap of what the agent did while the
// user was away; they surface as assistant text like Claude Code shows them,
// marked as written between turns so the host does not count them in the turn's time.
func TestParseSystemAwaySummary(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","parentUuid":"","timestamp":"2026-01-01T00:00:00Z","type":"system","subtype":"away_summary","content":"built the slides and verified the output","isMeta":false}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if len(ev) != 1 || ev[0].Kind != domain.EventAssistant || ev[0].Text != "built the slides and verified the output" || !ev[0].BetweenTurns {
		t.Fatalf("events=%#v", ev)
	}
	if len(nodes) != 1 || len(nodes[0].Events) != 1 || !nodes[0].Events[0].BetweenTurns {
		t.Fatalf("nodes=%#v", nodes)
	}
}

// Claude Code compacts an idle session on its own before the prompt cache expires. That compaction
// is not work of the turn it is filed under: its summary and the files it re-attaches are marked as
// written between turns, up to the next user or assistant record. A compaction the user waited
// for — a manual /compact, or an automatic one in the middle of a turn — is not marked. The idle
// case follows the order real logs write: a finished turn, often an away_summary, the boundary,
// the summary, re-attached files, a notice, and later the next prompt with its own @-file.
func TestParseMarksOnlyIdleCompactionAsBetweenTurns(t *testing.T) {
	const (
		prompt  = `{"uuid":"u1","type":"user","timestamp":"2026-11-03T09:43:00Z","message":{"role":"user","content":"question"}}`
		answer  = `{"uuid":"a1","parentUuid":"u1","type":"assistant","timestamp":"2026-11-03T09:45:00Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`
		toolUse = `{"uuid":"a1","parentUuid":"u1","type":"assistant","timestamp":"2026-11-03T09:45:00Z","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/w/x.go"}}]}}`
		away    = `{"uuid":"w1","parentUuid":"a1","type":"system","subtype":"away_summary","timestamp":"2026-11-03T10:13:00Z","content":"answered the question","isMeta":false}`
		summary = `{"uuid":"s1","parentUuid":"b1","type":"user","timestamp":"2026-11-03T10:40:19Z","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued from a previous conversation."}}`
		reread  = `{"uuid":"f1","parentUuid":"s1","type":"attachment","timestamp":"2026-11-03T10:40:20Z","attachment":{"type":"file","filename":"/w/notes.md","content":{"type":"text","file":{"filePath":"/w/notes.md","content":"notes"}}}}`
		notice  = `{"uuid":"i1","parentUuid":"f1","type":"system","subtype":"informational","timestamp":"2026-11-03T10:40:20Z","content":"Compacted while idle","isMeta":false}`
		next    = `{"uuid":"u2","parentUuid":"i1","type":"user","timestamp":"2026-11-03T11:26:29Z","message":{"role":"user","content":"look at @other.md"}}`
		mention = `{"uuid":"f2","parentUuid":"u2","type":"attachment","timestamp":"2026-11-03T11:26:29Z","attachment":{"type":"file","filename":"/w/other.md","content":{"type":"text","file":{"filePath":"/w/other.md","content":"other"}}}}`
		// A user record with nothing to show (an injected reminder) still ends the compaction.
		reminder = `{"uuid":"m1","parentUuid":"i1","type":"user","isMeta":true,"timestamp":"2026-11-03T11:26:29Z","message":{"role":"user","content":"<system-reminder>x</system-reminder>"}}`
	)
	boundary := func(trigger string) string {
		return `{"uuid":"b1","parentUuid":null,"logicalParentUuid":"a1","timestamp":"2026-11-03T10:40:19Z","type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"` + trigger + `"}}`
	}
	marked := func(t *testing.T, lines ...string) map[string]bool {
		t.Helper()
		src := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(src, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		_, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
		out := map[string]bool{}
		for _, n := range nodes {
			for _, e := range n.Events {
				out[n.ID] = out[n.ID] || e.BetweenTurns
			}
		}
		return out
	}

	idle := marked(t, prompt, answer, away, boundary("auto"), summary, reread, notice, next, mention)
	if !idle["s1"] || !idle["f1"] || !idle["w1"] {
		t.Fatalf("an idle compaction's summary and re-attached file, and the away_summary, should be marked: %v", idle)
	}
	if idle["u1"] || idle["a1"] || idle["u2"] || idle["f2"] {
		t.Fatalf("only what was written between turns should be marked: %v", idle)
	}
	if quiet := marked(t, prompt, answer, boundary("auto"), summary, notice, reminder, mention); quiet["f2"] {
		t.Fatalf("a user record ends the compaction even with nothing to show: %v", quiet)
	}
	if manual := marked(t, prompt, answer, boundary("manual"), summary, reread); manual["s1"] || manual["f1"] {
		t.Fatalf("a manual /compact is not between turns: %v", manual)
	}
	if midTurn := marked(t, prompt, toolUse, boundary("auto"), summary, reread); midTurn["s1"] || midTurn["f1"] {
		t.Fatalf("an automatic compaction in the middle of a turn is not between turns: %v", midTurn)
	}
}

// Newer versions log slash commands only as system/local_command records; the
// command invocation must surface as a user event while empty output wrappers
// stay silent.
func TestParseSystemLocalCommand(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","type":"system","subtype":"local_command","content":"<command-name>/fork</command-name><command-args>f2</command-args>"}` + "\n" +
		`{"uuid":"b","parentUuid":"a","timestamp":"2026-01-01T00:00:01Z","type":"system","subtype":"local_command","content":"<local-command-stdout></local-command-stdout>","level":"info"}` + "\n" +
		`{"uuid":"c","parentUuid":"b","timestamp":"2026-01-01T00:00:02Z","type":"system","subtype":"local_command","content":"<local-command-stdout>forked to f2</local-command-stdout>","level":"info"}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, _, _, _, _, _, _, _ := parse(context.Background(), src)
	var got []string
	for _, e := range ev {
		got = append(got, string(e.Kind)+":"+e.Text)
	}
	want := []string{
		"user:<command-name>/fork</command-name><command-args>f2</command-args>",
		"user:<local-command-stdout>forked to f2</local-command-stdout>",
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events=%#v want %#v", got, want)
	}
}

// User @-mentions (and slash-command file arguments) inject file/directory
// content as top-level attachment records; they surface as EventAttachment
// (path in ToolArg, content in Text) while the many system-noise attachment
// subtypes (reminders, listings, …) stay invisible with their nodes intact.
func TestParseAttachmentFileAndDirectory(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","type":"attachment","attachment":{"type":"file","filename":"/w/notes.md","content":{"type":"text","file":{"filePath":"/w/notes.md","content":"# Notes\nbody"}}}}` + "\n" +
		`{"uuid":"b","parentUuid":"a","timestamp":"2026-01-01T00:00:01Z","type":"attachment","attachment":{"type":"directory","path":"/w/docs","content":"a.md\nb.md"}}` + "\n" +
		`{"uuid":"c","parentUuid":"b","timestamp":"2026-01-01T00:00:02Z","type":"attachment","attachment":{"type":"task_reminder","content":"noise"}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if len(ev) != 2 || ev[0].Kind != domain.EventAttachment || ev[1].Kind != domain.EventAttachment {
		t.Fatalf("events=%#v", ev)
	}
	if ev[0].ToolArg != "/w/notes.md" || ev[0].Text != "# Notes\nbody" {
		t.Fatalf("file attachment=%#v", ev[0])
	}
	if ev[1].ToolArg != "/w/docs" || ev[1].Text != "a.md\nb.md" {
		t.Fatalf("directory attachment=%#v", ev[1])
	}
	if len(nodes) != 3 || len(nodes[2].Events) != 0 {
		t.Fatalf("nodes=%#v", nodes)
	}
}

func TestParseEndTurnNotAddedToConversationNode(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if len(ev) != 2 || ev[1].Kind != domain.EventTurnComplete {
		t.Fatalf("events=%#v", ev)
	}
	if len(nodes) != 1 || len(nodes[0].Events) != 1 || nodes[0].Events[0].Kind == domain.EventTurnComplete {
		t.Fatalf("nodes=%#v", nodes)
	}
}

func TestLoadConversationDropsDuplicateManualCompactLeaf(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(
		`{"uuid":"u1","parentUuid":null,"timestamp":"2026-08-25T00:00:00Z","type":"user","message":{"role":"user","content":"before compact"}}` + "\n" +
			`{"uuid":"a1","parentUuid":"u1","timestamp":"2026-08-25T00:00:01Z","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"before answer"}]}}` + "\n" +
			`{"uuid":"bare","parentUuid":"a1","timestamp":"2026-08-25T00:00:02Z","type":"user","message":{"role":"user","content":"/compact"}}` + "\n" +
			`{"uuid":"boundary","parentUuid":null,"logicalParentUuid":"a1","timestamp":"2026-08-25T00:00:03Z","type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual"}}` + "\n" +
			`{"uuid":"summary","parentUuid":"boundary","timestamp":"2026-08-25T00:00:04Z","type":"user","isCompactSummary":true,"message":{"role":"user","content":"summary body"}}` + "\n" +
			`{"uuid":"meta","parentUuid":"summary","timestamp":"2026-08-25T00:00:05Z","type":"user","isMeta":true,"message":{"role":"user","content":"command caveat"}}` + "\n" +
			`{"uuid":"xml","parentUuid":"meta","timestamp":"2026-08-25T00:00:06Z","type":"user","message":{"role":"user","content":"<command-name>/compact</command-name>"}}` + "\n" +
			`{"uuid":"u2","parentUuid":"xml","timestamp":"2026-08-25T00:00:07Z","type":"user","message":{"role":"user","content":"after compact"}}` + "\n" +
			`{"uuid":"a2","parentUuid":"u2","timestamp":"2026-08-25T00:00:08Z","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"after answer"}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	c, err := p.LoadConversation(context.Background(), domain.SessionRef{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Nodes["bare"]; ok {
		t.Fatal("duplicate bare /compact leaf should not be a conversation node")
	}
	if got := c.Children["a1"]; len(got) != 1 || got[0] != "boundary" {
		t.Fatalf("compact parent children = %v, want only boundary", got)
	}
	wantPath := []string{"u1", "a1", "boundary", "summary", "meta", "xml", "u2", "a2"}
	if got := c.ActivePath(); strings.Join(got, ",") != strings.Join(wantPath, ",") {
		t.Fatalf("active path = %v, want %v", got, wantPath)
	}
	if got := c.Nodes["summary"].Events; len(got) != 1 || got[0].RawType != domain.RawCompactSummary {
		t.Fatalf("compact summary event = %#v", got)
	}
	if got := c.Nodes["xml"].Events; len(got) != 1 || got[0].Command != "/compact" {
		t.Fatalf("XML compact command = %#v", got)
	}
}

func TestDropDuplicateManualCompactLeavesKeepsNonMatches(t *testing.T) {
	bare := func(id, parent, text string) domain.ConvNode {
		return domain.ConvNode{ID: id, Parent: parent, Events: []domain.Event{{Kind: domain.EventUser, Text: text}}}
	}
	child := domain.ConvNode{ID: "child", Parent: "candidate", Events: []domain.Event{{Kind: domain.EventAssistant, Text: "reply"}}}
	withExtraContent := bare("candidate", "parent", "/compact")
	withExtraContent.Events = append(withExtraContent.Events, domain.Event{Kind: domain.EventAssistant, Text: "keep me"})
	tests := []struct {
		name       string
		nodes      []domain.ConvNode
		ordinals   map[string]int
		boundaries []compactBoundary
	}{
		{
			name:       "compact mention",
			nodes:      []domain.ConvNode{bare("candidate", "parent", "please run /compact")},
			ordinals:   map[string]int{"candidate": 1},
			boundaries: []compactBoundary{{parent: "parent", ordinal: 2}},
		},
		{
			name:       "compact with arguments",
			nodes:      []domain.ConvNode{bare("candidate", "parent", "/compact keep these notes")},
			ordinals:   map[string]int{"candidate": 1},
			boundaries: []compactBoundary{{parent: "parent", ordinal: 2}},
		},
		{
			name:       "candidate has child",
			nodes:      []domain.ConvNode{bare("candidate", "parent", "/compact"), child},
			ordinals:   map[string]int{"candidate": 1, "child": 2},
			boundaries: []compactBoundary{{parent: "parent", ordinal: 3}},
		},
		{
			name:       "candidate has other content",
			nodes:      []domain.ConvNode{withExtraContent},
			ordinals:   map[string]int{"candidate": 1},
			boundaries: []compactBoundary{{parent: "parent", ordinal: 2}},
		},
		{
			name:       "different parent",
			nodes:      []domain.ConvNode{bare("candidate", "other-parent", "/compact")},
			ordinals:   map[string]int{"candidate": 1},
			boundaries: []compactBoundary{{parent: "parent", ordinal: 2}},
		},
		{
			name:     "no boundary",
			nodes:    []domain.ConvNode{bare("candidate", "parent", "/compact")},
			ordinals: map[string]int{"candidate": 1},
		},
		{
			name:       "candidate follows boundary",
			nodes:      []domain.ConvNode{bare("candidate", "parent", "/compact")},
			ordinals:   map[string]int{"candidate": 3},
			boundaries: []compactBoundary{{parent: "parent", ordinal: 2}},
		},
		{
			name:       "multiple candidates",
			nodes:      []domain.ConvNode{bare("candidate", "parent", "/compact"), bare("other", "parent", "/compact")},
			ordinals:   map[string]int{"candidate": 1, "other": 2},
			boundaries: []compactBoundary{{parent: "parent", ordinal: 3}},
		},
		{
			name:       "candidate matches multiple boundaries",
			nodes:      []domain.ConvNode{bare("candidate", "parent", "/compact")},
			ordinals:   map[string]int{"candidate": 1},
			boundaries: []compactBoundary{{parent: "parent", ordinal: 2}, {parent: "parent", ordinal: 3}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dropDuplicateManualCompactLeaves(tt.nodes, tt.ordinals, tt.boundaries)
			found := false
			for _, n := range got {
				if n.ID == "candidate" {
					found = true
				}
			}
			if !found {
				t.Fatalf("candidate was removed from %#v", got)
			}
		})
	}
}

func TestParseKeepsBareCompactBesideAutomaticBoundary(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(
		`{"uuid":"parent","timestamp":"2026-08-25T00:00:00Z","type":"assistant","message":{"role":"assistant","content":"answer"}}` + "\n" +
			`{"uuid":"candidate","parentUuid":"parent","timestamp":"2026-08-25T00:00:01Z","type":"user","message":{"role":"user","content":"/compact"}}` + "\n" +
			`{"uuid":"boundary","parentUuid":null,"logicalParentUuid":"parent","timestamp":"2026-08-25T00:00:02Z","type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto"}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	for _, n := range nodes {
		if n.ID == "candidate" {
			return
		}
	}
	t.Fatalf("automatic compact changed non-matching nodes: %#v", nodes)
}

func TestLoadConversationWithoutUUIDFallsBackToLinear(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":[{"type":"text","text":"Q"}]}}` + "\n" +
		`{"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"A"}],"stop_reason":"end_turn"}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	c, err := p.LoadConversation(context.Background(), domain.SessionRef{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, id := range c.ActivePath() {
		for _, e := range c.Nodes[id].Events {
			texts = append(texts, e.Text)
		}
	}
	if len(texts) != 2 || texts[0] != "Q" || texts[1] != "A" {
		t.Fatalf("texts=%v nodes=%#v", texts, c.Nodes)
	}
}

// Regression for the OOM where opening a session detail made the plugin allocate
// memory without bound. A transcript whose parent links form a cycle (X<->Y) plus
// a queued message drives LoadConversation -> attachQueued -> ActivePath, which
// used to walk the cyclic chain forever, ramping RSS until the process was killed.
func TestLoadConversationCyclicParentTerminates(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(
		`{"uuid":"X","parentUuid":"Y","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":[{"type":"text","text":"x"}]}}` + "\n" +
			`{"uuid":"Y","parentUuid":"X","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"y"}]}}` + "\n" +
			`{"uuid":"L","parentUuid":"X","timestamp":"2026-01-01T00:00:02Z","message":{"role":"user","content":[{"type":"text","text":"l"}]}}` + "\n" +
			`{"type":"queue-operation","operation":"enqueue","content":"queued msg","timestamp":"2026-01-01T00:00:03Z"}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	done := make(chan struct{})
	go func() {
		_, _ = p.LoadConversation(context.Background(), domain.SessionRef{Source: src})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("LoadConversation did not terminate on a cyclic transcript")
	}
}

// A user can queue a "! command" while one is running; Claude later writes it as
// a real <bash-input> record. The queued copy's text is bare ("git ...") while the
// executed record wraps it ("<bash-input>git ...</bash-input>"), so the old raw-text
// dedup missed it and the queued copy lingered as a phantom ▶ on the wrong turn.
// It must be deduped against the normalized command; an unrelated queued message
// must still survive.
func TestAttachQueuedDedupsExecutedCommand(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(
		`{"uuid":"u1","parentUuid":null,"timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","content":[{"type":"text","text":"do it"}]}}` + "\n" +
			`{"uuid":"a1","parentUuid":"u1","timestamp":"2026-01-01T00:00:01Z","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}` + "\n" +
			`{"uuid":"b1","parentUuid":"a1","timestamp":"2026-01-01T00:00:03Z","type":"user","message":{"role":"user","content":"<bash-input>git push origin v0.5.0</bash-input>"}}` + "\n" +
			`{"type":"queue-operation","operation":"enqueue","content":"git push origin v0.5.0","timestamp":"2026-01-01T00:00:02Z"}` + "\n" +
			`{"type":"queue-operation","operation":"enqueue","content":"an unrelated queued note","timestamp":"2026-01-01T00:00:02Z"}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	c, err := p.LoadConversation(context.Background(), domain.SessionRef{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	var queued []string
	for _, id := range c.ActivePath() {
		for _, e := range c.Nodes[id].Events {
			if e.Kind == domain.EventQueued {
				queued = append(queued, e.Text)
			}
		}
	}
	// The executed "! git push origin v0.5.0" must be deduped; only the unrelated
	// note (which never ran) survives as a queued event.
	if len(queued) != 1 || queued[0] != "an unrelated queued note" {
		t.Fatalf("expected only the unrelated queued message to survive, got %#v", queued)
	}
}

// A "!" shell command is a genuine user action, judged like any user message:
// it opens its own turn (Command boundary), separate from the preceding prompt.
func TestBashCommandOpensItsOwnTurn(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(
		`{"uuid":"u1","parentUuid":null,"timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","content":[{"type":"text","text":"do it"}]}}` + "\n" +
			`{"uuid":"a1","parentUuid":"u1","timestamp":"2026-01-01T00:00:01Z","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}` + "\n" +
			`{"uuid":"b1","parentUuid":"a1","timestamp":"2026-01-01T00:00:02Z","type":"user","message":{"role":"user","content":"<bash-input>git status</bash-input>"}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	c, err := p.LoadConversation(context.Background(), domain.SessionRef{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	turns := conversation.TurnsOfPath(*c, conversation.DeepestPath(*c, c.Roots[0]))
	if len(turns) != 2 {
		t.Fatalf("a ! command must open its own turn: got %d turns, want 2", len(turns))
	}
	if hl := conversation.TurnHeadline(*c, turns[1]); hl != "! git status" {
		t.Fatalf("second turn headline = %q, want the shell command", hl)
	}
}

// A session with a /model run two hours after its first turn, written as newer versions log it
// (system/local_command records) or as older ones did (a user record and its output).
func localCommandSession(form string) []string {
	command := map[string][]string{
		"local_command records": {
			`{"uuid":"c1","parentUuid":"a1","timestamp":"2026-01-01T02:00:00Z","type":"system","subtype":"local_command","content":"<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args></command-args>"}`,
			`{"uuid":"c2","parentUuid":"c1","timestamp":"2026-01-01T02:00:00Z","type":"system","subtype":"local_command","content":"<local-command-stdout>Set model to Opus</local-command-stdout>","commandRun":{"command":"model","args":""}}`,
		},
		"user records": {
			`{"uuid":"c1","parentUuid":"a1","timestamp":"2026-01-01T02:00:00Z","type":"user","message":{"role":"user","content":"<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args></command-args>"}}`,
			`{"uuid":"c2","parentUuid":"c1","timestamp":"2026-01-01T02:00:00Z","type":"user","message":{"role":"user","content":"<local-command-stdout>Set model to Opus</local-command-stdout>"}}`,
		},
	}[form]
	return append([]string{
		`{"uuid":"u1","parentUuid":null,"timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","content":"first"}}`,
		`{"uuid":"a1","parentUuid":"u1","timestamp":"2026-01-01T00:00:05Z","type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
	}, command...)
}

// A command Claude Code runs itself (/model, /fork, …) is never answered by the model: it opens
// no turn, and since it was run between turns it takes no part in the time of the turn it joins.
func TestLocalCommandOpensNoTurn(t *testing.T) {
	for _, form := range []string{"local_command records", "user records"} {
		t.Run(form, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "s.jsonl")
			writeSession(t, src, append(localCommandSession(form),
				`{"uuid":"u2","parentUuid":"c2","timestamp":"2026-01-01T02:00:10Z","type":"user","message":{"role":"user","content":"second"}}`)...)
			c, err := (&Plugin{}).LoadConversation(context.Background(), domain.SessionRef{Source: src})
			if err != nil {
				t.Fatal(err)
			}
			turns := conversation.TurnsOfPath(*c, c.ActivePath())
			if len(turns) != 2 || conversation.TurnHeadline(*c, turns[1]) != "second" {
				t.Fatalf("turns = %v, want the two prompts' turns only", turns)
			}
			for _, id := range []string{"c1", "c2"} {
				for _, e := range c.Nodes[id].Events {
					if e.Command != "" || !e.BetweenTurns {
						t.Fatalf("%s: %s %q Command=%q BetweenTurns=%v, want a command run between turns", id, e.Kind, e.Text, e.Command, e.BetweenTurns)
					}
				}
			}
		})
	}
}

// Nor does a command run last make the session look as if a prompt had just been sent.
func TestScanLocalCommandAtTheEndLeavesTheTurnFinished(t *testing.T) {
	root := t.TempDir()
	finished := filepath.Join(root, "proj", "finished.jsonl")
	writeSession(t, finished, localCommandSession("user records")[:2]...)
	for _, form := range []string{"local_command records", "user records"} {
		src := filepath.Join(root, "proj", strings.ReplaceAll(form, " ", "-")+".jsonl")
		writeSession(t, src, localCommandSession(form)...)
		if got, want := scanOne(t, root, src).LastKind, scanOne(t, root, finished).LastKind; got != want {
			t.Fatalf("%s: LastKind = %q, want %q as without the command", form, got, want)
		}
	}
}

// A command the model answers, such as a skill expanded into a prompt, still opens its own turn.
func TestPromptCommandOpensItsTurn(t *testing.T) {
	src := filepath.Join(t.TempDir(), "s.jsonl")
	writeSession(t, src,
		`{"uuid":"u1","parentUuid":null,"timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","content":"first"}}`,
		`{"uuid":"a1","parentUuid":"u1","timestamp":"2026-01-01T00:00:05Z","type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
		`{"uuid":"k1","parentUuid":"a1","timestamp":"2026-01-01T00:01:00Z","type":"user","message":{"role":"user","content":"<command-name>/verify</command-name>\n<command-message>verify</command-message>\n<command-args></command-args>"}}`,
		`{"uuid":"k2","parentUuid":"k1","timestamp":"2026-01-01T00:01:00Z","type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Verify the change."}]}}`,
		`{"uuid":"k3","parentUuid":"k2","timestamp":"2026-01-01T00:01:05Z","type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"verified"}]}}`)
	c, err := (&Plugin{}).LoadConversation(context.Background(), domain.SessionRef{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	turns := conversation.TurnsOfPath(*c, c.ActivePath())
	if len(turns) != 2 || conversation.TurnHeadline(*c, turns[1]) != "/verify" {
		t.Fatalf("turns = %v, want the prompt's turn and the /verify turn", turns)
	}
}

func TestLoadConversationNativeForkPrependsParent(t *testing.T) {
	d := t.TempDir()
	proj := filepath.Join(d, "proj")
	sub := filepath.Join(proj, "parent", "subagents")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(proj, "parent.jsonl")
	_ = os.WriteFile(parentPath, []byte(
		`{"uuid":"p1","parentUuid":null,"timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","content":[{"type":"text","text":"q1"}]}}`+"\n"+
			`{"uuid":"p2","parentUuid":"p1","timestamp":"2026-01-01T00:00:01Z","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a1"}]}}`+"\n"), 0600)
	forkPath := filepath.Join(sub, "agent-x.jsonl")
	_ = os.WriteFile(forkPath, []byte(
		`{"uuid":"f1","parentUuid":"p1","parentSessionId":"parent","parentLastUuid":"p1","timestamp":"2026-01-01T00:01:00Z","type":"user","message":{"role":"user","content":[{"type":"text","text":"q2"}]}}`+"\n"), 0600)
	p := &Plugin{}
	c, err := p.LoadConversation(context.Background(), domain.SessionRef{Source: forkPath})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, id := range c.ActivePath() {
		for _, e := range c.Nodes[id].Events {
			texts = append(texts, e.Text)
		}
	}
	// The pre-fork parent message q1 and the fork's continuation q2 are both on the active branch.
	if len(texts) != 2 || texts[0] != "q1" || texts[1] != "q2" {
		t.Fatalf("texts=%v nodes=%#v", texts, c.Nodes)
	}
}

func TestIsEmptyFork(t *testing.T) {
	// Full-copy fork of parent p1→p2 cut at p2 (not continued): p2 is a leaf with no child = empty.
	truncated := []domain.ConvNode{{ID: "p1"}, {ID: "p2", Parent: "p1"}}
	if !isEmptyFork(truncated, "p2") {
		t.Fatal("truncated full-copy fork should be empty")
	}
	// Continued after forking at p2 (f1 directly under p2): not empty.
	continued := []domain.ConvNode{{ID: "p1"}, {ID: "p2", Parent: "p1"}, {ID: "f1", Parent: "p2"}}
	if isEmptyFork(continued, "p2") {
		t.Fatal("continued fork should not be empty")
	}
	// Native fork (diff only; forkAt not in its own nodes): not empty.
	native := []domain.ConvNode{{ID: "f1", Parent: "p2"}}
	if isEmptyFork(native, "p2") {
		t.Fatal("native diff fork should not be empty")
	}
	// A normal (non-fork) session: not empty.
	if isEmptyFork(truncated, "") {
		t.Fatal("non-fork should not be empty")
	}
}

// noticeText is the body of the notice /fork leaves in the new session, cut down from a real
// one; up to is the time of the last record it copied from the parent.
func noticeText(upTo string) string {
	return `<task-notification>\n<fork-source>\nThis session began as a fork (copy) of another session that is still running: a session whose self-reported name is 'work'. The conversation up to ` + upTo + ` is shared history with it; the two sessions have since diverged.\n</fork-source>\n</task-notification>`
}

// Lines of a session made by Claude Code's /fork, cut down from a real one: the notice enqueued,
// the parent's history copied with its uuids, then the notice delivered under the last copied
// record.
var slashForkChild = []string{
	`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-01T00:01:01Z","content":"` + noticeText("2026-01-01T00:00:03Z") + `"}`,
	`{"uuid":"u1","parentUuid":null,"type":"user","cwd":"/work","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"test"}}`,
	`{"uuid":"a1","parentUuid":"u1","type":"assistant","timestamp":"2026-01-01T00:00:03Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
	`{"uuid":"n1","parentUuid":"a1","type":"user","timestamp":"2026-01-01T00:01:01Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"` + noticeText("2026-01-01T00:00:03Z") + `"}}`,
}

// A session made by /fork given a prompt: the prompt runs as the fork's first turn, and the notice,
// queued meanwhile, is delivered only after that turn.
var slashForkWithPrompt = []string{
	`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-01T00:01:02Z","content":"` + noticeText("2026-01-01T00:00:03Z") + `"}`,
	`{"uuid":"u1","parentUuid":null,"type":"user","cwd":"/work","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"test"}}`,
	`{"uuid":"a1","parentUuid":"u1","type":"assistant","timestamp":"2026-01-01T00:00:03Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
	`{"uuid":"f1","parentUuid":"a1","type":"user","timestamp":"2026-01-01T00:01:01Z","message":{"role":"user","content":"look into it"}}`,
	`{"uuid":"g1","parentUuid":"f1","type":"assistant","timestamp":"2026-01-01T00:01:05Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"looked"}]}}`,
	`{"uuid":"n1","parentUuid":"g1","type":"user","timestamp":"2026-01-01T00:01:06Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"` + noticeText("2026-01-01T00:00:03Z") + `"}}`,
}

// The first prompt sent to the fork, written under the notice.
var slashForkContinuation = []string{
	`{"uuid":"u2","parentUuid":"n1","type":"user","timestamp":"2026-01-01T00:05:00Z","message":{"role":"user","content":"carry on"}}`,
	`{"uuid":"a2","parentUuid":"u2","type":"assistant","timestamp":"2026-01-01T00:05:03Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`,
}

func writeSession(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func scanOne(t *testing.T, root, path string) domain.Session {
	t.Helper()
	res, err := (&Plugin{id: "claude", o: Options{ProjectsDir: root}}).Scan(context.Background(), plugin.ScanInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Sessions {
		if s.SourceRef.Source == path {
			return s
		}
	}
	t.Fatalf("%s not scanned: %#v", path, res.Sessions)
	return domain.Session{}
}

// A /fork session nobody has prompted yet is the parent's history and nothing more: its fork
// point is the last copied record, and like an unprompted PlanFork copy it is an empty fork.
func TestScanSlashForkNotYetPromptedIsEmptyFork(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "proj", "child.jsonl")
	writeSession(t, child, slashForkChild...)
	s := scanOne(t, root, child)
	if s.ForkAt != "a1" || !s.EmptyFork {
		t.Fatalf("ForkAt=%q EmptyFork=%v, want a1 and an empty fork", s.ForkAt, s.EmptyFork)
	}
	if s.Title != "test" {
		t.Fatalf("title = %q, want the first copied prompt", s.Title)
	}
}

func TestScanSlashForkPromptedIsNotEmptyFork(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "proj", "child.jsonl")
	writeSession(t, child, append(append([]string{}, slashForkChild...), slashForkContinuation...)...)
	s := scanOne(t, root, child)
	if s.ForkAt != "a1" || s.EmptyFork {
		t.Fatalf("ForkAt=%q EmptyFork=%v, want a1 and a continued fork", s.ForkAt, s.EmptyFork)
	}
}

// A fork given a prompt forks where the copy ends, not where its notice arrives after its first
// turn, and that turn alone makes it more than the parent's history.
func TestScanSlashForkGivenAPromptForksWhereTheCopyEnds(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "proj", "child.jsonl")
	writeSession(t, child, slashForkWithPrompt...)
	s := scanOne(t, root, child)
	if s.ForkAt != "a1" || s.EmptyFork {
		t.Fatalf("ForkAt=%q EmptyFork=%v, want a1 and a continued fork", s.ForkAt, s.EmptyFork)
	}
}

// Until the prompt's turn is over the notice is only queued, and the fork point is already known.
func TestScanSlashForkInItsFirstTurnForksWhereTheCopyEnds(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "proj", "child.jsonl")
	writeSession(t, child, slashForkWithPrompt[:len(slashForkWithPrompt)-1]...)
	s := scanOne(t, root, child)
	if s.ForkAt != "a1" || s.EmptyFork {
		t.Fatalf("ForkAt=%q EmptyFork=%v, want a1 and a continued fork", s.ForkAt, s.EmptyFork)
	}
}

// A fork of a session that was itself made by /fork copies that session's notice along with its
// history; the fork point is where its own copy ends, given by its own notice.
func TestScanSlashForkOfASlashForkForksWhereItsOwnCopyEnds(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "proj", "child.jsonl")
	writeSession(t, child,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-01T00:03:00Z","content":"`+noticeText("2026-01-01T00:02:03Z")+`"}`,
		slashForkChild[1], slashForkChild[2], slashForkChild[3],
		`{"uuid":"u2","parentUuid":"n1","type":"user","timestamp":"2026-01-01T00:02:00Z","message":{"role":"user","content":"carry on"}}`,
		`{"uuid":"a2","parentUuid":"u2","type":"assistant","timestamp":"2026-01-01T00:02:03Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`,
		`{"uuid":"n2","parentUuid":"a2","type":"user","timestamp":"2026-01-01T00:03:00Z","message":{"role":"user","content":"`+noticeText("2026-01-01T00:02:03Z")+`"}}`)
	s := scanOne(t, root, child)
	if s.ForkAt != "a2" || !s.EmptyFork {
		t.Fatalf("ForkAt=%q EmptyFork=%v, want a2 and an empty fork", s.ForkAt, s.EmptyFork)
	}
}

// Without the end of the shared history the fork point is unknown, and the session is shown as
// one of its own rather than tied to its parent at a guessed point.
func TestScanSlashForkNoticeWithoutSharedHistoryEndHasNoLineage(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	child := filepath.Join(proj, forkChildID+".jsonl")
	notice := strings.Replace(noticeText(""), "up to  is", "up to now is", 1)
	writeSession(t, child,
		slashForkChild[1], slashForkChild[2],
		`{"uuid":"n1","parentUuid":"a1","type":"user","timestamp":"2026-01-01T00:01:01Z","message":{"role":"user","content":"`+notice+`"}}`)
	writeSession(t, filepath.Join(proj, forkParentID+".jsonl"), slashForkParentLines...)
	s := scanOne(t, root, child)
	if s.ForkAt != "" || s.ParentSessionID != "" || s.EmptyFork {
		t.Fatalf("ForkAt=%q ParentSessionID=%q EmptyFork=%v, want no lineage", s.ForkAt, s.ParentSessionID, s.EmptyFork)
	}
}

// The fork-source notice is not part of any turn: the fork's first prompt continues right below
// the fork point, and nothing of the notice (task block or queued copy) is shown.
func TestLoadConversationSlashForkLeavesOutForkSourceNotice(t *testing.T) {
	src := filepath.Join(t.TempDir(), "child.jsonl")
	writeSession(t, src, append(append([]string{}, slashForkChild...), slashForkContinuation...)...)
	c, err := (&Plugin{}).LoadConversation(context.Background(), domain.SessionRef{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Nodes["n1"]; ok {
		t.Fatal("the fork-source notice should not be a node")
	}
	if got := c.Nodes["u2"].Parent; got != "a1" {
		t.Fatalf("the fork's first prompt hangs under %q, want the fork point a1", got)
	}
	if path := c.ActivePath(); len(path) != 4 || path[len(path)-1] != "a2" {
		t.Fatalf("active path = %v, want u1 a1 u2 a2", path)
	}
	for id, n := range c.Nodes {
		for _, e := range n.Events {
			if e.Kind == domain.EventTask || e.Kind == domain.EventQueued {
				t.Fatalf("node %s shows %s %q from the notice", id, e.Kind, e.Text)
			}
		}
	}
}

const (
	forkParentID = "defb4851-3c72-4d12-b74a-9dc1de2f7bc9"
	forkChildID  = "e6684ea0-15ce-427b-85db-a924d4f68caf"
)

// The parent's side of a /fork, cut down from a real one: the copied history, then the command
// and its output naming the new session by the first 8 characters of its id.
var slashForkParentLines = []string{
	`{"uuid":"u1","parentUuid":null,"type":"user","cwd":"/work","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"test"}}`,
	`{"uuid":"a1","parentUuid":"u1","type":"assistant","timestamp":"2026-01-01T00:00:03Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`,
	`{"uuid":"c1","parentUuid":"a1","type":"system","subtype":"local_command","timestamp":"2026-01-01T00:01:00Z","content":"<command-name>/fork</command-name>\n<command-message>fork</command-message>\n<command-args></command-args>"}`,
	`{"uuid":"c2","parentUuid":"c1","type":"system","subtype":"local_command","timestamp":"2026-01-01T00:01:00Z","content":"<local-command-stdout>session waiting for a prompt · e6684ea0</local-command-stdout>","commandRun":{"command":"fork","args":""}}`,
}

func TestScanSlashForkFindsParentByItsForkOutput(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	child := filepath.Join(proj, forkChildID+".jsonl")
	writeSession(t, child, append(append([]string{}, slashForkChild...), slashForkContinuation...)...)
	writeSession(t, filepath.Join(proj, forkParentID+".jsonl"), slashForkParentLines...)
	// Sessions copied from the parent after its /fork hold the same output record. They sort
	// before the parent so that only telling copies apart keeps them from being taken for it.
	planForkCopy := make([]string, 0, len(slashForkParentLines))
	for _, l := range slashForkParentLines {
		planForkCopy = append(planForkCopy, strings.Replace(l, `{`, `{"parentSessionId":"`+forkParentID+`","parentLastUuid":"c2",`, 1))
	}
	writeSession(t, filepath.Join(proj, "11111111-0000-0000-0000-000000000000.jsonl"), planForkCopy...)
	branchCopy := make([]string, 0, len(slashForkParentLines))
	for _, l := range slashForkParentLines {
		branchCopy = append(branchCopy, strings.Replace(l, `{`, `{"forkedFrom":{"sessionId":"`+forkParentID+`"},`, 1))
	}
	writeSession(t, filepath.Join(proj, "22222222-0000-0000-0000-000000000000.jsonl"), branchCopy...)
	slashForkCopy := append(append([]string{}, slashForkParentLines...),
		`{"uuid":"n2","parentUuid":"c2","type":"user","timestamp":"2026-01-01T00:02:00Z","message":{"role":"user","content":"<task-notification>\n<fork-source>\ncopy\n</fork-source>\n</task-notification>"}}`)
	writeSession(t, filepath.Join(proj, "00000000-0000-0000-0000-000000000000.jsonl"), slashForkCopy...)
	// A /fork given a prompt gets its notice only after that prompt's turn; until then the copy
	// has nothing after it but the turn.
	unnotifiedSlashForkCopy := append([]string{`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-01T00:02:00Z","content":"` + noticeText("2026-01-01T00:01:00Z") + `"}`}, slashForkParentLines...)
	unnotifiedSlashForkCopy = append(unnotifiedSlashForkCopy, `{"uuid":"f2","parentUuid":"c2","type":"user","timestamp":"2026-01-01T00:02:00Z","message":{"role":"user","content":"look into it"}}`)
	writeSession(t, filepath.Join(proj, "33333333-0000-0000-0000-000000000000.jsonl"), unnotifiedSlashForkCopy...)

	if s := scanOne(t, root, child); s.ParentSessionID != forkParentID || s.ForkAt != "a1" {
		t.Fatalf("ParentSessionID=%q ForkAt=%q, want %s at a1", s.ParentSessionID, s.ForkAt, forkParentID)
	}
}

// The output of a /fork given a prompt carries the prompt too, and the fork point found for the child
// is one the parent has, so the child can be shown as a branch of the parent.
func TestScanSlashForkGivenAPromptFindsParentAtTheForkPoint(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	child := filepath.Join(proj, forkChildID+".jsonl")
	writeSession(t, child, slashForkWithPrompt...)
	writeSession(t, filepath.Join(proj, forkParentID+".jsonl"), append(append([]string{}, slashForkParentLines[:3]...),
		`{"uuid":"c2","parentUuid":"c1","type":"system","subtype":"local_command","timestamp":"2026-01-01T00:01:00Z","content":"<local-command-stdout>session running · look into it · e6684ea0</local-command-stdout>","commandRun":{"command":"fork","args":"look into it"}}`)...)
	if s := scanOne(t, root, child); s.ParentSessionID != forkParentID || s.ForkAt != "a1" {
		t.Fatalf("ParentSessionID=%q ForkAt=%q, want %s at a1", s.ParentSessionID, s.ForkAt, forkParentID)
	}
}

// A parent that was itself made by /fork has its own notice first and the /fork that made the
// child after it; only output before its own notice is a copy. A later fork of that parent, still
// in the turn of its prompt, holds a copy of the parent's notice but not yet one of its own.
func TestScanSlashForkFindsParentThatIsItselfASlashFork(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	child := filepath.Join(proj, forkChildID+".jsonl")
	writeSession(t, child, append(append([]string{}, slashForkChild...), slashForkContinuation...)...)
	parentNotice := noticeText("2025-12-31T00:00:00Z")
	parentHistory := []string{
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2025-12-31T00:01:00Z","content":"` + parentNotice + `"}`,
		`{"uuid":"g1","parentUuid":null,"type":"user","timestamp":"2025-12-31T00:00:00Z","message":{"role":"user","content":"older"}}`,
		`{"uuid":"gn","parentUuid":"g1","type":"user","timestamp":"2025-12-31T00:01:00Z","message":{"role":"user","content":"` + parentNotice + `"}}`,
	}
	writeSession(t, filepath.Join(proj, forkParentID+".jsonl"), append(append([]string{}, parentHistory...), slashForkParentLines...)...)
	sibling := []string{`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-01T00:02:00Z","content":"` + noticeText("2026-01-01T00:01:00Z") + `"}`}
	sibling = append(append(sibling, parentHistory[1:]...), slashForkParentLines...)
	sibling = append(sibling, `{"uuid":"f2","parentUuid":"c2","type":"user","timestamp":"2026-01-01T00:02:00Z","message":{"role":"user","content":"look into it"}}`)
	writeSession(t, filepath.Join(proj, "33333333-0000-0000-0000-000000000000.jsonl"), sibling...)
	if s := scanOne(t, root, child); s.ParentSessionID != forkParentID {
		t.Fatalf("ParentSessionID=%q, want %s", s.ParentSessionID, forkParentID)
	}
}

// With the parent's log gone the fork still knows where it forked, just not from which session.
func TestScanSlashForkWithoutParentLogKeepsForkPoint(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "proj", forkChildID+".jsonl")
	writeSession(t, child, append(append([]string{}, slashForkChild...), slashForkContinuation...)...)
	if s := scanOne(t, root, child); s.ParentSessionID != "" || s.ForkAt != "a1" {
		t.Fatalf("ParentSessionID=%q ForkAt=%q, want no parent and a1", s.ParentSessionID, s.ForkAt)
	}
}

// Only a notice whose content is <fork-source> is left out; a task notification that merely
// mentions the tag in its result still shows as a task.
func TestParseKeepsTaskNotificationQuotingForkSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "s.jsonl")
	writeSession(t, src,
		`{"uuid":"u1","parentUuid":null,"type":"user","timestamp":"2026-11-04T01:00:00Z","message":{"role":"user","content":"go"}}`,
		`{"uuid":"t1","parentUuid":"u1","type":"user","timestamp":"2026-11-04T01:00:05Z","message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n<result>saw <fork-source> in the log</result>\n</task-notification>"}}`,
	)
	_, nodes, _, _, _, _, _, forkAt := parse(context.Background(), src)
	if forkAt != "" {
		t.Fatalf("forkAt = %q, want none", forkAt)
	}
	for _, n := range nodes {
		if n.ID == "t1" && len(n.Events) == 1 && n.Events[0].Kind == domain.EventTask {
			return
		}
	}
	t.Fatalf("the task notification should stay a task: %#v", nodes)
}

// Open a native fork on its own (the fallback path where the root ancestor isn't found). Since
// the conversation display was canonicalized to start from the root ancestor, the old behavior of
// classifying the parent's original continuation (directly under the fork point) as main(origin)
// is gone. p2 must not become main (it isn't on a fork branch either, so it's a rewind).
func TestLoadConversationNativeForkNoMain(t *testing.T) {
	d := t.TempDir()
	proj := filepath.Join(d, "proj")
	sub := filepath.Join(proj, "parent", "subagents")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(proj, "parent.jsonl")
	// The parent continues p1 → p2; the fork branches at p1.
	_ = os.WriteFile(parentPath, []byte(
		`{"uuid":"p1","parentUuid":null,"timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","content":[{"type":"text","text":"q1"}]}}`+"\n"+
			`{"uuid":"p2","parentUuid":"p1","timestamp":"2026-01-01T00:00:01Z","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a1"}]}}`+"\n"), 0600)
	forkPath := filepath.Join(sub, "agent-x.jsonl")
	_ = os.WriteFile(forkPath, []byte(
		`{"uuid":"f1","parentUuid":"p1","parentSessionId":"parent","parentLastUuid":"p1","timestamp":"2026-01-01T00:01:00Z","type":"user","message":{"role":"user","content":[{"type":"text","text":"q2"}]}}`+"\n"), 0600)
	p := &Plugin{}
	c, err := p.LoadConversation(context.Background(), domain.SessionRef{Source: forkPath})
	if err != nil {
		t.Fatal(err)
	}
	// The parent's original continuation p2 must not be main (canonicalization dropped main; the
	// mainline now comes in on the active side).
	if k := conversation.BranchKind(*c, "p2"); k == "main" {
		t.Fatalf("parent continuation p2 should not be main, got %q", k)
	}
}

func TestParseStopReasonToolUseWithoutBlockIsToolCallTail(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Let me check."}],"stop_reason":"tool_use"}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if got := common.LastMeaningful(ev); got != domain.EventToolCall {
		t.Fatalf("last kind=%q events=%#v", got, ev)
	}
	if len(nodes) != 1 || len(nodes[0].Events) != 1 || nodes[0].Events[0].Kind != domain.EventAssistant {
		t.Fatalf("synthetic tail status should not be added to conversation node: %#v", nodes)
	}
}

func TestDetectActiveIgnoresStaleRuntimeWithoutMatchedProcess(t *testing.T) {
	d := t.TempDir()
	rt := t.TempDir()
	sid := "sid-stale"
	if err := os.WriteFile(filepath.Join(rt, "1.json"), []byte(`{"sessionId":"`+sid+`","status":"busy"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{ProjectsDir: d, RuntimeDir: rt, Executable: "claude"}}
	ss, err := p.DetectActive(context.Background(), []domain.Session{{PluginID: "claude", AgentType: "claude", SessionID: sid, LastKind: domain.EventReasoning}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != "" {
		t.Fatalf("stale runtime without matched process should not mark active: %#v", ss[0])
	}
}

func TestDetectActiveIgnoresProcessMatchedOnlyByCWD(t *testing.T) {
	p := &Plugin{id: "claude", o: Options{RuntimeDir: t.TempDir(), Executable: "claude"}}
	sessions := []domain.Session{{
		PluginID:  "claude",
		AgentType: "claude",
		SessionID: "completed",
		CWD:       "/work",
		UpdatedAt: time.Unix(20, 0),
		LastKind:  domain.EventTurnComplete,
	}}

	processes := []domain.Process{
		{PID: 1, Executable: "phpstorm"},
		{PID: 2, PPID: 1, Executable: "claude", CWD: "/work"},
	}
	ss, err := p.DetectActive(context.Background(), sessions, processes)
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != "" {
		t.Fatalf("process without direct session evidence should not mark a same-cwd session active: %#v", ss[0])
	}
}

func TestDetectActiveFallsBackToCWDForTerminalProcess(t *testing.T) {
	p := &Plugin{id: "claude", o: Options{RuntimeDir: t.TempDir(), Executable: "claude"}}
	sessions := []domain.Session{{
		PluginID:  "claude",
		AgentType: "claude",
		SessionID: "terminal-session",
		CWD:       "/work",
		UpdatedAt: time.Unix(20, 0),
		LastKind:  domain.EventTurnComplete,
	}}
	processes := []domain.Process{
		{PID: 1, Executable: "zsh"},
		{PID: 2, PPID: 1, Executable: "claude", CWD: "/work"},
	}

	ss, err := p.DetectActive(context.Background(), sessions, processes)
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != domain.StatusReady {
		t.Fatalf("terminal-launched process should retain the cwd fallback: %#v", ss[0])
	}
}

func TestDetectActiveRuntimeOverridesTailOnlyWhenMatched(t *testing.T) {
	rt := t.TempDir()
	sid := "sid-live"
	if err := os.WriteFile(filepath.Join(rt, "1.json"), []byte(`{"sessionId":"`+sid+`","status":"idle"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
	ss, err := p.DetectActive(context.Background(), []domain.Session{{PluginID: "claude", AgentType: "claude", SessionID: sid, LastKind: domain.EventReasoning}}, []domain.Process{{Args: []string{sid}}})
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != domain.StatusReady {
		t.Fatalf("matched idle runtime should be ready: %#v", ss[0])
	}
}

// An interactively launched claude opens no jsonl and carries no sessionId in its args, so the
// only clue is the pid in the runtime json. Verify that a pid match alone detects it as active.
func TestDetectActiveMatchesByRuntimePID(t *testing.T) {
	rt := t.TempDir()
	sid := "sid-pid"
	if err := os.WriteFile(filepath.Join(rt, "4242.json"), []byte(`{"sessionId":"`+sid+`","pid":4242,"status":"idle"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
	ss, err := p.DetectActive(context.Background(), []domain.Session{{PluginID: "claude", AgentType: "claude", SessionID: sid, LastKind: domain.EventReasoning}}, []domain.Process{{PID: 4242, Executable: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != domain.StatusReady {
		t.Fatalf("runtime pid match with idle status should be ready: %#v", ss[0])
	}
}

// After a /clear the process keeps the arguments it was started with, so `claude --resume <old>` still
// names the old session while the runtime json maps the pid to the new one. Only the new one is active.
func TestDetectActiveRuntimePIDOverridesStaleResumeArgs(t *testing.T) {
	rt := t.TempDir()
	if err := os.WriteFile(filepath.Join(rt, "4242.json"), []byte(`{"sessionId":"sid-new","pid":4242,"status":"idle"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
	sessions := []domain.Session{
		{PluginID: "claude", AgentType: "claude", SessionID: "sid-old", LastKind: domain.EventTurnComplete},
		{PluginID: "claude", AgentType: "claude", SessionID: "sid-new", LastKind: domain.EventTurnComplete},
	}
	ss, err := p.DetectActive(context.Background(), sessions, []domain.Process{{PID: 4242, Executable: "claude", Args: []string{"claude", "--resume", "sid-old"}}})
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != "" {
		t.Fatalf("the session the process was started with is no longer active: %#v", ss[0])
	}
	if ss[1].Status != domain.StatusReady {
		t.Fatalf("the session the runtime json names should be ready: %#v", ss[1])
	}

	// A process the runtime json does not know yet (just started) is still matched by its arguments.
	ss, err = p.DetectActive(context.Background(), []domain.Session{sessions[0]}, []domain.Process{{PID: 5555, Executable: "claude", Args: []string{"claude", "--resume", "sid-old"}}})
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != domain.StatusReady {
		t.Fatalf("an unmapped process should still match by its arguments: %#v", ss[0])
	}
}

// A background shell (run_in_background) outlives the agent turn: older releases report the runtime
// status idle while a shell child of the claude pid is still running. The session must be shown as
// a finished turn with a shell still working, neither ready nor running.
func TestDetectActiveIdleWithShellChildIsShell(t *testing.T) {
	rt := t.TempDir()
	sid := "sid-shell"
	if err := os.WriteFile(filepath.Join(rt, "4242.json"), []byte(`{"sessionId":"`+sid+`","pid":4242,"status":"idle"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
	sess := []domain.Session{{PluginID: "claude", AgentType: "claude", SessionID: sid, LastKind: domain.EventToolResult}}

	// No shell child: idle runtime maps to ready.
	ready, err := p.DetectActive(context.Background(), append([]domain.Session(nil), sess...), []domain.Process{{PID: 4242, Executable: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if ready[0].Status != domain.StatusReady {
		t.Fatalf("no shell child should be ready: %#v", ready[0])
	}

	// A non-shell child (e.g. an MCP server) is not a shell at work.
	mcpOnly, err := p.DetectActive(context.Background(), append([]domain.Session(nil), sess...), []domain.Process{
		{PID: 4242, Executable: "claude"},
		{PID: 5000, PPID: 4242, Executable: "npm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mcpOnly[0].Status != domain.StatusReady {
		t.Fatalf("a non-shell child should leave it ready: %#v", mcpOnly[0])
	}

	shell, err := p.DetectActive(context.Background(), append([]domain.Session(nil), sess...), []domain.Process{
		{PID: 4242, Executable: "claude"},
		{PID: 5000, PPID: 4242, Executable: "npm"},
		{PID: 5001, PPID: 4242, Executable: "zsh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if shell[0].Status != domain.StatusShell {
		t.Fatalf("idle with a live shell child should be shell: %#v", shell[0])
	}
}

// Newer releases name the same state themselves: the runtime status "shell" means the turn is over
// while a background Bash task is unfinished.
func TestDetectActiveShellRuntimeIsShell(t *testing.T) {
	rt := t.TempDir()
	sid := "sid-shell-runtime"
	if err := os.WriteFile(filepath.Join(rt, "4242.json"), []byte(`{"sessionId":"`+sid+`","pid":4242,"status":"shell"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
	// The tail says the model is writing, which the runtime status must override.
	ss, err := p.DetectActive(context.Background(), []domain.Session{{PluginID: "claude", AgentType: "claude", SessionID: sid, LastKind: domain.EventStream}}, []domain.Process{{PID: 4242, Executable: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != domain.StatusShell || ss[0].PermissionWait {
		t.Fatalf("shell runtime should be shell without an ask: %#v", ss[0])
	}
}

// A shell child during a turn is the Bash tool at work in that turn, so the session stays running.
func TestDetectActiveBusyWithShellChildStaysRunning(t *testing.T) {
	rt := t.TempDir()
	sid := "sid-busy-shell"
	if err := os.WriteFile(filepath.Join(rt, "4242.json"), []byte(`{"sessionId":"`+sid+`","pid":4242,"status":"busy"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
	ss, err := p.DetectActive(context.Background(), []domain.Session{{PluginID: "claude", AgentType: "claude", SessionID: sid, LastKind: domain.EventToolCall}}, []domain.Process{
		{PID: 4242, Executable: "claude"},
		{PID: 5001, PPID: 4242, Executable: "zsh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != domain.StatusRunning {
		t.Fatalf("busy with a shell child should stay running: %#v", ss[0])
	}
}

// A waiting runtime is blocked on the user whatever the dialog is: the waitingFor label varies by
// release ("permission prompt", "input needed" for an AskUserQuestion, …) and must not gate the flag.
func TestDetectActiveWaitingRuntimeIsPermissionWait(t *testing.T) {
	for _, tc := range []struct {
		name string
		rt   string
		want bool
	}{
		{"permission prompt", `"status":"waiting","waitingFor":"permission prompt"`, true},
		{"input needed", `"status":"waiting","waitingFor":"input needed"`, true},
		{"unlabeled wait", `"status":"waiting"`, true},
		{"busy", `"status":"busy"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := t.TempDir()
			sid := "sid-wait"
			if err := os.WriteFile(filepath.Join(rt, "4242.json"), []byte(`{"sessionId":"`+sid+`","pid":4242,`+tc.rt+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
			ss, err := p.DetectActive(context.Background(), []domain.Session{{PluginID: "claude", AgentType: "claude", SessionID: sid, LastKind: domain.EventToolCall}}, []domain.Process{{PID: 4242, Executable: "claude"}})
			if err != nil {
				t.Fatal(err)
			}
			if ss[0].Status != domain.StatusRunning {
				t.Fatalf("waiting/busy runtime should be running: %#v", ss[0])
			}
			if ss[0].PermissionWait != tc.want {
				t.Fatalf("permission wait=%v want %v: %#v", ss[0].PermissionWait, tc.want, ss[0])
			}
		})
	}
}

// When several sessions run concurrently in the same cwd, verify that pid matching from the
// runtime json resolves both without relying on the shared cwd.
func TestDetectActivePIDMatchResolvesSameCWDCollision(t *testing.T) {
	rt := t.TempDir()
	older, newer := "sid-older", "sid-newer"
	if err := os.WriteFile(filepath.Join(rt, "100.json"), []byte(`{"sessionId":"`+older+`","pid":100,"status":"idle"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt, "200.json"), []byte(`{"sessionId":"`+newer+`","pid":200,"status":"busy"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
	cwd := "/work/repo"
	t0 := time.Unix(1000, 0)
	sessions := []domain.Session{
		{PluginID: "claude", AgentType: "claude", SessionID: older, CWD: cwd, UpdatedAt: t0, LastKind: domain.EventReasoning},
		{PluginID: "claude", AgentType: "claude", SessionID: newer, CWD: cwd, UpdatedAt: t0.Add(time.Minute), LastKind: domain.EventReasoning},
	}
	procs := []domain.Process{
		{PID: 100, Executable: "claude", CWD: cwd},
		{PID: 200, Executable: "claude", CWD: cwd},
	}
	ss, err := p.DetectActive(context.Background(), sessions, procs)
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != domain.StatusReady {
		t.Fatalf("older session should be ready via pid match, not lost to cwd collision: %#v", ss[0])
	}
	if ss[1].Status != domain.StatusRunning {
		t.Fatalf("newer session should be running: %#v", ss[1])
	}
}

// When the runtime status is an unknown value (anything other than busy/shell/idle/waiting), it falls
// back to the last event kind. Without the fallback the Status would be left empty and the session
// would drop out of the active list.
func TestDetectActiveRuntimeUnknownStatusFallsBackToKind(t *testing.T) {
	rt := t.TempDir()
	sid := "sid-unknown"
	if err := os.WriteFile(filepath.Join(rt, "9.json"), []byte(`{"sessionId":"`+sid+`","pid":9,"status":"frobnicate"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "claude", o: Options{RuntimeDir: rt, Executable: "claude"}}
	ss, err := p.DetectActive(context.Background(), []domain.Session{{PluginID: "claude", AgentType: "claude", SessionID: sid, LastKind: domain.EventTurnComplete}}, []domain.Process{{PID: 9, Executable: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Status != domain.StatusReady {
		t.Fatalf("unknown runtime status should fall back to kind (turn_complete→ready), not drop: %#v", ss[0])
	}
}

// Claude writes a content block only once it is complete, so the tail of a log names what has just
// ended. LastKind must instead report what the session is doing now: a finished thinking block means
// the model is already generating the next one, and a tool result means it is back to thinking.
func TestScanLastKindIsOngoingActivity(t *testing.T) {
	d := t.TempDir()
	dir := filepath.Join(d, "proj")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	prompt := `{"uuid":"u1","cwd":"/work","timestamp":"2026-06-23T00:00:00Z","message":{"role":"user","content":"question"}}`
	thinking := `{"uuid":"a1","parentUuid":"u1","cwd":"/work","timestamp":"2026-06-23T00:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"pondering","signature":"sig"}]}}`
	toolUse := `{"uuid":"a2","parentUuid":"a1","cwd":"/work","timestamp":"2026-06-23T00:00:02Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/work/x.go"}}]}}`
	toolResult := `{"uuid":"u2","parentUuid":"a2","cwd":"/work","timestamp":"2026-06-23T00:00:03Z","message":{"role":"user","content":[{"type":"tool_result","content":"file body"}]}}`
	answer := `{"uuid":"a3","parentUuid":"u2","cwd":"/work","timestamp":"2026-06-23T00:00:04Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`
	away := func(parent string) string {
		return `{"uuid":"s1","parentUuid":"` + parent + `","cwd":"/work","timestamp":"2026-06-23T00:00:05Z","type":"system","subtype":"away_summary","content":"answered the question","isMeta":false}`
	}
	files := map[string]struct {
		lines []string
		want  domain.EventKind
	}{
		"after-thinking.jsonl": {[]string{prompt, thinking}, domain.EventStream},
		"after-result.jsonl":   {[]string{prompt, thinking, toolUse, toolResult}, domain.EventReasoning},
		"after-tool-use.jsonl": {[]string{prompt, thinking, toolUse}, domain.EventToolCall},
		// A recap of the session is not an activity: the turn it follows decides.
		"away-after-answer.jsonl":    {[]string{prompt, thinking, toolUse, toolResult, answer, away("a3")}, domain.EventTurnComplete},
		"away-during-tool-use.jsonl": {[]string{prompt, thinking, toolUse, away("a2")}, domain.EventToolCall},
		// Nor is a compaction done while the session sat idle: its summary is not a prompt being answered.
		"idle-compaction.jsonl": {[]string{prompt, thinking, toolUse, toolResult, answer,
			`{"uuid":"b1","parentUuid":null,"logicalParentUuid":"a3","cwd":"/work","timestamp":"2026-06-23T00:55:00Z","type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto"}}`,
			`{"uuid":"s1","parentUuid":"b1","cwd":"/work","timestamp":"2026-06-23T00:55:00Z","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued from a previous conversation."}}`,
		}, domain.EventTurnComplete},
	}
	for name, c := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(c.lines, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := &Plugin{id: "claude", o: Options{ProjectsDir: d}}
	res, err := p.Scan(context.Background(), plugin.ScanInput{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]domain.EventKind{}
	for _, s := range res.Sessions {
		seen[filepath.Base(s.SourceRef.Source)] = s.LastKind
	}
	for name, c := range files {
		if seen[name] != c.want {
			t.Errorf("%s: LastKind = %q, want %q", name, seen[name], c.want)
		}
	}
}
