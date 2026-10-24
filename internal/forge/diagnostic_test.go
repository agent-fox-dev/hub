package forge

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// remoteBlock is the origin or the upstream block of GET /forge.
type remoteBlock struct {
	Forge      string  `json:"forge"`
	APIURL     *string `json:"api_url"`
	Repository *struct {
		Host, Owner, Name string
	} `json:"repository"`
	Credential string `json:"credential"`
}

// diagnostic is the body of GET /forge.
type diagnostic struct {
	remoteBlock
	Upstream *remoteBlock `json:"upstream"`
	Webhook  struct {
		Configured     bool    `json:"configured"`
		URL            *string `json:"url"`
		LastDeliveryAt *string `json:"last_delivery_at"`
	} `json:"webhook"`
}

func getDiagnostic(t *testing.T, e *env, slug string) (diagnostic, string) {
	t.Helper()
	rec := e.get("/workspaces/"+slug+"/forge", apiKeyAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d\n%s", slug, rec.Code, rec.Body)
	}
	var d diagnostic
	testutil.Decode(t, rec, &d)
	return d, strings.TrimSpace(rec.Body.String())
}

// GET /forge describes how a read would reach the forge of the origin and, on a
// carry_patch workspace, of the upstream, without contacting it: the forge, the API URL
// as issuex uses it (the default of the host, or the variable without a trailing slash
// and with GitLab's /api/v4), the repository with its full namespace, the kind of the
// credential and never its value; unknown and null where the remote cannot be read or
// classified; and the webhook block.
func TestDiagnostic_DescribesTheRemotes(t *testing.T) {
	counting := &countingTransport{}
	e := newEnv(t, func(cfg *Config) { cfg.Transport = counting })
	e.originAuth = &githttp.BasicAuth{Username: "alice", Password: "pw-s3cret"}
	e.upstreamAuth = &githttp.BasicAuth{Username: patUsername, Password: "up-s3cret"}
	addWorkspace(t, e.db, "widgets")
	addWorkspace(t, e.db, "sub", withGitURL("https://gitlab.example.com/group/subgroup/widgets"))
	e.setVar("sub", "FORGE_API_URL", "https://gitlab.example.com/api/v4/")
	addWorkspace(t, e.db, "lab", withGitURL("https://gitlab.example.com/group/widgets"))
	e.setVar("lab", "FORGE_API_URL", "https://gl.example.com/base")
	addWorkspace(t, e.db, "internal", withGitURL("https://git.internal.example/acme/widgets"))
	addWorkspace(t, e.db, "unparseable", withGitURL("https://user:s3cret@host"))
	addWorkspace(t, e.db, "fork", withGitURL("https://github.com/me/widgets"), carryPatch("https://github.com/up/widgets"))
	e.setVar("fork", "UPSTREAM_FORGE_API_URL", "https://api.github.example/enterprise")
	addWorkspace(t, e.db, "forkunknown", carryPatch("https://git.other.example/up/widgets"))
	e.setVar("forkunknown", "FORGE_API_URL", "https://api.github.example/origin")
	addWorkspace(t, e.db, "leftover", func(w *testutil.Workspace) { w.UpstreamURL = "https://github.com/up/widgets" })

	_, body := getDiagnostic(t, e, "widgets")
	const want = `{"forge":"github","api_url":"https://api.github.com","repository":{"host":"github.com","owner":"acme","name":"widgets"},"credential":"basic","upstream":null,"webhook":{"configured":false,"url":"https://hub.example.com/hooks/forge/widgets","last_delivery_at":null}}`
	if body != want {
		t.Errorf("widgets:\n%s\nwant\n%s", body, want)
	}
	for slug, want := range map[string]struct{ forge, api, owner string }{
		"sub":         {"gitlab", "https://gitlab.example.com/api/v4", "group/subgroup"},
		"lab":         {"gitlab", "https://gl.example.com/base/api/v4", "group"},
		"internal":    {"unknown", "", "acme"},
		"unparseable": {"unknown", "", ""},
	} {
		d, body := getDiagnostic(t, e, slug)
		api := ""
		if d.APIURL != nil {
			api = *d.APIURL
		}
		owner := ""
		if d.Repository != nil {
			owner = d.Repository.Owner
		}
		if d.Forge != want.forge || api != want.api || owner != want.owner || d.Credential != "basic" || d.Upstream != nil {
			t.Errorf("%s: %s; want %+v", slug, body, want)
		}
		if strings.Contains(body, "s3cret") || strings.Contains(body, "alice") {
			t.Errorf("%s: the body holds the credential: %s", slug, body)
		}
	}
	d, _ := getDiagnostic(t, e, "fork")
	if d.Upstream == nil || d.Upstream.Forge != "github" || d.Upstream.Credential != "pat" || d.Upstream.Repository.Owner != "up" ||
		d.Upstream.APIURL == nil || *d.Upstream.APIURL != "https://api.github.example/enterprise" || d.Repository.Owner != "me" {
		t.Errorf("fork: upstream = %+v; want github, pat, up/widgets at the enterprise API", d.Upstream)
	}
	if d, _ := getDiagnostic(t, e, "forkunknown"); d.Upstream == nil || d.Upstream.Forge != "unknown" || d.Upstream.APIURL != nil {
		t.Errorf("forkunknown: upstream = %+v; want unknown, without the origin's API URL", d.Upstream)
	}
	if d, _ := getDiagnostic(t, e, "leftover"); d.Upstream != nil {
		t.Errorf("leftover: upstream = %+v; want null on a standard workspace", d.Upstream)
	}
	if n := counting.n.Load(); n != 0 {
		t.Errorf("the transport saw %d requests; want none", n)
	}
}

