package forge

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
)

// randomSecret makes a valid webhook secret: n characters from '!' to '~'.
func randomSecret(r *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('!' + r.Intn('~'-'!'+1))
	}
	return string(b)
}

// TS-32-14: The secret appears in no response but the answer to the PUT that made it, and in no log
// line, audit event or row of the delivery log.
// Requirement: 32-REQ-2.7
func TestSecretNeverAppearsOutsideThePut_TS_32_14(t *testing.T) {
	r := rand.New(rand.NewSource(14))
	for i := 0; i < 40; i++ {
		generated := i%2 == 0
		secret := randomSecret(r, 16+r.Intn(240))
		if i%5 == 0 {
			secret = randomSecret(r, 16) // the shortest
		}
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			logs := captureLog(t)
			em := &recordingEmitter{}
			e := newEnv(t, withEmitter(em))
			addWorkspace(t, e.db, "widgets")

			body := `{"secret":` + quoteJSON(secret) + `}`
			if generated {
				body = ``
			}
			put := e.send(http.MethodPut, webhookPath, apiKeyAuth(), body)
			if put.Code != http.StatusOK {
				t.Fatalf("PUT: status = %d; want 200\n%s", put.Code, put.Body)
			}
			if generated {
				secret, _ = webhookAnswer(t, put)["secret"].(string)
				if secret == "" {
					t.Fatalf("the PUT did not answer with the secret it made: %s", put.Body)
				}
			}

			var others []string // every other response
			note := func(rec interface{ String() string }) { others = append(others, rec.String()) }
			note(e.get("/workspaces/widgets/forge", apiKeyAuth()).Body)
			note(e.get("/workspaces/widgets/forge/deliveries", apiKeyAuth()).Body)

			payload := issuesPayload("labeled", "acme/widgets")
			for _, d := range []signedDelivery{
				githubDelivery(secret, "gh-ok", "issues", payload),                                          // accepted
				githubDelivery(secret, "gh-ok", "issues", payload),                                          // a duplicate
				githubDelivery(secret+"x", "gh-bad", "issues", payload),                                     // rejected: wrong key
				githubDelivery(secret, "gh-malformed", "issues", `{"action":"labeled"}`),                    // malformed
				gitlabDelivery(secret, "gl-ok", "Issue Hook", gitlabIssuePayload("update", "acme/widgets")), // accepted
				gitlabDelivery(secret+"x", "gl-bad", "Issue Hook", "{}"),                                    // rejected: wrong token
				githubDelivery(secret, "", "issues", payload),                                               // no id
			} {
				note(e.deliver("widgets", d).Body)
			}
			note(e.deliver("nosuch", githubDelivery(secret, "gh-ok", "issues", payload)).Body)
			// A PUT that is refused answers without the value it was given.
			note(e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":`+quoteJSON(secret+" ")+`}`).Body)
			note(e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":`+quoteJSON(secret+strings.Repeat("x", 256))+`}`).Body) // too long
			note(e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":`+quoteJSON(secret+"\u00e9")+`}`).Body)                 // not ASCII
			note(e.send(http.MethodPut, webhookPath, patAuth("forge:read"), `{"secret":`+quoteJSON(secret)+`}`).Body)

			// A store that fails with an error that holds the value: the answer and the log do not.
			broken := newEnv(t, withFakeSecrets(&fakeSecrets{updateErr: fmt.Errorf("update of %s failed", secret)}))
			addWorkspace(t, broken.db, "widgets")
			note(broken.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":`+quoteJSON(secret)+`}`).Body)
			note(e.get("/workspaces/widgets/forge", apiKeyAuth()).Body)
			note(e.get("/workspaces/widgets/forge/deliveries", apiKeyAuth()).Body)
			del := e.send(http.MethodDelete, webhookPath, apiKeyAuth(), "")
			note(del.Body)
			note(e.deliver("widgets", githubDelivery(secret, "gh-after", "issues", payload)).Body)

			events, err := json.Marshal(em.recorded())
			if err != nil {
				t.Fatal(err)
			}
			var rows []string
			for _, row := range e.deliveryRows() {
				rows = append(rows, fmt.Sprintf("%+v", row))
			}

			for name, sink := range map[string]string{
				"a response":             strings.Join(others, "\n"),
				"the log":                logs.String(),
				"an audit event":         string(events),
				"the delivery log":       strings.Join(rows, "\n"),
				"the answer to a DELETE": del.Body.String(),
			} {
				if strings.Contains(sink, secret) {
					t.Errorf("the secret is in %s:\n%s", name, truncate(sink, 600))
				}
			}
			if generated != strings.Contains(put.Body.String(), secret) {
				t.Errorf("the PUT answer %s: generated %v; the secret is in it only when the hub made it", put.Body, generated)
			}
			if len(others) == 0 || len(em.recorded()) == 0 || len(rows) == 0 || logs.String() == "" {
				t.Errorf("the run left %d responses, %d events, %d rows and a log of %d bytes; want some of each, or it proves nothing",
					len(others), len(em.recorded()), len(rows), len(logs.String()))
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
