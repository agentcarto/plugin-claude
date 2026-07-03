package claude

import (
	"regexp"
	"strings"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
)

var commandRE = regexp.MustCompile(`<command-name>\s*([^<]+?)\s*</command-name>`)
var bashInputRE = regexp.MustCompile(`(?s)^<bash-input>\s*(.*?)\s*</bash-input>`)

// pseudoPromptPrefixes lists Claude Code's system-injected wrappers: text
// beginning with one of these is not a genuine user prompt. Compared
// case-insensitively (older versions logged a bare "Caveat: ..." preamble).
var pseudoPromptPrefixes = []string{
	"<command-name>", "<command-message>", "<command-args>",
	"<local-command-stdout>", "<local-command-stderr>",
	"<system-reminder>",
	"<bash-input>", "<bash-stdout>", "<bash-stderr>",
	"<task-notification>", "<local-command-caveat>", "caveat:",
	"[request interrupted",
}

// isPseudoPrompt reports whether s is not a genuine user prompt but rather
// empty text, a system-injected block, or a short slash command on a single line.
func isPseudoPrompt(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	low := strings.ToLower(s)
	for _, p := range pseudoPromptPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return common.IsBareSlashCommand(s)
}

// promptText returns the cleaned, whitespace-folded genuine prompt in text,
// or "" when the message is system-injected.
func promptText(text string) string {
	t := stripForkBoilerplate(text)
	if isPseudoPrompt(t) {
		return ""
	}
	return strings.Join(strings.Fields(t), " ")
}

// commandLabel returns the normalized label of a user-issued command: a slash
// command recorded as <command-name> ("/verify"), or a "!"-prefixed shell
// command recorded as <bash-input> ("! ls -la"). /clear only wipes the screen
// and must not open a turn or title, so it yields "".
func commandLabel(text string) string {
	if m := commandRE.FindStringSubmatch(text); len(m) > 1 {
		name := strings.TrimSpace(m[1])
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		if name == "/clear" {
			return ""
		}
		return name
	}
	if m := bashInputRE.FindStringSubmatch(strings.TrimSpace(text)); len(m) > 1 && m[1] != "" {
		return "! " + strings.Join(strings.Fields(m[1]), " ")
	}
	return ""
}

// annotate fills the normalized display fields, in place: Prompt/Command on
// user events (compact summaries are boundaries of their own kind and never
// carry a prompt), EventTask conversion for task notifications, and
// ToolArg/ToolDetail/Changes on tool calls.
func annotate(es []domain.Event) {
	for i := range es {
		switch {
		case es[i].Kind == domain.EventToolCall:
			annotateTool(&es[i])
		case es[i].Kind == domain.EventUser && es[i].RawType != domain.RawCompactSummary:
			if t, ok := parseTaskNotification(es[i].Text); ok {
				taskEvent(&es[i], t)
				continue
			}
			es[i].Prompt = promptText(es[i].Text)
			es[i].Command = commandLabel(es[i].Text)
		}
	}
}