// An invalid API URL variable is 422, checked before the remote, and only the variables
// of the blocks that are shown are consulted.
func TestDiagnostic_RejectsAnInvalidAPIURL(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "origin", withGitURL("https://host"))
	e.setVar("origin", "FORGE_API_URL", "http://insecure")
	addWorkspace(t, e.db, "fork", carryPatch("https://github.com/up/widgets"))
	e.setVar("fork", "UPSTREAM_FORGE_API_URL", "ftp://x")
	addWorkspace(t, e.db, "standard")
	e.setVar("standard", "UPSTREAM_FORGE_API_URL", "ftp://x")
	for _, slug := range []string{"origin", "fork"} {
		rec := e.get("/workspaces/"+slug+"/forge", apiKeyAuth())
		if rec.Code != http.StatusUnprocessableEntity || testutil.Error(t, rec).ErrorType != "forge_api_url_invalid" {
			t.Errorf("%s: %d %s; want 422 forge_api_url_invalid", slug, rec.Code, rec.Body)
		}
	}
	if rec := e.get("/workspaces/standard/forge", apiKeyAuth()); rec.Code != http.StatusOK {
		t.Errorf("standard: status = %d; want 200, its upstream variable is not consulted", rec.Code)
	}
}

// The webhook block says whether the workspace-scope secret is set (an empty one is not;
// an org or user one does not count), the ingress URL (null without external_url) and the
// time of the newest delivery of any outcome; the secret itself is never in it.
func TestDiagnostic_WebhookBlock(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	addWorkspace(t, e.db, "b")
	addWorkspace(t, e.db, "blank")
	const secret = "a-secret-of-16-chars"
	if _, err := e.secrets.CreateSecrets("workspace", "a", []secrets.EntryInput{{Key: webhookSecretKey, Value: secret}}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][2]string{{"org", "acme"}, {"user", ownerID}} {
		if _, err := e.secrets.CreateSecrets(scope[0], scope[1], []secrets.EntryInput{{Key: webhookSecretKey, Value: secret}}); err != nil {
			t.Fatal(err)
		}
	}
	testutil.Exec(t, e.db, `INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES ('workspace', 'blank', ?, '', ?, ?)`, webhookSecretKey, at(0), at(0))
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "d-1", outcome: "ignored", receivedAt: at(0)})
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "d-2", outcome: "malformed", receivedAt: at(12)})

	a, body := getDiagnostic(t, e, "a")
	if !a.Webhook.Configured || a.Webhook.URL == nil || *a.Webhook.URL != "https://hub.example.com/hooks/forge/a" || a.Webhook.LastDeliveryAt == nil || *a.Webhook.LastDeliveryAt != at(12) {
		t.Errorf("a: webhook = %+v; want configured, the ingress URL and the 09:12 delivery", a.Webhook)
	}
	if strings.Contains(body, secret) {
		t.Errorf("a: the body holds the secret")
	}
	for _, slug := range []string{"b", "blank"} {
		if d, body := getDiagnostic(t, e, slug); d.Webhook.Configured || d.Webhook.LastDeliveryAt != nil || !strings.Contains(body, `"last_delivery_at":null`) {
			t.Errorf("%s: webhook = %+v; want not configured and no delivery", slug, d.Webhook)
		}
	}
	noURL := newEnv(t, func(cfg *Config) { cfg.ExternalURL = "" })
	addWorkspace(t, noURL.db, "a")
	if _, body := getDiagnostic(t, noURL, "a"); !strings.Contains(body, `"url":null`) {
		t.Errorf("without external_url: %s; want a null url", body)
	}
}

// A failure of the secrets store or the delivery log, or no secrets store, is 500 with no
// webhook block and no cause in the answer.
func TestDiagnostic_WebhookFailuresAre500(t *testing.T) {
	for name, env := range map[string]func() *env{
		"the secrets store fails": func() *env { return newEnv(t, withFakeSecrets(&fakeSecrets{getErr: errors.New("boom: disk gone")})) },
		"there is no secrets store": func() *env { return newEnv(t, func(cfg *Config) { cfg.Secrets = nil }) },
		"the delivery log is gone": func() *env {
			e := newEnv(t)
			testutil.Exec(t, e.db, `DROP TABLE forge_deliveries`)
			return e
		},
	} {
		e := env()
		addWorkspace(t, e.db, "a")
		rec := e.get("/workspaces/a/forge", apiKeyAuth())
		var doc map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &doc)
		if _, has := doc["webhook"]; rec.Code != http.StatusInternalServerError || has || strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%s: %d %s; want 500 without a webhook block or the cause", name, rec.Code, rec.Body)
		}
	}
}
