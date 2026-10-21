package forge

import (
	"log/slog"
	"slices"
	"strconv"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// gitlabMapper returns the mapper of a GitLab event and action, or nil for one hub does not
// relay. GitLab sends the event in X-Gitlab-Event and the action in object_attributes; a push has
// none. The confidential hooks are not relayed: what they carry should not reach the audit log
// without a decision of its own.
func gitlabMapper(event, action string) mapper {
	switch event {
	case "Issue Hook":
		switch action {
		case "open":
			return gitlabIssue(audit.EventForgeIssueOpened)
		case "close":
			return gitlabIssue(audit.EventForgeIssueClosed)
		case "reopen":
			return gitlabIssue(audit.EventForgeIssueReopened)
		case "update":
			return gitlabIssueUpdate
		}
	case "Note Hook":
		if action == "" || action == "create" {
			return gitlabNote
		}
	case "Merge Request Hook":
		switch action {
		case "open":
			return gitlabMergeRequest(audit.EventForgePullRequestOpened)
		case "close":
			return gitlabMergeRequest(audit.EventForgePullRequestClosed)
		case "reopen":
			return gitlabMergeRequest(audit.EventForgePullRequestReopened)
		case "merge":
			return gitlabMergeRequest(audit.EventForgePullRequestMerged)
		case "update":
			return gitlabMergeRequestUpdate
		}
	case "Push Hook", "Tag Push Hook":
		return gitlabPush
	}
	return nil
}

// gitlabActor is the login of the user whose action GitLab reports: user.username of the
// hooks that have a user object, user_username of the push hooks.
func gitlabActor(doc payload) string {
	if login, ok := doc.str("user", "username"); ok {
		return login
	}
	login, _ := doc.str("user_username")
	return login
}

// gitlabIssue maps an Issue Hook that opens, closes or reopens an issue. The number of an issue
// is its iid, and its labels are the top-level labels, whose names are their titles.
func gitlabIssue(eventType string) mapper {
	return func(b *builder) ([]audit.HubEvent, error) {
		b.actor = gitlabActor(b.doc)
		number, ok := b.doc.integer("object_attributes", "iid")
		if err := need(ok, "object_attributes.iid"); err != nil {
			return nil, err
		}
		return []audit.HubEvent{gitlabIssueEvent(b, eventType, number)}, nil
	}
}

// gitlabIssueEvent makes the event of an Issue Hook that edits, opens, closes or reopens an issue.
func gitlabIssueEvent(b *builder, eventType string, number int) audit.HubEvent {
	issueTitle, _ := b.doc.str("object_attributes", "title")
	url, _ := b.doc.str("object_attributes", "url")
	return b.event(eventType, "issue", strconv.Itoa(number), map[string]any{
		"number": number,
		"title":  title(issueTitle),
		"url":    payloadURL(url),
		"labels": capNames(b.doc.names("title", "labels")),
	})
}

// gitlabIssueUpdate maps an Issue Hook update, which GitLab sends for every change. A change of the
// title or the description is edited; then come one unlabeled for each label taken off and one
// labeled for each label put on, each group sorted by name, like the separate deliveries GitHub
// sends. An update that changes none of them says nothing hub relays.
func gitlabIssueUpdate(b *builder) ([]audit.HubEvent, error) {
	b.actor = gitlabActor(b.doc)
	number, ok := b.doc.integer("object_attributes", "iid")
	if err := need(ok, "object_attributes.iid"); err != nil {
		return nil, err
	}
	var events []audit.HubEvent
	if b.doc.at("changes", "title") != nil || b.doc.at("changes", "description") != nil {
		events = append(events, gitlabIssueEvent(b, audit.EventForgeIssueEdited, number))
	}

	previous := b.doc.names("title", "changes", "labels", "previous")
	current := b.doc.names("title", "changes", "labels", "current")
	url, _ := b.doc.str("object_attributes", "url")
	// The labels the issue has now: the top-level ones, else what the change says.
	labels := b.doc.names("title", "labels")
	if b.doc.at("labels") == nil {
		labels = current
	}
	labels = capNames(labels)
	label := func(eventType, name string) audit.HubEvent {
		return b.event(eventType, "issue", strconv.Itoa(number), map[string]any{
			"number": number,
			"label":  name,
			"labels": labels,
			"url":    payloadURL(url),
		})
	}
	var changes []audit.HubEvent
	for _, name := range difference(previous, current) {
		changes = append(changes, label(audit.EventForgeIssueUnlabeled, name))
	}
	for _, name := range difference(current, previous) {
		changes = append(changes, label(audit.EventForgeIssueLabeled, name))
	}
	if len(changes) > maxLabelEvents {
		slog.Warn("forge: a delivery changed more labels than hub relays, the first are relayed",
			"slug", b.d.slug, "changes", len(changes), "relayed", maxLabelEvents)
		changes = changes[:maxLabelEvents]
	}
	return append(events, changes...), nil
}

// difference returns the names in a that are not in b, sorted and without repeats.
func difference(a, b []string) []string {
	in := make(map[string]bool, len(b)+len(a))
	for _, name := range b {
		in[name] = true
	}
	var out []string
	for _, name := range a {
		if !in[name] {
			in[name] = true
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// gitlabNote maps a Note Hook of a new note on an issue or a merge request, numbered by the iid of
// the issue or merge request. A note on a commit or a snippet says nothing hub relays.
func gitlabNote(b *builder) ([]audit.HubEvent, error) {
	b.actor = gitlabActor(b.doc)
	noteable, _ := b.doc.str("object_attributes", "noteable_type")
	var number int
	var ok, onPull bool
	switch noteable {
	case "Issue":
		number, ok = b.doc.integer("issue", "iid")
	case "MergeRequest":
		number, ok = b.doc.integer("merge_request", "iid")
		onPull = true
	default:
		return nil, nil
	}
	if err := need(ok, "the iid of the noteable"); err != nil {
		return nil, err
	}
	commentURL, _ := b.doc.str("object_attributes", "url")
	return []audit.HubEvent{b.event(audit.EventForgeCommentCreated, "comment", strconv.Itoa(number), map[string]any{
		"number":       number,
		"comment_url":  payloadURL(commentURL),
		"pull_request": onPull,
	})}, nil
}

// gitlabMergeRequest maps a Merge Request Hook that opens, closes, reopens or merges a merge request.
func gitlabMergeRequest(eventType string) mapper {
	return func(b *builder) ([]audit.HubEvent, error) {
		b.actor = gitlabActor(b.doc)
		number, ok := b.doc.integer("object_attributes", "iid")
		if err := need(ok, "object_attributes.iid"); err != nil {
			return nil, err
		}
		url, _ := b.doc.str("object_attributes", "url")
		head, _ := b.doc.str("object_attributes", "source_branch")
		base, _ := b.doc.str("object_attributes", "target_branch")
		state, _ := b.doc.str("object_attributes", "state")
		return []audit.HubEvent{b.event(eventType, "pull_request", strconv.Itoa(number), map[string]any{
			"number": number,
			"url":    payloadURL(url),
			"head":   head,
			"base":   base,
			"merged": eventType == audit.EventForgePullRequestMerged || state == "merged",
		})}, nil
	}
}

// gitlabMergeRequestUpdate maps a Merge Request Hook update: GitLab sends oldrev when commits were
// pushed to the merge request, which is synchronize; any other update says nothing hub relays.
func gitlabMergeRequestUpdate(b *builder) ([]audit.HubEvent, error) {
	if oldrev, _ := b.doc.str("object_attributes", "oldrev"); oldrev == "" {
		return nil, nil
	}
	return gitlabMergeRequest(audit.EventForgePullRequestSynchronize)(b)
}

// gitlabPush maps a Push Hook or a Tag Push Hook. Its resource is the repository and its id the ref.
func gitlabPush(b *builder) ([]audit.HubEvent, error) {
	b.actor = gitlabActor(b.doc)
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
