package forge

import (
	"strconv"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// githubMapper returns the mapper of a GitHub event and action, or nil for one hub does not
// relay. GitHub sends the event in X-GitHub-Event and the action in the payload; a push has none.
func githubMapper(event, action string) mapper {
	switch event {
	case "issues":
		switch action {
		case "opened":
			return githubIssue(audit.EventForgeIssueOpened)
		case "edited":
			return githubIssue(audit.EventForgeIssueEdited)
		case "closed":
			return githubIssue(audit.EventForgeIssueClosed)
		case "reopened":
			return githubIssue(audit.EventForgeIssueReopened)
		case "labeled":
			return githubIssueLabel(audit.EventForgeIssueLabeled)
		case "unlabeled":
			return githubIssueLabel(audit.EventForgeIssueUnlabeled)
		}
	case "issue_comment":
		if action == "created" {
			return githubComment
		}
	case "pull_request":
		switch action {
		case "opened":
			return githubPull(audit.EventForgePullRequestOpened)
		case "reopened":
			return githubPull(audit.EventForgePullRequestReopened)
		case "synchronize":
			return githubPull(audit.EventForgePullRequestSynchronize)
		case "closed":
			return githubPullClosed
		}
	case "push":
		return githubPush
	}
	return nil
}

// githubActor is the login of the user whose action GitHub reports.
func githubActor(doc payload) string {
	login, _ := doc.str("sender", "login")
	return login
}

// githubIssue maps an issues delivery that opens, edits, closes or reopens an issue.
func githubIssue(eventType string) mapper {
	return func(b *builder) ([]audit.HubEvent, error) {
		b.actor = githubActor(b.doc)
		number, ok := b.doc.integer("issue", "number")
		if err := need(ok, "issue.number"); err != nil {
			return nil, err
		}
		issueTitle, _ := b.doc.str("issue", "title")
		url, _ := b.doc.str("issue", "html_url")
		return []audit.HubEvent{b.event(eventType, "issue", strconv.Itoa(number), map[string]any{
			"number": number,
			"title":  title(issueTitle),
			"url":    payloadURL(url),
			"labels": capNames(b.doc.names("name", "issue", "labels")),
		})}, nil
	}
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
			"labels": capNames(b.doc.names("name", "issue", "labels")),
			"url":    payloadURL(url),
		})}, nil
	}
}

// githubComment maps an issue_comment delivery of a new comment. GitHub sends the comments on
// pull requests this way too; the issue then has a pull_request object.
func githubComment(b *builder) ([]audit.HubEvent, error) {
	b.actor = githubActor(b.doc)
	number, ok := b.doc.integer("issue", "number")
	if err := need(ok, "issue.number"); err != nil {
		return nil, err
	}
	commentURL, _ := b.doc.str("comment", "html_url")
	return []audit.HubEvent{b.event(audit.EventForgeCommentCreated, "comment", strconv.Itoa(number), map[string]any{
		"number":       number,
		"comment_url":  payloadURL(commentURL),
		"pull_request": b.doc.at("issue", "pull_request") != nil,
	})}, nil
}

// githubPull maps a pull_request delivery that opens, reopens or updates a pull request.
func githubPull(eventType string) mapper {
	return func(b *builder) ([]audit.HubEvent, error) {
		return githubPullEvent(b, eventType)
	}
}

// githubPullClosed maps a pull_request delivery that closes a pull request: a merged one is
// merged and never closed.
func githubPullClosed(b *builder) ([]audit.HubEvent, error) {
	if merged, _ := b.doc.at("pull_request", "merged").(bool); merged {
		return githubPullEvent(b, audit.EventForgePullRequestMerged)
	}
	return githubPullEvent(b, audit.EventForgePullRequestClosed)
}

// githubPullEvent makes the event of a pull_request delivery.
func githubPullEvent(b *builder, eventType string) ([]audit.HubEvent, error) {
	b.actor = githubActor(b.doc)
	number, ok := b.doc.integer("pull_request", "number")
	if err := need(ok, "pull_request.number"); err != nil {
		return nil, err
	}
	url, _ := b.doc.str("pull_request", "html_url")
	head, _ := b.doc.str("pull_request", "head", "ref")
	base, _ := b.doc.str("pull_request", "base", "ref")
	merged, _ := b.doc.at("pull_request", "merged").(bool)
	return []audit.HubEvent{b.event(eventType, "pull_request", strconv.Itoa(number), map[string]any{
		"number": number,
		"url":    payloadURL(url),
		"head":   head,
		"base":   base,
		"merged": merged,
	})}, nil
}

// githubPush maps a push delivery. Its resource is the repository and its id the ref.
func githubPush(b *builder) ([]audit.HubEvent, error) {
	b.actor = githubActor(b.doc)
	ref, ok := b.doc.str("ref")
	if err := need(ok && ref != "", "ref"); err != nil {
		return nil, err
	}
	before, _ := b.doc.str("before")
	after, _ := b.doc.str("after")
	return []audit.HubEvent{b.event(audit.EventForgePush, "repository", ref, map[string]any{
		"ref":    ref,
		"before": before,
		"after":  after,
	})}, nil
}
