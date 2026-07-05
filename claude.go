package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/conversation"
	"github.com/agentcarto/core/domain"
	"github.com/agentcarto/core/plugin"
	"github.com/agentcarto/core/scan"

	"gopkg.in/yaml.v3"
)

type Options struct {
	ProjectsDir string `yaml:"projects_dir"`
	RuntimeDir  string `yaml:"runtime_dir"`
	Executable  string `yaml:"executable"`
}

type Factory struct{}

func (Factory) Descriptor() plugin.Descriptor {
	// ParserVersion=3: fork sessions now derive their SessionID from the file name (fixes key
	// collisions where a subagent's sessionId equals its parent's). Bumping the version
	// invalidates the old (v2) cache so everything is re-parsed.
	// Note: excluding empty sessions with no real content (only /clear) from the list
	// (NodesHaveRealContent) is shipped without bumping ParserVersion. The TUI discards warm
	// state and fully re-parses on every startup (tui.scan's initial full pass), so even with an
	// old cache the exclusion takes effect on the next launch.
	// ParserVersion=6: user events now carry the normalized Prompt/Command fields
	// (agent-specific pseudo-prompt/command vocabulary moved out of core).
	// ParserVersion=7: tool calls carry ToolArg/ToolDetail/Changes and task
	// notifications become EventTask (agent-specific rendering moved out of the host).
	return plugin.Descriptor{Type: "claude", DisplayName: "Claude", ParserVersion: "7", Capabilities: domain.Capabilities{Scan: true, Conversation: true, Active: true, Resume: true, Rewind: true, Relocate: true}}
}

func (Factory) New(id string, n *yaml.Node) (any, error) {
	o := Options{ProjectsDir: "~/.claude/projects", RuntimeDir: "~/.claude/sessions", Executable: "claude"}
	if e := common.DecodeOptions(n, &o); e != nil {
		return nil, e
	}
	o.ProjectsDir = common.ExpandHome(o.ProjectsDir)
	o.RuntimeDir = common.ExpandHome(o.RuntimeDir)
	return &Plugin{id: id, o: o}, nil
}

type Plugin struct {
	id string
	o  Options
}

func (p *Plugin) Executable() string { return p.o.Executable }

func (p *Plugin) Scan(ctx context.Context, in plugin.ScanInput) (plugin.ScanOutput, error) {
	cache := scan.New(in.Warm, in.Dead, Factory{}.Descriptor().ParserVersion)
	// Walk the ProjectsDir tree once and split normal sessions from subagent forks by path, to
	// avoid paying the readdir cost twice. The final ordering doesn't matter: the catalog
	// re-sorts by UpdatedAt, so a mixed traversal order has no effect on display.
	files, e := common.WalkFiles(p.o.ProjectsDir, func(x string) bool {
		return filepath.Ext(x) == ".jsonl"
	})
	if e != nil {
		return plugin.ScanOutput{}, e
	}
	out := make([]domain.Session, 0, len(files))
	for _, f := range files {
		if s, ok := cache.Reuse(f); ok {
			out = append(out, s)
			continue
		}
		if cache.Skip(f) {
			continue
		}
		var (
			s  domain.Session
			ok bool
		)
		if isSubagentForkPath(f) {
			s, ok = p.forkSession(ctx, f)
		} else {
			s, ok = p.scanSession(ctx, f)
		}
		if !ok {
			cache.Dead(f)
			continue
		}
		cache.Stamp(&s)
		out = append(out, s)
	}
	return plugin.ScanOutput{Sessions: out, Dead: cache.DeadOut()}, nil
}

// isSubagentForkPath reports whether a file lives under a subagents/ directory.
func isSubagentForkPath(path string) bool {
	sep := string(filepath.Separator)
	return strings.Contains(path, sep+"subagents"+sep)
}

