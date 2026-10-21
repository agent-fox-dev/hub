package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// forgeDeliveryEvents are the constants of the events the forge webhook ingress relays and
// of the two it records for the webhook secret, with the event type each stands for.
var forgeDeliveryEvents = []struct {
	name string
	got  string
	want string
}{
	{"EventForgeIssueOpened", EventForgeIssueOpened, "hub.forge.issue.opened"},
	{"EventForgeIssueEdited", EventForgeIssueEdited, "hub.forge.issue.edited"},
	{"EventForgeIssueClosed", EventForgeIssueClosed, "hub.forge.issue.closed"},
	{"EventForgeIssueReopened", EventForgeIssueReopened, "hub.forge.issue.reopened"},
	{"EventForgeIssueLabeled", EventForgeIssueLabeled, "hub.forge.issue.labeled"},
	{"EventForgeIssueUnlabeled", EventForgeIssueUnlabeled, "hub.forge.issue.unlabeled"},
	{"EventForgeCommentCreated", EventForgeCommentCreated, "hub.forge.comment.created"},
	{"EventForgePullRequestOpened", EventForgePullRequestOpened, "hub.forge.pull_request.opened"},
	{"EventForgePullRequestClosed", EventForgePullRequestClosed, "hub.forge.pull_request.closed"},
	{"EventForgePullRequestMerged", EventForgePullRequestMerged, "hub.forge.pull_request.merged"},
	{"EventForgePullRequestReopened", EventForgePullRequestReopened, "hub.forge.pull_request.reopened"},
	{"EventForgePullRequestSynchronize", EventForgePullRequestSynchronize, "hub.forge.pull_request.synchronize"},
	{"EventForgePush", EventForgePush, "hub.forge.push"},
	{"EventForgeWebhookSet", EventForgeWebhookSet, "hub.forge.webhook.set"},
	{"EventForgeWebhookUnset", EventForgeWebhookUnset, "hub.forge.webhook.unset"},
}

// TS-32-46: Each delivery and webhook event type is an exported constant whose value is its
// event type string.
// Requirement: 32-REQ-8.4
func TestForgeDeliveryEventConstants_TS_32_46(t *testing.T) {
	for _, c := range forgeDeliveryEvents {
		if c.got != c.want {
			t.Errorf("%s = %q; want %q", c.name, c.got, c.want)
		}
	}
}

// TS-32-46: Each of the constants has a doc comment in types.go, which starts with its name.
// Requirement: 32-REQ-8.4
func TestForgeDeliveryEventConstantsAreDocumented_TS_32_46(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "types.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse types.go: %v", err)
	}
	docs := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			doc := value.Doc
			if doc == nil && len(gen.Specs) == 1 {
				doc = gen.Doc
			}
			for _, name := range value.Names {
				if doc != nil {
					docs[name.Name] = doc.Text()
				}
			}
		}
	}
	for _, c := range forgeDeliveryEvents {
		doc, declared := docs[c.name]
		switch {
		case !declared || strings.TrimSpace(doc) == "":
			t.Errorf("%s has no doc comment in types.go", c.name)
		case !strings.HasPrefix(doc, c.name+" "):
			t.Errorf("the doc comment of %s starts %q; want it to start with the name", c.name, firstLine(doc))
		}
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
