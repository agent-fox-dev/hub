package sandboxes

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// profilesEcho mounts the profile route over svc, nil for a hub without [sandbox].
func profilesEcho(svc *Service) *echo.Echo {
	e, api := testutil.API()
	RegisterRoutes(api, svc)
	return e
}

func getProfiles(t *testing.T, e *echo.Echo, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Request(t, e, http.MethodGet, "/api/v1/sandbox-profiles", "", auth)
}

func newTestService(t *testing.T, cfg *Config) (*Service, *fakeProvider) {
	t.Helper()
	fake := newFakeProvider()
	svc, err := New(requireValid(t, cfg), fake)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc, fake
}

// Permissions are sandboxes:read and sandboxes:write; a PAT reads with either
// and writes with write only, API keys and admin tokens are not scope-checked,
// and an unknown credential type gets nothing.
func TestAuth_Scopes(t *testing.T) {
	if got := Permissions(); !slices.Equal(got, []apikit.Permission{{Resource: "sandboxes", Action: "read"}, {Resource: "sandboxes", Action: "write"}}) {
		t.Errorf("Permissions() = %v", got)
	}
	for _, tc := range []struct {
		auth        *apikit.AuthInfo
		read, write bool
	}{
		{pat(), false, false},
		{pat("secrets:read", "sandbox:read", "Sandboxes:Read"), false, false},
		{pat("sandboxes:read"), true, false},
		{pat("sandboxes:write"), true, true},
		{pat("workspaces:read", "sandboxes:write", "sandboxes:read"), true, true},
		{ownerKey(), true, true},
		{adminAuth(), true, true},
		{&apikit.AuthInfo{CredentialType: "service", Permissions: []string{"sandboxes:write"}}, false, false},
	} {
		if r, w := canReadSandboxes(tc.auth), canWriteSandboxes(tc.auth); r != tc.read || w != tc.write {
			t.Errorf("%s %v: read %v, write %v; want %v, %v", tc.auth.CredentialType, tc.auth.Permissions, r, w, tc.read, tc.write)
		}
	}
}

// The checks run in order: no credential is 401, a hub without [sandbox] is 404
// sandboxes_disabled for every caller, and only then a PAT without a sandbox
// scope is 403.
func TestProfiles_RefusalsInOrder(t *testing.T) {
	enabled, _ := newTestService(t, validConfig())
	for name, svc := range map[string]*Service{"off": nil, "on": enabled} {
		rec := getProfiles(t, profilesEcho(svc), nil)
		if rec.Code != http.StatusUnauthorized || testutil.Error(t, rec).Code != 401 || errorType(t, rec) == "sandboxes_disabled" {
			t.Errorf("feature %s, no credential: %d %s; want 401", name, rec.Code, rec.Body)
		}
	}
	off := profilesEcho(nil)
	want := map[string]any{"error": map[string]any{"code": float64(404), "message": "sandboxes are not enabled on this hub", "error_type": "sandboxes_disabled"}}
	for _, auth := range []*apikit.AuthInfo{adminAuth(), ownerKey(), pat("secrets:read"), pat("sandboxes:read")} {
		if rec := getProfiles(t, off, auth); rec.Code != http.StatusNotFound || !reflect.DeepEqual(testutil.Object(t, rec), want) {
			t.Errorf("feature off, %s: %d %s; want the sandboxes_disabled body", auth.CredentialType, rec.Code, rec.Body)
		}
	}
	on := profilesEcho(enabled)
	for _, auth := range []*apikit.AuthInfo{pat(), pat("secrets:read")} {
		if rec := getProfiles(t, on, auth); rec.Code != http.StatusForbidden || message(t, rec) == "" {
			t.Errorf("PAT %v: %d %s; want 403 with a message", auth.Permissions, rec.Code, rec.Body)
		}
	}
	for _, auth := range []*apikit.AuthInfo{pat("sandboxes:read"), pat("sandboxes:write"), ownerKey(), adminAuth()} {
		if rec := getProfiles(t, on, auth); rec.Code != http.StatusOK {
			t.Errorf("%s %v: %d; want 200", auth.CredentialType, auth.Permissions, rec.Code)
		}
	}
}

// The listing has every profile sorted by name with the documented fields,
// names sorted and never null, nothing of the deployment, and the same body for
// every caller; it is served from memory after the engine fails.
func TestProfiles_ListsSortedProfilesFromMemory(t *testing.T) {
	cfg := configWith(
		profileWith("zeta", func(p *ProfileConfig) {
			p.Default, p.TTL, p.RequireLandlock = true, "90m", true
			p.CPUs, p.MemoryMB, p.PIDs = 1, 512, 100
			p.Secrets, p.OptionalSecrets, p.Env = []string{"git_pat", "ANTHROPIC"}, []string{"EXTRA"}, map[string]string{"LANG": "C", "API_TOKEN": "env-value-1"}
		}),
		validProfile("alpha"),
	)
	cfg.EngineBinary, cfg.Runtime, cfg.MaxTTL = "/opt/podman-x", "runsc", strPtr("3h")
	svc, fake := newTestService(t, cfg)
	e := profilesEcho(svc)
	fake.reapErr = errors.New("engine down")
	fake.reapCalls, fake.capsCalls = 0, 0

	rec := getProfiles(t, e, adminAuth())
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, content type %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	want := map[string]any{"profiles": []any{
		map[string]any{
			"name": "alpha", "default": false, "image": "registry.example/alpha:1", "network": "none", "ttl_seconds": float64(3600),
			"resources": map[string]any{"cpus": float64(0), "memory_mb": float64(0), "pids": float64(0), "disk_mb": float64(0)},
			"secrets":   []any{}, "optional_secrets": []any{}, "env_keys": []any{}, "require_landlock": false,
		},
		map[string]any{
			"name": "zeta", "default": true, "image": "registry.example/zeta:1", "network": "none", "ttl_seconds": float64(5400),
			"resources": map[string]any{"cpus": float64(1), "memory_mb": float64(512), "pids": float64(100), "disk_mb": float64(0)},
			"secrets":   []any{"ANTHROPIC", "git_pat"}, "optional_secrets": []any{"EXTRA"}, "env_keys": []any{"API_TOKEN", "LANG"}, "require_landlock": true,
		},
	}}
	if got := testutil.Object(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("body = %v\nwant   %v", got, want)
	}
	if !regexp.MustCompile(`"ttl_seconds":5400[,}]`).Match(rec.Body.Bytes()) {
		t.Errorf("ttl_seconds is not the integer 5400: %s", rec.Body)
	}
	for _, hidden := range []string{"podman-x", "runsc", "max_ttl", "3h", "provider", "env-value-1"} {
		if strings.Contains(rec.Body.String(), hidden) {
			t.Errorf("the listing holds %q", hidden)
		}
	}
	if fake.total() != 0 {
		t.Errorf("the listing made %d provider calls", fake.total())
	}
	for _, auth := range []*apikit.AuthInfo{ownerKey(), pat("sandboxes:read"), testutil.PAT("user-2", "sandboxes:write"), testutil.User("")} {
		if other := getProfiles(t, e, auth); other.Code != http.StatusOK || !bytes.Equal(other.Body.Bytes(), rec.Body.Bytes()) {
			t.Errorf("%s %q got %d %s; want the same listing", auth.CredentialType, auth.UserID, other.Code, other.Body)
		}
	}
}
