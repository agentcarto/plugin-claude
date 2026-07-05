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

func TestParseSkipsEmptyThinkingBlock(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"thinking","thinking":"real thought"},{"type":"text","text":"answer"}]}}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, _, _, _, _, _, _, _ := parse(context.Background(), src)
	var reasoning []string
	for _, e := range ev {
		if e.Kind == domain.EventReasoning {
			reasoning = append(reasoning, e.Text)
		}
	}
	if len(reasoning) != 1 || reasoning[0] != "real thought" {
		t.Fatalf("expected only the non-empty thinking block, got %#v", reasoning)
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

// away_summary system records carry the recap of what the agent did while the
// user was away; they surface as assistant text like Claude Code shows them.
func TestParseSystemAwaySummary(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "s.jsonl")
	data := []byte(`{"uuid":"a","parentUuid":"","timestamp":"2026-01-01T00:00:00Z","type":"system","subtype":"away_summary","content":"built the slides and verified the output","isMeta":false}` + "\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	ev, nodes, _, _, _, _, _, _ := parse(context.Background(), src)
	if len(ev) != 1 || ev[0].Kind != domain.EventAssistant || ev[0].Text != "built the slides and verified the output" {
		t.Fatalf("events=%#v", ev)
	}
	if len(nodes) != 1 || len(nodes[0].Events) != 1 {
		t.Fatalf("nodes=%#v", nodes)
	}
}

// Newer versions log slash commands only as system/local_command records; the
// command invocation must surface as a user event (the core logic turns the
// <command-name> wrapper into a turn boundary) while empty output wrappers stay
// silent.
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

// A background shell (run_in_background) outlives the agent turn: the runtime status reports idle
// while a shell child of the claude pid is still running. The session must not be shown as ready.
func TestDetectActiveShellChildKeepsRunning(t *testing.T) {
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

	// A live shell child of the claude pid keeps it running; a non-shell child (e.g. MCP server) does not.
	ps := []domain.Process{
		{PID: 4242, Executable: "claude"},
		{PID: 5000, PPID: 4242, Executable: "npm"},
		{PID: 5001, PPID: 4242, Executable: "zsh"},
	}
	run, err := p.DetectActive(context.Background(), append([]domain.Session(nil), sess...), ps)
	if err != nil {
		t.Fatal(err)
	}
	if run[0].Status != domain.StatusRunning {
		t.Fatalf("live shell child should keep running: %#v", run[0])
	}
}

// When several sessions run concurrently in the same cwd, cwd approximation alone only catches
// the most recent one and misattributes the older ones. Verify that pid matching from the runtime
// json correctly resolves both.
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

// When the runtime status is an unknown value (anything other than idle/busy/waiting), it falls
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
