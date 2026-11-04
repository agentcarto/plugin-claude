package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
)

// forkSourceNotice returns the text of the notice Claude Code's /fork writes into the new session,
// when the record is that notice delivered as a user message.
func forkSourceNotice(o, msg map[string]any) (string, bool) {
	if common.String(o["type"]) != "user" {
		return "", false
	}
	for _, text := range messageTexts(msg) {
		if isForkSourceText(text) {
			return text, true
		}
	}
	return "", false
}

// enqueuedForkSourceNotice returns the text of the notice when the record queues a fork-source
// notice for delivery.
func enqueuedForkSourceNotice(o map[string]any) (string, bool) {
	if common.String(o["type"]) != "queue-operation" || common.String(o["operation"]) != "enqueue" {
		return "", false
	}
	text := common.String(o["content"])
	return text, isForkSourceText(text)
}

// isForkSourceText reports whether text is a fork-source notice: a <task-notification> whose
// content is <fork-source>. Requiring <fork-source> right after the opening tag keeps an ordinary
// task notification that merely quotes the tag in its result from matching.
func isForkSourceText(text string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), "<task-notification>")
	return ok && strings.HasPrefix(strings.TrimSpace(rest), "<fork-source>")
}

var timestampPattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

// sharedHistoryEnd returns the time a fork-source notice gives as the end of the history the
// session shares with its parent, which is the time of the last record copied from it, or the
// zero time when the notice gives none. The notice states it in prose ("The conversation up to
// <time> is shared history"); only the timestamp is taken, so rewording around it does not matter.
func sharedHistoryEnd(notice string) time.Time {
	return common.Time(timestampPattern.FindString(notice))
}

// copyEnd returns the last record a /fork session copied from its parent, given end, the end of
// the history the two share. The copy is the parent's active path, written in order with the
// parent's times before anything else, and everything the session writes itself is later than
// end; so the last record, in the order of nodes, written no later than end ends the copy. Where
// the notice was delivered says nothing about it: Claude Code delivers the notice once the session
// is idle, and a /fork given a prompt has run that prompt's turn below the copy by then. It
// returns "" when end is the zero time or no record qualifies.
func copyEnd(nodes []domain.ConvNode, end time.Time) string {
	if end.IsZero() {
		return ""
	}
	last := ""
	for _, n := range nodes {
		if !n.Timestamp.IsZero() && !n.Timestamp.After(end) {
			last = n.ID
		}
	}
	return last
}

// reattachPastForkNotices points every node whose parent is a dropped fork-source notice at the
// notice's own parent. notices maps a notice uuid to its parent uuid.
func reattachPastForkNotices(nodes []domain.ConvNode, notices map[string]string) {
	for i := range nodes {
		// Bounded by the number of notices so a malformed log whose notices point at each
		// other cannot loop forever.
		for range len(notices) {
			p, ok := notices[nodes[i].Parent]
			if !ok {
				break
			}
			nodes[i].Parent = p
		}
	}
}

// slashForkParent returns the id of the session whose /fork created the session childID (stored
// at path, forked at the node forkAt), or "" when no log beside it ran that /fork. The child's log
// never names its parent; only the parent's log names the child, in the output of the /fork, and
// that log sits in the same project directory and was written after the fork point.
func slashForkParent(ctx context.Context, path, childID string, nodes []domain.ConvNode, forkAt string) string {
	// The output carries the first 8 characters of the new session's id.
	if len(childID) < 8 {
		return ""
	}
	short := childID[:8]
	var since time.Time
	for _, n := range nodes {
		if n.ID == forkAt {
			since = n.Timestamp
			break
		}
	}
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// The lineage is an extra: the session is listed without it rather than not at all.
		return ""
	}
	for _, e := range entries {
		cand := filepath.Join(dir, e.Name())
		if e.IsDir() || filepath.Ext(cand) != ".jsonl" || cand == path {
			continue
		}
		if info, err := e.Info(); err != nil || info.ModTime().Before(since) {
			continue
		}
		if ranSlashForkOf(ctx, cand, short) {
			return common.IDFromPath(cand)
		}
	}
	return ""
}

// ranSlashForkOf reports whether the log at path ran the /fork whose output names short. A session
// copied from that log carries the same record, so copies are passed over: a PlanFork copy marks
// every record with parentSessionId, a /branch copy with forkedFrom, and a /fork copy puts its
// fork-source notice after everything it copied.
func ranSlashForkOf(ctx context.Context, path, short string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	// Lines are read whole, not with a bufio.Scanner, for the reason given in common.JSONLines;
	// only the few lines that mention short or the notice tag are decoded.
	r := bufio.NewReaderSize(f, 64<<10)
	found := false
	for ctx.Err() == nil {
		line, err := r.ReadBytes('\n')
		switch {
		case bytes.Contains(line, []byte(short)) && isOriginalSlashForkOutput(line, short):
			found = true
		case bytes.Contains(line, []byte("<fork-source>")):
			if o, e := common.UnmarshalJSONMap(line); e == nil {
				if _, ok := forkSourceNotice(o, common.Map(o["message"])); ok {
					found = false
				}
			}
		}
		if err != nil {
			return found
		}
	}
	return false
}

// isOriginalSlashForkOutput reports whether line is the output record of a /fork, not copied by
// PlanFork or /branch, that names short as a word of its text.
func isOriginalSlashForkOutput(line []byte, short string) bool {
	var o struct {
		CommandRun struct {
			Command string `json:"command"`
		} `json:"commandRun"`
		Content         string          `json:"content"`
		ParentSessionID string          `json:"parentSessionId"`
		ForkedFrom      json.RawMessage `json:"forkedFrom"`
	}
	if json.Unmarshal(line, &o) != nil || o.CommandRun.Command != "fork" || o.ParentSessionID != "" || o.ForkedFrom != nil {
		return false
	}
	// Matched as a whole word, so the wording around the id in the output does not matter.
	words := strings.FieldsFunc(o.Content, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return slices.Contains(words, short)
}
