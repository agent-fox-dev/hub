package forge

import (
	"context"

	"github.com/agent-fox-dev/aflib/issuex"
)

// The issuex surface the write routes build on. The spec could only assume these
// signatures; pinning them here verifies the real ones at compile time, and a change in
// aflib breaks the build of this package.
//
// What issuex does not offer, and the write routes therefore do themselves, is listed in
// docs/errata/31_forge_writes_divergences.md: a label lookup, a label create that returns
// the label, an issue update that can send an empty body or an empty set, and a label
// removal that reports the forge's 404 on GitHub.
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
