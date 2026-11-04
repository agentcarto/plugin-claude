package claude

import (
	"strings"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
)

// isForkSourceNotice reports whether a record is the notice Claude Code's /fork writes into the
// new session: a user message holding a <task-notification> whose content is <fork-source>.
// Requiring <fork-source> right after the opening tag keeps an ordinary task notification that
// merely quotes the tag in its result from matching.
func isForkSourceNotice(o, msg map[string]any) bool {
	if common.String(o["type"]) != "user" {
		return false
	}
	texts := []string{common.String(msg["content"])}
	for _, b := range common.Slice(msg["content"]) {
		text, ok := b.(string)
		if !ok {
			text = common.Text(common.Map(b)["text"])
		}
		texts = append(texts, text)
	}
	for _, text := range texts {
		rest, ok := strings.CutPrefix(strings.TrimSpace(text), "<task-notification>")
		if ok && strings.HasPrefix(strings.TrimSpace(rest), "<fork-source>") {
			return true
		}
	}
	return false
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