// scanSession builds the catalog entry for a normal (non-fork) session file. It returns false
// when the file holds nothing parseable or no real conversation content, so the caller can mark
// it dead.
func (p *Plugin) scanSession(ctx context.Context, f string) (domain.Session, bool) {
	ev, nodes, _, cwd, start, model, psid, plast := parse(ctx, f)
	if len(ev) == 0 && len(nodes) == 0 {
		return domain.Session{}, false
	}
	// Sessions that are only /clear, or have no real content, are kept out of the list. Once the
	// conversation grows the file changes, its fingerprint updates, and it gets re-parsed (same
	// as Copilot's empty-session exclusion).
	if !conversation.NodesHaveRealContent(nodes) {
		return domain.Session{}, false
	}
	id := common.IDFromPath(f)
	last := common.LastMeaningful(ev)
	if last == domain.EventAssistant {
		last = domain.EventStream
	}
	if cwd == "" {
		cwd = "(unknown)"
	}
	if start.IsZero() {
		start = common.FileTime(f)
	}
	s := domain.Session{PluginID: p.id, AgentType: "claude", SessionID: id, CWD: cwd, StartedAt: start, UpdatedAt: common.FileTime(f), Title: common.Title(ev, "(no title)"), Model: model, SourceRef: domain.SessionRef{Source: f}, LastKind: last, ParentSessionID: psid, ForkAt: plast, EmptyFork: isEmptyFork(nodes, plast)}
	return s, true
}

func (p *Plugin) forkSession(ctx context.Context, path string) (domain.Session, bool) {
	ev, _, _, cwd, start, _, parent, forkAt := parse(ctx, path)
	if len(ev) == 0 {
		return domain.Session{}, false
	}
	if forkAt == "" {
		return domain.Session{}, false // not a fork, just a regular subagent
	}
	// Fork files under subagents/ inherit the parent session's id in their sessionId field. Using
	// that as the SessionID would collide on key (plugin_id, session_id) with the main session,
	// causing cache-PK overwrites and duplicate rows in the list, so each reload picks a different
	// source and the display flips. Derive a unique fork id from the file name instead.
	sid := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title, cwd := forkTitle(ctx, path, ev, cwd)
	if cwd == "" {
		cwd = "(unknown)"
	}
	updated := common.FileTime(path)
	if start.IsZero() {
		start = updated
	}
	// A native subagent fork has a synthetic, filename-based SessionID (its real sessionId equals
	// the parent's). `claude --resume <SessionID>` fails because no such session exists, and the
	// fork branch has no resumable head either, so mark it unresumable (the app won't offer a
	// resume action).
	return domain.Session{PluginID: p.id, AgentType: "claude", SessionID: sid, CWD: cwd, StartedAt: start, UpdatedAt: updated, Title: title, SourceRef: domain.SessionRef{Source: path}, ParentSessionID: parent, ForkAt: forkAt, LastKind: common.LastMeaningful(ev), Unresumable: true}, true
}

// forkTitle derives the list title (and resolves the cwd) for a subagent fork. The title prefers
// the parent conversation's first message, since the fork child's conversation really begins
// there. If the parent file can't be read it falls back to the fork's own directive (with the
// fork boilerplate stripped), and finally to "(fork)". When the fork file itself has no cwd, the
// parent's cwd is used.
func forkTitle(ctx context.Context, path string, ev []domain.Event, cwd string) (title, resolvedCWD string) {
	directive := ""
	for _, e := range ev {
		if e.Kind == domain.EventUser {
			directive = common.CleanTitle(stripForkBoilerplate(e.Text))
			break
		}
	}
	parentPath := filepath.Dir(filepath.Dir(path)) + ".jsonl"
	if _, e := os.Stat(parentPath); e == nil {
		pev, _, _, pcwd, _, _, _, _ := parse(ctx, parentPath)
		title = common.Title(pev, "")
		if cwd == "" {
			cwd = pcwd
		}
	}
	if title == "" {
		title = directive
	}
	if title == "" {
		title = "(fork)"
	}
	return title, cwd
}

func stripForkBoilerplate(s string) string {
	const end = "</fork-boilerplate>"
	if i := strings.Index(s, end); i >= 0 {
		return strings.TrimSpace(s[i+len(end):])
	}
	return s
}

// userOrAssistant maps a message role to its event kind.
func userOrAssistant(role string) domain.EventKind {
	if role == "user" {
		return domain.EventUser
	}
	return domain.EventAssistant
}

