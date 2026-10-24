package cli

import "testing"

// Every segment is percent-encoded so a slug or id cannot escape its route.
func TestAPIPath_EscapesEverySegment(t *testing.T) {
	if got := apiPath("workspaces", "a/b", "patches", "x y"); got != "/workspaces/a%2Fb/patches/x%20y" {
		t.Errorf("apiPath = %q", got)
	}
}

// Wildcard routes take a path: its elements are encoded, its slashes kept.
func TestEscapePathKeepSlashes(t *testing.T) {
	for in, want := range map[string]string{"src/a b.go": "src/a%20b.go", "a/b/c": "a/b/c", "x?y": "x%3Fy"} {
		if got := escapePathKeepSlashes(in); got != want {
			t.Errorf("escapePathKeepSlashes(%q) = %q, want %q", in, got, want)
		}
	}
}
