package auth_test

import (
	"testing"

	"github.com/agent-fox-dev/hub/internal/auth"
)

// ---------------------------------------------------------------------------
// TS-02-10: In dev mode (no external_url), any http://localhost redirect_uri
// origin is allowed regardless of port; non-localhost hosts are rejected.
// Requirement: 02-REQ-3.1
// ---------------------------------------------------------------------------

func TestAllowlist_DevMode_LocalhostAllowed(t *testing.T) {
	al := auth.NewAllowlist("", true)

	tests := []struct {
		uri     string
		allowed bool
	}{
		{"http://localhost:8080/cb", true},
		{"http://localhost:3000/cb", true},
		{"http://localhost/callback", true},
		{"http://localhost:9999/some/path?foo=bar", true},
		{"http://otherhost:3000/cb", false},
		{"http://example.com:8080/cb", false},
		{"https://localhost:3000/cb", false}, // dev mode requires http
	}

	for _, tt := range tests {
		err := al.IsAllowed(tt.uri)
		if tt.allowed && err != nil {
			t.Errorf("expected %q to be allowed in dev mode, got error: %v", tt.uri, err)
		}
		if !tt.allowed && err == nil {
			t.Errorf("expected %q to be rejected in dev mode, got nil error", tt.uri)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-02-11: In production mode (external_url set), only redirect_uri origins
// exactly matching external_url scheme+host+port are allowed.
// Requirement: 02-REQ-3.2
// ---------------------------------------------------------------------------

func TestAllowlist_ProductionMode_ExactMatch(t *testing.T) {
	al := auth.NewAllowlist("https://app.example.com", false)

	tests := []struct {
		uri     string
		allowed bool
	}{
		{"https://app.example.com/callback", true},
		{"https://app.example.com/some/path", true},
		{"https://app.example.com:8443/callback", false}, // different port
		{"http://app.example.com/callback", false},       // different scheme
		{"https://evil.example.com/callback", false},     // different host
	}

	for _, tt := range tests {
		err := al.IsAllowed(tt.uri)
		if tt.allowed && err != nil {
			t.Errorf("expected %q to be allowed in production mode, got error: %v", tt.uri, err)
		}
		if !tt.allowed && err == nil {
			t.Errorf("expected %q to be rejected in production mode, got nil error", tt.uri)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-02-12: Allowlist check extracts only scheme+host+port from redirect_uri;
// path component is not considered.
// Requirement: 02-REQ-3.3
// ---------------------------------------------------------------------------

func TestAllowlist_PathIgnored(t *testing.T) {
	al := auth.NewAllowlist("https://app.example.com", false)

	// Matching origin with various paths should all be allowed.
	allowed := []string{
		"https://app.example.com/some/path?foo=bar",
		"https://app.example.com/another/path",
		"https://app.example.com/",
		"https://app.example.com",
	}

	for _, uri := range allowed {
		if err := al.IsAllowed(uri); err != nil {
			t.Errorf("expected %q to be allowed (path ignored), got error: %v", uri, err)
		}
	}

	// Different host with same path should be rejected.
	if err := al.IsAllowed("https://evil.example.com/some/path?foo=bar"); err == nil {
		t.Error("expected different host to be rejected even with same path")
	}
}

// ---------------------------------------------------------------------------
// TS-02-E13: redirect_uri with host 'localhost.evil.com' is rejected in dev
// mode; it is not treated as a localhost match.
// Requirement: 02-REQ-3.E1
// ---------------------------------------------------------------------------

func TestAllowlist_DevMode_LocalhostSubstringRejected(t *testing.T) {
	al := auth.NewAllowlist("", true)

	rejected := []string{
		"http://localhost.evil.com:3000/cb",
		"http://mylocalhost:3000/cb",
		"http://notlocalhost:3000/cb",
		"http://localhost.com:3000/cb",
		"http://xyzlocalhost:3000/cb",
	}

	for _, uri := range rejected {
		if err := al.IsAllowed(uri); err == nil {
			t.Errorf("expected %q to be rejected in dev mode (not exact localhost), got nil", uri)
		}
	}
}

// ---------------------------------------------------------------------------
// Production mode without external_url returns error (config error).
// Requirement: 02-REQ-2.E9
// ---------------------------------------------------------------------------

func TestAllowlist_ProductionMode_NoExternalURL(t *testing.T) {
	al := auth.NewAllowlist("", false)

	err := al.IsAllowed("http://localhost:3000/cb")
	if err == nil {
		t.Error("expected error when external_url is empty in production mode")
	}
}
