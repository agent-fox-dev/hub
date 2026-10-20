package forge

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// rejectionInputs is what it takes to make one of the nine routes refuse a request for each
// of the reasons that need something of the route: a request it finds invalid, and one with
// a body-like field over GitHub's limit.
type rejectionInputs struct {
	route writeRouteCase
	// invalidBody is a body the route refuses with 400; invalidPath replaces the route's
	// path when it is the path that is invalid.
	invalidBody string
	invalidPath string
	// oversize is a request body over the limit; empty for a route with no body-like field.
	oversize string
}

func rejectionTable() []rejectionInputs {
	over := jsonString(strings.Repeat("x", 65537))
	return []rejectionInputs{
		{writeRouteCases[0], `{}`, "", `{"title":"t","body":` + over + `}`},
		{writeRouteCases[1], `{}`, "", `{"body":` + over + `}`},
		{writeRouteCases[2], `{"comment":5}`, "", `{"comment":` + over + `}`},
		{writeRouteCases[3], `{}`, "", `{"body":` + over + `}`},
		{writeRouteCases[4], `{}`, "", ""},
		{writeRouteCases[5], "", "/workspaces/%s/issues/0/labels/af:fix", ""},
		{writeRouteCases[6], `{}`, "", ""},
		{writeRouteCases[7], `{}`, "", `{"title":"t","head":"h","base":"main","body":` + over + `}`},
		{writeRouteCases[8], `{}`, "", `{"body":` + over + `}`},
	}
}

// TS-31-7: Any refusal hub makes on its own, for any write route, sends nothing to the forge
// and records no event: no credential, no forge:write, an unknown credential type, a
// workspace that is not there or not the caller's, one that is not active, an invalid
// request, a target the workspace does not have, a forge hub cannot tell, a credential it
// cannot read, a body over the limit.
// Requirement: 31-REQ-1.7
func TestHubSideRejectionsSendNothingAndRecordNothing_TS_31_7(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(githubWrites())
	addWorkspace(t, e.db, "retired", func(r *workspaceRow) { r.status = "archived" })
	addWorkspace(t, e.db, "theirs", func(r *workspaceRow) { r.owner = otherID })
	addWorkspace(t, e.db, "unknown", func(r *workspaceRow) { r.gitURL = "https://git.internal.example/acme/widgets" })

	broken := newWriteEnv(t, withEmitter(em)) // a credential store that fails
	broken.stub.setHandler(githubWrites())
	broken.originErr = errors.New("secrets store is down")

	service := &apikit.AuthInfo{UserID: ownerID, CredentialType: "service", Permissions: []string{"forge:write"}}
	for _, in := range rejectionTable() {
		route := in.route
		classes := []struct {
			name string
			env  *env
			want int
			send func() int
		}{
			{"no credential", e, 401, func() int { return e.sendRoute(route, "widgets", nil).Code }},
			{"no forge:write", e, 403, func() int { return e.sendRoute(route, "widgets", patAuth("forge:read")).Code }},
			{"an unknown credential type", e, 403, func() int { return e.sendRoute(route, "widgets", service).Code }},
			{"another user's workspace", e, 404, func() int { return e.sendRoute(route, "theirs", apiKeyAuth()).Code }},
			{"a workspace that is not there", e, 404, func() int { return e.sendRoute(route, "ghost", apiKeyAuth()).Code }},
			{"a workspace that is not active", e, 400, func() int { return e.sendRoute(route, "retired", apiKeyAuth()).Code }},
			{"an invalid request", e, 400, func() int {
				path := route.url("widgets")
				if in.invalidPath != "" {
					path = strings.Replace(in.invalidPath, "%s", "widgets", 1)
				}
				return e.send(route.method, path, apiKeyAuth(), in.invalidBody).Code
			}},
			{"a target the workspace does not have", e, 400, func() int {
				return e.send(route.method, route.url("widgets")+"?target=upstream", apiKeyAuth(), route.body).Code
			}},
			{"a forge hub cannot tell", e, 422, func() int { return e.sendRoute(route, "unknown", apiKeyAuth()).Code }},
			{"a credential hub cannot read", broken, 500, func() int { return broken.sendRoute(route, "widgets", apiKeyAuth()).Code }},
		}
		if in.oversize != "" {
			classes = append(classes, struct {
				name string
				env  *env
				want int
				send func() int
			}{"a body over the limit", e, 413, func() int { return e.send(route.method, route.url("widgets"), apiKeyAuth(), in.oversize).Code }})
		}

		for _, class := range classes {
			stubBefore, brokenBefore, eventsBefore := e.stub.count(), broken.stub.count(), len(em.recorded())
			if got := class.send(); got != class.want {
				t.Errorf("%s with %s: status = %d; want %d", route.name, class.name, got, class.want)
			}
			if e.stub.count() != stubBefore || broken.stub.count() != brokenBefore {
				t.Errorf("%s with %s: the forge received a request", route.name, class.name)
			}
			if len(em.recorded()) != eventsBefore {
				t.Errorf("%s with %s: an event was recorded", route.name, class.name)
			}
		}
	}

	// The control: the same routes, valid, on the same environment, do send and do record.
	sent, recorded := e.stub.count(), len(em.recorded())
	for _, route := range writeRouteCases {
		if rec := e.sendRoute(route, "widgets", apiKeyAuth()); rec.Code >= 300 {
			t.Errorf("%s: status = %d; want it to succeed", route.name, rec.Code)
		}
	}
	if e.stub.count() <= sent || len(em.recorded())-recorded != len(writeRouteCases) {
		t.Errorf("valid requests sent %d and recorded %d; want requests and %d events", e.stub.count()-sent, len(em.recorded())-recorded, len(writeRouteCases))
	}
	_ = http.StatusOK
}