// messageEvents converts a single message's content blocks into display events. A content block
// may be a bare string or a typed object (text/thinking/tool_use/tool_result); blocks of an
// unknown type produce no event.
func messageEvents(msg map[string]any, ts time.Time) []domain.Event {
	role := common.String(msg["role"])
	var es []domain.Event
	for _, b := range common.Slice(msg["content"]) {
		if text, ok := b.(string); ok && text != "" {
			es = append(es, domain.Event{Kind: userOrAssistant(role), Text: text, Timestamp: ts, RawType: role})
			continue
		}
		m := common.Map(b)
		bt := common.String(m["type"])
		text := common.Text(m["text"])
		k := domain.EventMeta
		tool := ""
		switch bt {
		case "text":
			k = userOrAssistant(role)
		case "thinking":
			k = domain.EventReasoning
			text = common.String(m["thinking"])
			// Newer models encrypt the thinking into the signature and leave the
			// thinking text empty (display:"omitted"). Skip such blocks instead of
			// rendering a blank reasoning event.
			if strings.TrimSpace(text) == "" {
				continue
			}
		case "tool_use":
			k = domain.EventToolCall
			tool = common.String(m["name"])
			text = common.Text(m["input"])
		case "tool_result":
			k = domain.EventToolResult
			text = common.Text(m["content"])
			// Image or tool_reference results have no text blocks; mark what is
			// there instead of showing an empty result.
			if strings.TrimSpace(text) == "" {
				text = nonTextSummary(m["content"])
			}
		}
		if k != domain.EventMeta {
			es = append(es, domain.Event{Kind: k, Text: text, Timestamp: ts, ToolName: tool, RawType: bt})
		}
	}
	// Fallback for messages whose content is a plain string rather than a list of blocks.
	if len(es) == 0 {
		if text, ok := msg["content"].(string); ok && text != "" {
			es = append(es, domain.Event{Kind: userOrAssistant(role), Text: text, Timestamp: ts, RawType: role})
		}
	}
	return es
}

// nonTextSummary renders a placeholder line per non-text content block, e.g.
// "[image]" for a screenshot result or "[tool: WebFetch]" for a tool reference.
func nonTextSummary(v any) string {
	var lines []string
	for _, b := range common.Slice(v) {
		m := common.Map(b)
		switch bt := common.String(m["type"]); bt {
		case "", "text":
		case "tool_reference":
			lines = append(lines, "[tool: "+common.String(m["tool_name"])+"]")
		default:
			lines = append(lines, "["+bt+"]")
		}
	}
	return strings.Join(lines, "\n")
}

// systemEvents surfaces the system records that carry conversation content:
// away_summary (what the agent accomplished while the user was away) and
// local_command (slash-command invocations and their output, which newer
// versions log here instead of as user records).
func systemEvents(o map[string]any, ts time.Time) []domain.Event {
	content := strings.TrimSpace(common.String(o["content"]))
	if content == "" {
		return nil
	}
	switch common.String(o["subtype"]) {
	case "away_summary":
		return []domain.Event{{Kind: domain.EventAssistant, Text: content, Timestamp: ts, RawType: "away_summary"}}
	case "local_command":
		if emptyCommandOutput(content) {
			return nil
		}
		// As a user event, the core conversation logic handles the
		// <command-name>/<local-command-stdout> wrappers exactly like the
		// user-record form older versions wrote.
		return []domain.Event{{Kind: domain.EventUser, Text: content, Timestamp: ts, RawType: "local_command"}}
	}
	return nil
}

var emptyCommandOutputRE = regexp.MustCompile(`^<local-command-std(out|err)>\s*</local-command-std(out|err)>$`)

// emptyCommandOutput reports whether content is an output wrapper with nothing in it.
func emptyCommandOutput(content string) bool {
	return emptyCommandOutputRE.MatchString(content)
}

