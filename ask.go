package claude

import (
	"strings"

	"github.com/agentcarto/core/common"
)

// This file reads Claude's ask tool, whose calls and results are conversation (EventQuestion,
// EventAnswer) rather than tool traffic.

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

// askResponse reads the result of an ask call as the Text of an EventAnswer: what the user
// answered, or that they turned the questions down. It returns "" for a result that is not the
// user's response (the call failed some other way), which stays a tool result. result is the
// tool_result block, body its text, and rec the record holding it.
func askResponse(rec, result map[string]any, body string) string {
	// The record says how its result came about in fields of its own (toolUseResult,
	// toolDenialKind, userFeedback). Claude Code writes one result to a record; in a record
	// holding several there is no telling which one the fields describe, so they are not read.
	if toolResults(rec) != 1 {
		rec = nil
	}
	if failed, _ := result["is_error"].(bool); failed {
		if common.String(rec["toolDenialKind"]) != "user-rejected" {
			return ""
		}
		// The feedback wraps the answers given so far in Claude Code's own wording, which changes
		// between releases, so it is passed on whole rather than picked apart.
		if feedback := strings.TrimSpace(common.String(rec["userFeedback"])); feedback != "" {
			return feedback
		}
		return "(declined to answer)"
	}
	if answers := common.AnswerText(askAnswers(rec["toolUseResult"])); answers != "" {
		return answers
	}
	return body
}

// askAnswers reads the answers from the account Claude Code keeps of an answered ask call:
// {"questions": [as in the call], "answers": {question text: answer}, "annotations": {question
// text: annotation}}. The answer to a multi-select question is its labels, already joined. It
// returns nil unless these fields are all the account holds, and every answer and annotation is
// readable and belongs to one of the questions: the account can also hold a reply in the user's
// own words, a request for more questions or a timeout, none of which a line per question can
// say, while the result's body says them all.
func askAnswers(account any) []common.Answer {
	a := common.Map(account)
	for field := range a {
		if field != "questions" && field != "answers" && field != "annotations" {
			return nil
		}
	}
	given, ok := a["answers"].(map[string]any)
	if !ok {
		return nil
	}
	annotations, ok := a["annotations"].(map[string]any)
	if !ok && a["annotations"] != nil {
		return nil
	}
	var answers []common.Answer
	read := 0
	for _, q := range askQuestions(a) {
		answer := common.Answer{Header: q.Label()}
		if v, answered := given[q.Text]; answered {
			value, ok := v.(string)
			if !ok {
				return nil
			}
			answer.Values = []string{value}
			read++
		}
		if v, annotated := annotations[q.Text]; annotated {
			note, ok := askNote(v)
			if !ok {
				return nil
			}
			answer.Note = note
			read++
		}
		answers = append(answers, answer)
	}
	if read != len(given)+len(annotations) {
		return nil
	}
	return answers
}

// askNote reads what the user wrote beside a choice from the annotation of a question:
// {"notes": text, "preview": the preview of the option chosen}, either of which may be missing.
// ok is false for an annotation of another shape.
func askNote(annotation any) (note string, ok bool) {
	a, ok := annotation.(map[string]any)
	if !ok {
		return "", false
	}
	if v, noted := a["notes"]; noted {
		note, ok = v.(string)
	}
	return note, ok
}

// toolResults counts the tool_result blocks in a record's message.
func toolResults(rec map[string]any) int {
	n := 0
	for _, b := range common.Slice(common.Map(rec["message"])["content"]) {
		if common.String(common.Map(b)["type"]) == "tool_result" {
			n++
		}
	}
	return n
}
