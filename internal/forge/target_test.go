package forge

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TS-30-13: target=upstream without a carry_patch upstream answers 400
// workspace_mode_mismatch and nothing is sent to the forge.
// Requirement: 30-REQ-3.3
func TestUpstreamTargetWithoutUpstreamIsRefused_TS_30_13(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "standard")
	addWorkspace(t, e.db, "nullmode", func(r *workspaceRow) { r.mode = nil })
	addWorkspace(t, e.db, "noupstream", func(r *workspaceRow) { r.mode = ptr("carry_patch") })
	addWorkspace(t, e.db, "emptyupstream", carryPatch(""))
	// A standard workspace that has an upstream_url left over is still not a fork.
	addWorkspace(t, e.db, "leftover", func(r *workspaceRow) { r.upstream = ptr("https://github.com/up/widgets") })

	for _, r := range forgeRoutes {
		if !r.takesTarget {
			continue
		}
		for _, slug := range []string{"standard", "nullmode", "noupstream", "emptyupstream", "leftover"} {
			t.Run(r.name+"/"+slug, func(t *testing.T) {
				rec := e.get(r.url(slug)+"?target=upstream", apiKeyAuth())
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d; want 400\n%s", rec.Code, rec.Body)
				}
				if got := decodeError(t, rec).ErrorType; got != "workspace_mode_mismatch" {
					t.Errorf("error_type = %q; want workspace_mode_mismatch", got)
				}
			})
		}
	}
	assertNothingLookedUp(t, e)
}

// TS-30-14: An unknown or empty target answers 400 listing origin and upstream.
// Requirement: 30-REQ-3.4
func TestInvalidTargetIsRefused_TS_30_14(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets", carryPatch("https://github.com/up/widgets"))

	for _, r := range forgeRoutes {
		if !r.takesTarget {
			continue
		}
		for _, query := range []string{"?target=bogus", "?target=", "?target=ORIGIN", "?target=origin&target=upstream", "?target=upstream%20"} {
			t.Run(r.name+query, func(t *testing.T) {
				rec := e.get(r.url("widgets")+query, apiKeyAuth())
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d; want 400\n%s", rec.Code, rec.Body)
				}
				msg := decodeError(t, rec).Message
				if !strings.Contains(msg, "origin") || !strings.Contains(msg, "upstream") {
					t.Errorf("message %q does not list origin and upstream", msg)
				}
			})
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}

// The diagnostic does not take a target, so it does not refuse one.
// Requirement: 30-REQ-3.4
func TestDiagnosticIgnoresTarget(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets")
	rec := e.get("/workspaces/widgets/forge?target=bogus", apiKeyAuth())
	if rec.Code == http.StatusBadRequest {
		t.Errorf("GET /forge?target=bogus answered 400: %s", rec.Body)
	}
}

// resolveTarget accepts an absent target and origin everywhere, and upstream only
// for a carry_patch workspace with an upstream_url.
// Requirement: 30-REQ-3.1, 30-REQ-3.2
func TestResolveTarget(t *testing.T) {
	fork := workspaceRemotes{gitURL: "https://github.com/me/widgets", mode: "carry_patch", upstreamURL: "https://github.com/up/widgets"}
	standard := workspaceRemotes{gitURL: "https://github.com/me/widgets", mode: "standard"}

	cases := []struct {
		name  string
		query url.Values
		ws    workspaceRemotes
		want  target
	}{
		{"absent on a standard workspace", url.Values{}, standard, targetOrigin},
		{"origin on a standard workspace", url.Values{"target": {"origin"}}, standard, targetOrigin},
		{"absent on a fork", url.Values{}, fork, targetOrigin},
		{"origin on a fork", url.Values{"target": {"origin"}}, fork, targetOrigin},
		{"upstream on a fork", url.Values{"target": {"upstream"}}, fork, targetUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, refusal := resolveTarget(tc.query, tc.ws)
			if refusal != nil {
				t.Fatalf("resolveTarget refused: %+v", refusal)
			}
			if got != tc.want {
				t.Errorf("target = %q; want %q", got, tc.want)
			}
		})
	}
}