func parse(ctx context.Context, path string) (ev []domain.Event, nodes []domain.ConvNode, queued []domain.Event, cwd string, start time.Time, model, parentSID, parentLast string) {
	_ = common.JSONLines(ctx, path, func(_ int, o map[string]any) error {
		ts := common.Time(common.String(o["timestamp"]))
		if start.IsZero() && !ts.IsZero() {
			start = ts
		}
		if cwd == "" {
			cwd = common.String(o["cwd"])
		}
		// rewind-fork records carry parent links (used by the manager to stitch sessions together).
		if parentSID == "" {
			parentSID = common.String(o["parentSessionId"])
		}
		if parentLast == "" {
			parentLast = common.String(o["parentLastUuid"])
		}
		msg := common.Map(o["message"])
		if common.String(o["type"]) == "queue-operation" {
			if common.String(o["operation"]) == "enqueue" {
				if txt := common.String(o["content"]); strings.TrimSpace(txt) != "" {
					queued = append(queued, domain.Event{Kind: domain.EventQueued, Text: txt, Timestamp: ts, RawType: "queued"})
				}
			}
			return nil
		}
		// Use the model from the first assistant message.
		if model == "" && common.String(o["type"]) == "assistant" {
			model = common.String(msg["model"])
		}
		// isMeta records (e.g. expanded commands) carry no display events, but we keep the node so
		// the tree stays connected.
		meta, _ := o["isMeta"].(bool)
		var es []domain.Event
		if !meta {
			es = messageEvents(msg, ts)
			if common.String(o["type"]) == "system" {
				es = append(es, systemEvents(o, ts)...)
			}
		}
		// The /compact auto-summary node is marked so the heading can render it as a boundary.
		if b, ok := o["isCompactSummary"].(bool); ok && b {
			for i := range es {
				es[i].RawType = domain.RawCompactSummary
			}
		}
		// Stamp each event from an assistant record with the model that produced
		// it, so the host can show per-turn models. Session.Model still takes the
		// first model (above) as the session-level fallback.
		if recModel := common.String(msg["model"]); recModel != "" {
			for i := range es {
				es[i].Model = recModel
			}
		}
		annotate(es)
		ev = append(ev, es...)
		if sr := common.String(msg["stop_reason"]); sr == "end_turn" {
			ev = append(ev, domain.Event{Kind: domain.EventTurnComplete, Timestamp: ts, RawType: "end_turn"})
		} else if sr == "tool_use" && !hasEventKind(es, domain.EventToolCall) {
			ev = append(ev, domain.Event{Kind: domain.EventToolCall, Timestamp: ts, RawType: "tool_use"})
		}
		if id := common.String(o["uuid"]); id != "" {
			// After /compact, records are written with parentUuid=None and the real connection
			// lives in logicalParentUuid. Prefer parentUuid, falling back to logicalParentUuid.
			parent := common.String(o["parentUuid"])
			if parent == "" {
				parent = common.String(o["logicalParentUuid"])
			}
			nodes = append(nodes, domain.ConvNode{ID: id, Parent: parent, Timestamp: ts, Events: es})
		}
		return nil
	})
	return ev, nodes, queued, cwd, start, model, parentSID, parentLast
}

