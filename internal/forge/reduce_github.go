package forge

import (
	"strconv"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// githubMapper returns the mapper of a GitHub event and action, or nil for one hub does not
// relay. GitHub sends the event in X-GitHub-Event and the action in the payload.
func githubMapper(event, action string) mapper {
	if event == "issues" && action == "labeled" {
		return githubIssueLabel(audit.EventForgeIssueLabeled)
	}
	return nil
}

// githubActor is the login of the user whose action GitHub reports.
func githubActor(doc payload) string {
	login, _ := doc.str("sender", "login")
	return login
}

// githubIssueLabel maps an issues delivery that puts a label on an issue or takes one off.
func githubIssueLabel(eventType string) mapper {
	return func(b *builder) ([]audit.HubEvent, error) {
		b.actor = githubActor(b.doc)
		number, ok := b.doc.integer("issue", "number")
		if err := need(ok, "issue.number"); err != nil {
			return nil, err
		}
		label, ok := b.doc.str("label", "name")
		if err := need(ok, "label.name"); err != nil {
			return nil, err
		}
		url, _ := b.doc.str("issue", "html_url")
		return []audit.HubEvent{b.event(eventType, "issue", strconv.Itoa(number), map[string]any{
			"number": number,
			"label":  label,
			"labels": b.doc.names("name", "issue", "labels"),
			"url":    url,
		})}, nil
	}
}
