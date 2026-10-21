package forge

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// TS-32-11: After DELETE …/forge/webhook a delivery answers the 404 of an unknown slug, writes no
// row and emits no event, and the rows from before are still listed.
// Requirement: 32-REQ-2.3
func TestIngressIsOffAfterUnset_TS_32_11(t *testing.T) {
	e, em := newIngressEnv(t)
	if outcome := outcomeOf(t, e.deliver("widgets", labeledDelivery("gh-1"))); outcome != "relayed" {
		t.Fatalf("before the unset: outcome = %q; want relayed", outcome)
	}

	if rec := e.send(http.MethodDelete, webhookPath, apiKeyAuth(), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status = %d; want 204\n%s", rec.Code, rec.Body)
	}

	rec := e.deliver("widgets", labeledDelivery("gh-2"))
	unknown := e.deliver("nosuch", labeledDelivery("gh-2"))
	if rec.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() {
		t.Errorf("after the unset: status %d %q; an unknown slug: status %d %q; want the same 404", rec.Code, rec.Body, unknown.Code, unknown.Body)
	}
	if rows := e.deliveryRows(); len(rows) != 1 || rows[0].id != "gh-1" {
		t.Errorf("rows = %+v; want only the one from before the unset", rows)
	}
	labeled := 0
	for _, ev := range em.recorded() {
		if ev.EventType == audit.EventForgeIssueLabeled {
			labeled++
		}
	}
	if labeled != 1 {
		t.Errorf("%d labeled events; want only the one from before the unset", labeled)
	}

	rows, list := listDeliveries(t, e, "widgets", "")
	if rows.Code != http.StatusOK || len(list) != 1 || list[0]["delivery_id"] != "gh-1" {
		t.Errorf("GET deliveries after the unset: status %d, %v; want the earlier row", rows.Code, list)
	}

	// Turning the webhook on again makes the ingress answer again.
	const again = "a-secret-set-again-16+"
	if rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"`+again+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(again, "gh-3", "issues", issuesPayload("labeled", "acme/widgets")))); outcome != "relayed" {
		t.Errorf("after the PUT: outcome = %q; want relayed", outcome)
	}
}
