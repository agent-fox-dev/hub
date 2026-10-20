package forge

import (
	"context"

	"github.com/agent-fox-dev/aflib/issuex"
)

// The issuex surface the write routes build on or go around. The spec could only assume
// these signatures; pinning them here verifies the real ones at compile time, and a change
// in aflib breaks the build of this package.
//
// The write routes call CreateIssue, AddComment (for an issue and for a pull request),
// AddLabels, RemoveLabel (on GitLab) and CreatePullRequest. What issuex cannot do the way
// the routes need, the hub does itself (write_raw.go; docs/errata/31_forge_writes_divergences.md):
// UpdateIssue cannot send an empty body or an empty set, CreateLabel returns no label and
// there is no lookup of one, RemoveLabel on GitHub hides the forge's 404, CloseIssue returns
// no issue, and PostReviewComment returns no URL. They are pinned all the same, so that
// a change that gives issuex what is missing shows here.
var (
	_ func(issuex.Client, context.Context, issuex.Repo, issuex.CreateIssueRequest) (issuex.Issue, error)             = issuex.Client.CreateIssue
	_ func(issuex.Client, context.Context, issuex.IssueRef) (issuex.IssueThread, error)                              = issuex.Client.ReadIssue
	_ func(issuex.Client, context.Context, issuex.IssueRef, issuex.UpdateIssueRequest) (issuex.Issue, error)         = issuex.Client.UpdateIssue
	_ func(issuex.Client, context.Context, issuex.IssueRef, string) error                                            = issuex.Client.CloseIssue
	_ func(issuex.Client, context.Context, issuex.IssueRef, string) (string, error)                                  = issuex.Client.AddComment
	_ func(issuex.Client, context.Context, issuex.IssueRef, []string) error                                          = issuex.Client.AddLabels
	_ func(issuex.Client, context.Context, issuex.IssueRef, string) error                                            = issuex.Client.RemoveLabel
	_ func(issuex.Client, context.Context, issuex.Repo, issuex.Label) error                                          = issuex.Client.CreateLabel
	_ func(issuex.Client, context.Context, issuex.Repo, issuex.CreatePullRequestRequest) (issuex.PullRequest, error) = issuex.Client.CreatePullRequest
	_ func(issuex.Client, context.Context, issuex.IssueRef, string) error                                            = issuex.Client.PostReviewComment

	// The shapes the write routes read and build.
	_ = issuex.CreateIssueRequest{Title: "", Body: "", Labels: nil, Assignees: nil}
	_ = issuex.UpdateIssueRequest{Title: "", Body: "", State: "", Labels: nil, Assignees: nil}
	_ = issuex.CreatePullRequestRequest{Title: "", Body: "", Head: "", Base: "", Draft: false}
	_ = issuex.Label{Name: "", Color: "", Description: ""}
	_ = issuex.IssueRef{Repo: issuex.Repo{}, Number: 0, IsPullRequest: false}
	_ = issuex.HTTPError{Method: "", Path: "", Status: 0, Message: "", RetryAfterHeader: "", RateLimitReset: "", RateLimitRemaining: ""}
)