// attachQueued associates user messages enqueued while the agent was running with the turn they
// belong to.
func attachQueued(c *domain.Conversation, queued []domain.Event) {
	if len(queued) == 0 {
		return
	}
	path := c.ActivePath()
	if len(path) == 0 {
		return
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	real := map[string]bool{}
	for _, id := range path {
		for _, e := range c.Nodes[id].Events {
			if e.Kind == domain.EventUser && e.Text != "" {
				real[norm(e.Text)] = true
			}
		}
	}
	for _, q := range queued {
		t := strings.TrimSpace(q.Text)
		if t == "" || isPseudoPrompt(t) || real[norm(t)] {
			continue
		}
		target := path[0]
		for _, id := range path {
			nts := c.Nodes[id].Timestamp
			if !nts.IsZero() && !q.Timestamp.IsZero() && !nts.After(q.Timestamp) {
				target = id
			}
		}
		n := c.Nodes[target]
		n.Events = append(n.Events, q)
		c.Nodes[target] = n
	}
}

// isClaudeProcess reports whether the executable or any argument's basename is claude.
func isClaudeProcess(p domain.Process) bool {
	toks := append([]string{p.Executable}, p.Args...)
	for _, t := range toks {
		b := filepath.Base(t)
		if b == "claude" || b == "claude.js" {
			return true
		}
	}
	return false
}

// isEmptyFork reports whether a full-copy fork has never been continued, i.e. its connection
// point forkAt is a leaf with no children. agentcarto's PlanFork writes only root→target, so
// before continuation target (=forkAt) is a leaf; continuing it adds a new node directly under
// forkAt. A native fork (diff only, forkAt not in its own nodes) always has a child under forkAt,
// so it returns false.
func isEmptyFork(nodes []domain.ConvNode, forkAt string) bool {
	if forkAt == "" {
		return false
	}
	inNodes := false
	for _, n := range nodes {
		if n.Parent == forkAt {
			return false // a child under forkAt means it was continued
		}
		if n.ID == forkAt {
			inNodes = true
		}
	}
	return inNodes
}

func hasEventKind(events []domain.Event, kind domain.EventKind) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func (p *Plugin) LoadConversation(ctx context.Context, r domain.SessionRef) (*domain.Conversation, error) {
	ev, n, queued, _, _, _, _, parentLast := parse(ctx, r.Source)
	var c domain.Conversation
	if len(n) == 0 {
		c = common.Linear(filterConversationEvents(ev))
		return &c, nil
	}
	c = domain.NewConversation(n)
	attachQueued(&c, queued)
	// A native fork (diff only; its connection point isn't in its own nodes) shows the pre-fork
	// history by grafting onto the parent history. A rewind-fork (full copy; the connection point
	// is in its own nodes) is shown as-is.
	if parentLast != "" {
		if _, ok := c.Nodes[parentLast]; !ok {
			if full, ok := claudeForkFull(ctx, r.Source, parentLast, n, c.ActiveLeaf); ok {
				return &full, nil
			}
		}
	}
	return &c, nil
}

// claudeForkFull grafts the fork file's diff tree onto the parent file's full tree at the
// connection point forkAt, and pins the active branch to the fork's tip.
func claudeForkFull(ctx context.Context, forkPath, forkAt string, forkNodes []domain.ConvNode, forkLeaf string) (domain.Conversation, bool) {
	parentPath := filepath.Dir(filepath.Dir(forkPath)) + ".jsonl"
	if _, e := os.Stat(parentPath); e != nil {
		return domain.Conversation{}, false
	}
	_, pn, _, _, _, _, _, _ := parse(ctx, parentPath)
	if len(pn) == 0 {
		return domain.Conversation{}, false
	}
	parentIDs := map[string]bool{}
	for _, nd := range pn {
		parentIDs[nd.ID] = true
	}
	if !parentIDs[forkAt] {
		return domain.Conversation{}, false
	}
	forkIDs := map[string]bool{}
	for _, nd := range forkNodes {
		forkIDs[nd.ID] = true
	}
	nodes := append([]domain.ConvNode(nil), pn...)
	for _, nd := range forkNodes {
		if parentIDs[nd.ID] {
			continue // skip nodes already present in the parent to avoid duplicates
		}
		par := nd.Parent
		if !forkIDs[par] { // the fork's root (its parent isn't inside the fork) attaches to the connection point
			par = forkAt
		}
		nodes = append(nodes, domain.ConvNode{ID: nd.ID, Parent: par, Timestamp: nd.Timestamp, Events: nd.Events})
	}
	c := domain.NewConversation(nodes)
	if forkLeaf != "" {
		c.ActiveLeaf = forkLeaf // pin the active branch to the fork's tip
	}
	// The parent's original continuation (parent-derived nodes directly under the fork point) used
	// to be classified as OriginRoots (=main), but that was dropped once the display was
	// canonicalized to start from the root ancestor. This function survives as a fallback for two
	// cases: opening a fork that can't be reached from the root (a standalone view where the
	// parent file isn't found), and the child-load fallback when opening from the parent side.
	// Because the loop above skips nodes already in the parent, the base is never duplicated.
	return c, true
}

func filterConversationEvents(events []domain.Event) []domain.Event {
	out := make([]domain.Event, 0, len(events))
	for _, e := range events {
		if e.Kind == domain.EventTurnComplete || (e.Kind == domain.EventToolCall && e.RawType == "tool_use" && e.Text == "") {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (p *Plugin) ResumeCommand(s domain.Session) (domain.Command, error) {
	if s.Unresumable {
		return domain.Command{}, fmt.Errorf("subagent fork has no resumable session id")
	}
	if s.Status != "" {
		return domain.Command{}, fmt.Errorf("session is active")
	}
	if _, e := os.Stat(s.CWD); e != nil {
		return domain.Command{}, fmt.Errorf("cwd unavailable: %w", e)
	}
	return domain.Command{Executable: p.o.Executable, Args: []string{"--resume", s.SessionID}, WorkingDirectory: s.CWD}, nil
}

func (p *Plugin) DetectActive(_ context.Context, ss []domain.Session, ps []domain.Process) ([]domain.Session, error) {
	runtimeBy := map[string]map[string]any{}
	pidToSession := map[int32]string{}
	files, _ := filepath.Glob(filepath.Join(p.o.RuntimeDir, "*.json"))
	for _, f := range files {
		b, e := os.ReadFile(f)
		if e != nil {
			continue
		}
		var o map[string]any
		if json.Unmarshal(b, &o) == nil {
			sid := common.String(o["sessionId"])
			runtimeBy[sid] = o
			if n, ok := o["pid"].(float64); ok && sid != "" {
				pidToSession[int32(n)] = sid
			}
		}
	}
	// pid match: ~/.claude/sessions/<pid>.json authoritatively maps pid to sessionId. A session
	// whose pid matches a live process is considered active (same shape as grok's
	// active_sessions.json). Unlike cwd approximation, this never confuses two sessions sharing a
	// cwd.
	pidMatched := map[string]bool{}
	for _, pr := range ps {
		if sid, ok := pidToSession[pr.PID]; ok {
			pidMatched[sid] = true
		}
	}
	// shell detection: Claude Code runs its Bash tool by spawning a shell as a direct child of the
	// claude process. A background shell (run_in_background) outlives the turn, so the runtime
	// status flips to "idle" (and would map to Ready) while the shell is still working. Mark any
	// session whose claude pid has a live shell child so it is not downgraded to Ready below.
	shellRunning := map[string]bool{}
	for _, pr := range ps {
		if sid, ok := pidToSession[pr.PPID]; ok && isShellProcess(pr) {
			shellRunning[sid] = true
		}
	}
	// cwd approximation: the most recent session whose cwd matches a claude process is also
	// treated as active. Processes already resolved by pid are excluded to avoid misattribution.
	cwdMatched := map[string]bool{}
	for _, pr := range ps {
		if pr.CWD == "" || !isClaudeProcess(pr) {
			continue
		}
		if _, known := pidToSession[pr.PID]; known {
			continue
		}
		best := -1
		for i := range ss {
			if ss[i].CWD == pr.CWD && (best < 0 || ss[i].UpdatedAt.After(ss[best].UpdatedAt)) {
				best = i
			}
		}
		if best >= 0 {
			cwdMatched[ss[best].SessionID] = true
		}
	}
	for i := range ss {
		s := &ss[i]
		matched := common.ProcessMatches(*s, ps) || pidMatched[s.SessionID] || cwdMatched[s.SessionID]
		if rt := runtimeBy[s.SessionID]; rt != nil && matched {
			switch common.String(rt["status"]) {
			case "busy", "waiting":
				s.Status = domain.StatusRunning
			case "idle":
				s.Status = domain.StatusReady
			default:
				// For an unknown runtime status (anything other than idle/busy/waiting), fall back
				// to the last event kind. Without this fallback the status would be left empty and
				// the session would drop out of the active list.
				s.Status = common.ActiveStatus(s.LastKind, true)
			}
			s.PermissionWait = common.String(rt["status"]) == "waiting" && common.String(rt["waitingFor"]) == "permission prompt"
			continue
		}
		if matched {
			s.Status = common.ActiveStatus(s.LastKind, true)
		}
	}
	// A session with a live shell child is still working even if the runtime/last-event status
	// says it is done; never present it as Ready.
	for i := range ss {
		if ss[i].Status == domain.StatusReady && shellRunning[ss[i].SessionID] {
			ss[i].Status = domain.StatusRunning
		}
	}
	return ss, nil
}

// shellNames lists the executable basenames Claude Code uses to run its Bash tool.
var shellNames = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true}

// isShellProcess reports whether a process is an interactive shell (used to detect a Bash tool's
// child shell still running under a claude session).
func isShellProcess(p domain.Process) bool {
	return shellNames[filepath.Base(p.Executable)]
}

func (p *Plugin) PlanFork(_ context.Context, s domain.Session, t domain.ForkTarget) (domain.MutationPlan, domain.Command, error) {
	if t.NodeID == "" {
		return domain.MutationPlan{}, domain.Command{}, fmt.Errorf("target node is required")
	}
	b, e := os.ReadFile(s.SourceRef.Source)
	if e != nil {
		return domain.MutationPlan{}, domain.Command{}, e
	}
	lines := strings.Split(string(b), "\n")
	var rows []map[string]any
	parents := map[string]string{}
	for _, line := range lines {
		// Lossless decode: every kept row is re-encoded below (they all gain
		// parent links), and a float64 round trip would corrupt >2^53 integers.
		o, e := common.UnmarshalJSONMap([]byte(line))
		if e != nil {
			continue
		}
		rows = append(rows, o)
		if id := common.String(o["uuid"]); id != "" {
			parents[id] = common.String(o["parentUuid"])
		}
	}
	if _, ok := parents[t.NodeID]; !ok {
		return domain.MutationPlan{}, domain.Command{}, fmt.Errorf("target node %q not found", t.NodeID)
	}
	// Keep the ancestor path up to the selected node (root→target).
	keep := map[string]bool{}
	for id, seen := t.NodeID, map[string]bool{}; id != "" && !seen[id]; id = parents[id] {
		seen[id] = true
		keep[id] = true
	}
	newID := common.NewID()
	var out bytes.Buffer
	for _, o := range rows {
		u := common.String(o["uuid"])
		if u != "" && !keep[u] {
			continue // drop other branches and anything after the selected node
		}
		// Drop last-prompt records pointing outside the kept path.
		if common.String(o["type"]) == "last-prompt" {
			if lf := common.String(o["leafUuid"]); lf != "" && !keep[lf] {
				continue
			}
		}
		if _, ok := o["sessionId"]; ok {
			o["sessionId"] = newID
		}
		// Attach parent links so this shows up as a fork (branch) of the original session.
		o["parentSessionId"] = s.SessionID
		o["parentLastUuid"] = t.NodeID
		x, e := common.MarshalJSONLine(o)
		if e != nil {
			return domain.MutationPlan{}, domain.Command{}, e
		}
		out.Write(x)
	}
	path := filepath.Join(filepath.Dir(s.SourceRef.Source), newID+".jsonl")
	plan := domain.MutationPlan{PluginID: p.id, Description: "fork Claude session", AllowedRoots: []string{p.o.ProjectsDir}, Writes: []domain.FileWrite{{Path: path, Data: out.Bytes(), Mode: 0600}}}
	cmd := domain.Command{Executable: p.o.Executable, Args: []string{"--resume", newID}, WorkingDirectory: s.CWD}
	return plan, cmd, nil
}

func ClaudeSlug(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func (p *Plugin) PlanRelocate(_ context.Context, old, new string, _ []domain.Session) (domain.MutationPlan, error) {
	oldDir := filepath.Join(p.o.ProjectsDir, ClaudeSlug(old))
	newDir := filepath.Join(p.o.ProjectsDir, ClaudeSlug(new))
	fs, e := common.WalkFiles(oldDir, func(x string) bool { return filepath.Ext(x) == ".jsonl" })
	if e != nil {
		return domain.MutationPlan{}, e
	}
	plan := domain.MutationPlan{PluginID: p.id, Description: "relocate Claude sessions", AllowedRoots: []string{p.o.ProjectsDir}}
	for _, f := range fs {
		data, n, e := common.RewriteJSONL(f, func(o map[string]any) bool {
			if common.String(o["cwd"]) == old {
				o["cwd"] = new
				return true
			}
			return false
		})
		if e != nil {
			return plan, e
		}
		if n > 0 {
			plan.Writes = append(plan.Writes, domain.FileWrite{Path: f, Data: data, Mode: 0600})
		}
	}
	if oldDir != newDir {
		plan.Moves = append(plan.Moves, domain.PathMove{From: oldDir, To: newDir})
	}
	return plan, nil
}
