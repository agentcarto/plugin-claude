package claude

import (
	"strings"

	"github.com/agentcarto/core/common"
)

// This file reads Claude's ask tool, whose calls are conversation (EventQuestion) rather than tool
// traffic.

// askTool is the tool Claude puts a question to the user with.
const askTool = "AskUserQuestion"

// askQuestions reads the questions of an ask call from its input:
// {"questions": [{question, header, multiSelect, options: [{label, description, preview}]}]}.
// It returns nil unless every question has its text and every option its label. A call is read
// whole or not at all: read in part, it would show less than the raw input it takes the place of.
func askQuestions(input any) []common.Question {
	var qs []common.Question
	for _, v := range common.Slice(common.Map(input)["questions"]) {
		m := common.Map(v)
		multi, _ := m["multiSelect"].(bool)
		q := common.Question{Header: common.String(m["header"]), Text: common.String(m["question"]), Multi: multi}
		options, listed := m["options"].([]any)
		if strings.TrimSpace(q.Text) == "" || (m["options"] != nil && !listed) {
			return nil
		}
		for _, ov := range options {
			o := common.Map(ov)
			opt := common.Option{
				Label:       common.String(o["label"]),
				Description: common.String(o["description"]),
				Preview:     common.String(o["preview"]),
			}
			if strings.TrimSpace(opt.Label) == "" {
				return nil
			}
			q.Options = append(q.Options, opt)
		}
		qs = append(qs, q)
	}
	return qs
}
