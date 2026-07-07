package claude

import (
	"testing"

	"github.com/agentcarto/core/domain"
)

func TestPromptText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fix the   bug\nplease", "fix the bug please"},
		{"boiler</fork-boilerplate>  real directive", "real directive"},
		{"<system-reminder>ctx</system-reminder>", ""},
		{"<command-name>/verify</command-name>", ""},
		{"<bash-input>ls</bash-input>", ""},
		{"<task-notification>done", ""},
		{"Caveat: The messages below were generated while running local commands", ""},
		{"[Request interrupted by user]", ""},
		{"/compact", ""},
		{"/compact please keep the design notes because they matter a lot", "/compact please keep the design notes because they matter a lot"},
		{"", ""},
	}
	for _, c := range cases {
		if got := promptText(c.in); got != c.want {
			t.Errorf("promptText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCommandLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<command-name>/verify</command-name>", "/verify"},
		{"<command-message>x</command-message>\n<command-name>fix</command-name>", "/fix"},
		{"<command-message>verify</command-message>\n<command-name>/verify</command-name>\n<command-args>@notes.md</command-args>", "/verify @notes.md"},
		{"<command-name>/verify</command-name>\n<command-args>a\n   b</command-args>", "/verify a b"},
		{"<command-name>/verify</command-name>\n<command-args></command-args>", "/verify"},
		{"<command-name>/clear</command-name>", ""},
		{"<command-name>/clear</command-name><command-args>now</command-args>", ""},
		{"<bash-input>ls   -la</bash-input>", "! ls -la"},
		{"quoting <bash-input>x</bash-input> mid-text", ""},
		{"plain prompt", ""},
	}
	for _, c := range cases {
		if got := commandLabel(c.in); got != c.want {
			t.Errorf("commandLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAnnotateSkipsCompactAndNonUser(t *testing.T) {
	es := []domain.Event{
		{Kind: domain.EventUser, Text: "real prompt"},
		{Kind: domain.EventUser, Text: "summary", RawType: domain.RawCompactSummary},
		{Kind: domain.EventAssistant, Text: "reply"},
		{Kind: domain.EventUser, Text: "<bash-input>make check</bash-input>"},
	}
	annotate(es)
	if es[0].Prompt != "real prompt" || es[0].Command != "" {
		t.Fatalf("es[0]=%+v", es[0])
	}
	if es[1].Prompt != "" || es[2].Prompt != "" {
		t.Fatalf("compact/assistant annotated: %+v %+v", es[1], es[2])
	}
	if es[3].Prompt != "" || es[3].Command != "! make check" {
		t.Fatalf("es[3]=%+v", es[3])
	}
}
