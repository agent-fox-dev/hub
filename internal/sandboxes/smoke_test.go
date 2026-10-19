package sandboxes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/cli"
)

// smokeSection is a valid [sandbox] section with two profiles whose env value
// comes from the environment and whose secrets are named.
const smokeSection = `
[sandbox]
provider = "podman"
max_ttl  = "2h"

[[sandbox.profiles]]
name             = "zeta"
image            = "registry.example/zeta:1"
ttl              = "30m"
secrets          = ["GIT_PAT", "ANTHROPIC_API_KEY"]
optional_secrets = ["GH_TOKEN"]

[sandbox.profiles.env]
API_TOKEN = "${SBXT_SMOKE_TOKEN}"
LANG      = "C.UTF-8"

[[sandbox.profiles]]
name    = "alpha"
default = true
image   = "registry.example/alpha:1"
ttl     = "1h"
`

// TS-25-55 (smoke): A valid sandbox section starts the service and a PAT with
// sandboxes:read lists the profiles.
// Path: 25-PATH-1. Requirements: 25-REQ-5.1, 25-REQ-7.4
func TestSmoke_ValidSectionServesProfiles_TS_25_55(t *testing.T) {
	t.Setenv("SBXT_SMOKE_TOKEN", "smoke-env-value-1")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeConfigFile(t, dir, smokeSection)
	path := apikit.ConfigPath()

	// The start-up call: load, validate, build, probe, check and log.
	logger, buf := newCapturedLogger()
	startFake := newFakeProvider()
	factory := &recordingFactory{provider: startFake}
	if _, err := Init(logger, path, factory.build); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	records := logRecords(t, buf)
	if len(records) != 1 || !slices.Equal(attributeKeys(records[0]), []string{"profiles", "provider"}) {
		t.Fatalf("start-up log = %s; want one info line with the provider and the profile names only", buf)
	}
	if factory.options.Binary != "podman" || startFake.reapCalls != 1 {
		t.Errorf("provider built with %+v and probed %d times; want podman and one probe", factory.options, startFake.reapCalls)
	}

	// The same section through the loader, the validator and the service, with
	// the real CheckSpec counted.
	fake := newFakeProvider()
	var checks int
	counting := func(caps sandbox.Capabilities, spec sandbox.Spec) (sandbox.Spec, error) {
		checks++
		return sandbox.CheckSpec(caps, spec)
	}
	svc, err := newService(requireValid(t, mustLoad(t, path)), fake, counting)
	if err != nil {
		t.Fatalf("newService returned error: %v", err)
	}
	if fake.reapCalls != 1 || checks != 2 {
		t.Errorf("probes = %d, CheckSpec calls = %d; want 1 and one per profile (2)", fake.reapCalls, checks)
	}

	rec := getProfiles(t, newHandlerEcho(svc), patAuth("sandboxes:read"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200: %s", rec.Code, rec.Body)
	}
	var body struct {
		Profiles []struct {
			Name    string   `json:"name"`
			Secrets []string `json:"secrets"`
			EnvKeys []string `json:"env_keys"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if len(body.Profiles) != 2 || body.Profiles[0].Name != "alpha" || body.Profiles[1].Name != "zeta" {
		t.Fatalf("profiles = %+v; want alpha, zeta in that order", body.Profiles)
	}
	zeta := body.Profiles[1]
	if !slices.Equal(zeta.Secrets, []string{"ANTHROPIC_API_KEY", "GIT_PAT"}) || !slices.Equal(zeta.EnvKeys, []string{"API_TOKEN", "LANG"}) {
		t.Errorf("zeta = %+v; want its sorted secret names and env keys", zeta)
	}
	for _, value := range []string{"smoke-env-value-1", "C.UTF-8"} {
		if strings.Contains(rec.Body.String(), value) {
			t.Errorf("the body contains the env value %q", value)
		}
	}
}

// bearerAuthMiddleware treats the bearer credential key as an API key and
// every other request as unauthenticated.
func bearerAuthMiddleware(key string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get("Authorization") == "Bearer "+key {
				apikit.SetAuthInfo(c, &apikit.AuthInfo{CredentialType: "api_key", UserID: "user-1", KeyID: "key-1"})
			}
			return next(c)
		}
	}
}

// TS-25-56 (smoke): afc sandbox profiles prints the profiles served by the
// real handler, through the real command tree and apikit's client.
// Path: 25-PATH-2. Requirement: 25-REQ-9.1
func TestSmoke_AfcPrintsServedProfiles_TS_25_56(t *testing.T) {
	svc, _ := newTestService(t, configWith(
		profileWith("zeta", func(p *ProfileConfig) { p.Secrets = []string{"GIT_PAT"} }),
		validProfile("alpha"),
	))
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(bearerAuthMiddleware("smoke-api-key"))
	RegisterRoutes(api, svc)
	hub := httptest.NewServer(e)
	t.Cleanup(hub.Close)

	// The handler's own answer, for comparison.
	req, _ := http.NewRequest(http.MethodGet, hub.URL+"/api/v1/sandbox-profiles", nil)
	req.Header.Set("Authorization", "Bearer smoke-api-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("asking the hub directly: %v", err)
	}
	defer resp.Body.Close()
	var want any
	if err := json.NewDecoder(resp.Body).Decode(&want); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("direct request: status %d, decode error %v", resp.StatusCode, err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ak"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ak", "config.toml"), []byte("endpoint_url = \"\"\nuser_id = \"\"\napi_key = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := cli.BuildRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--endpoint-url", hub.URL, "--api-key", "smoke-api-key", "sandbox", "profiles"})
	err = root.Execute()
	if code := cli.ExitCode(err); code != 0 {
		t.Fatalf("afc exit code = %d (%v); want 0\nstdout: %s\nstderr: %s", code, err, stdout.String(), stderr.String())
	}

	dec := json.NewDecoder(&stdout)
	var got any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not a JSON document: %v", err)
	}
	if dec.More() {
		t.Error("stdout holds more than one JSON document")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("afc printed %v; the handler answered %v", got, want)
	}
}

// 25-PATH-3 (smoke): An invalid section refuses start with every failure, an
// unknown key included, each naming the profile and the field, and without
// contacting the provider.
// Requirements: 25-REQ-1.4, 25-REQ-4.6
func TestSmoke_InvalidSectionReportsEveryFailure_PATH_3(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider = "podman"
max_ttl  = "1h"

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "30m"

[[sandbox.profiles]]
name      = "dev"
image     = "registry.example/dev2:1"
ttl       = "30m"
memroy_mb = 512

[[sandbox.profiles]]
name  = "slow"
image = "registry.example/slow:1"
ttl   = "3h"
`)
	logger, buf := newCapturedLogger()
	fake := newFakeProvider()
	factory := &recordingFactory{provider: fake}

	svc, err := Init(logger, path, factory.build)
	if svc != nil || err == nil {
		t.Fatalf("Init = (%v, %v); want a refusal", svc, err)
	}
	requireFailure(t, err, "sandbox.profiles[1].memroy_mb")
	requireFailure(t, err, `"dev"`, "name", "already used")
	requireFailure(t, err, `"slow"`, "ttl")
	if factory.calls != 0 || fake.total() != 0 {
		t.Errorf("factory calls = %d, provider calls = %d; want none", factory.calls, fake.total())
	}
	if buf.Len() != 0 {
		t.Errorf("a refused start logged: %s", buf)
	}
}

// newRealHub mounts the sandbox routes behind a real apikit server (its auth
// middleware, its permission registry with sandboxes.Permissions() and its PAT
// handlers) over a real database holding one user with an API key. It returns
// a function that serves a request in process, and the API key. The config
// file and the XDG variables must already point at a directory the test owns.
func newRealHub(t *testing.T, svc *Service) (do func(method, path, credential, body string) *httptest.ResponseRecorder, apiKey string) {
	t.Helper()
	cfg, err := apikit.LoadConfig()
	if err != nil {
		t.Fatalf("apikit.LoadConfig: %v", err)
	}
	database, err := apikit.OpenDatabase(cfg.Database.Path)
	if err != nil {
		t.Fatalf("apikit.OpenDatabase: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	server := apikit.NewServer(cfg, func() error { return nil })
	if err := server.MountHandlers(database, Permissions()...); err != nil {
		t.Fatalf("MountHandlers: %v", err)
	}
	RegisterRoutes(server.APIGroup(), svc)

	now := apikit.NowUTC()
	if _, err := database.SqlDB.Exec(`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at)
		VALUES ('u1', 'smoke', 'smoke@example.com', '', 'user', 'active', 'github', 'p1', ?, ?)`, now, now); err != nil {
		t.Fatalf("inserting a user: %v", err)
	}
	key, err := apikit.GenerateAPIKey(database.SqlDB, "u1", 0, echo.New().Logger)
	if err != nil {
		t.Fatalf("apikit.GenerateAPIKey: %v", err)
	}

	do = func(method, path, credential, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		rec := httptest.NewRecorder()
		server.Echo().ServeHTTP(rec, req)
		return rec
	}
	return do, key.FullKey
}

// TS-25-58 (smoke): Without a [sandbox] section the start logs that sandboxes
// are disabled, the two scopes are registered with apikit, and the route
// answers 404 sandboxes_disabled to every authenticated caller, behind
// apikit's real auth middleware.
// Path: 25-PATH-4. Requirements: 25-REQ-1.2, 25-REQ-6.1, 25-REQ-7.2
func TestSmoke_DisabledRoute_TS_25_58(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	writeConfigFile(t, dir, "[server]\nport = 0\n\n[database]\npath = \""+filepath.Join(dir, "hub.db")+"\"\n")

	// The start-up wiring of cmd/af-hub for a hub without a [sandbox] section.
	logger, buf := newCapturedLogger()
	svc, err := Init(logger, apikit.ConfigPath(), (&recordingFactory{provider: newFakeProvider()}).build)
	if svc != nil || err != nil {
		t.Fatalf("Init = (%v, %v); want (nil, nil)", svc, err)
	}
	records := logRecords(t, buf)
	if len(records) != 1 || records[0]["level"] != "INFO" || !strings.Contains(records[0]["msg"].(string), "disabled") {
		t.Fatalf("start-up log = %s; want one info line saying sandboxes are disabled", buf)
	}

	do, apiKey := newRealHub(t, svc)
	createPAT := func(permissions string) *httptest.ResponseRecorder {
		return do(http.MethodPost, "/api/v1/user/tokens", apiKey, `{"name":"smoke","permissions":`+permissions+`}`)
	}

	// Both scopes are registered with apikit: a PAT can carry them, and an
	// unregistered scope of the same resource is refused.
	rec := createPAT(`["sandboxes:read","sandboxes:write"]`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating a PAT with both sandbox scopes: status %d; want 201: %s", rec.Code, rec.Body)
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Token == "" {
		t.Fatalf("PAT response: %v: %s", err, rec.Body)
	}
	if rec := createPAT(`["sandboxes:delete"]`); rec.Code != http.StatusBadRequest {
		t.Errorf("creating a PAT with an unregistered sandbox scope: status %d; want 400", rec.Code)
	}
	other := createPAT(`["users:read"]`)
	if other.Code != http.StatusCreated {
		t.Fatalf("creating a PAT without sandbox scopes: status %d: %s", other.Code, other.Body)
	}
	var unrelated struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(other.Body.Bytes(), &unrelated)

	want := map[string]any{"error": map[string]any{
		"code":       float64(404),
		"message":    "sandboxes are not enabled on this hub",
		"error_type": "sandboxes_disabled",
	}}
	for name, credential := range map[string]string{
		"API key":                 apiKey,
		"PAT with sandbox scopes": created.Token,
		"PAT without scopes":      unrelated.Token,
	} {
		rec := do(http.MethodGet, "/api/v1/sandbox-profiles", credential, "")
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusNotFound || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %d %s; want 404 sandboxes_disabled", name, rec.Code, rec.Body)
		}
	}
	if rec := do(http.MethodGet, "/api/v1/sandbox-profiles", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a credential: status %d; want 401", rec.Code)
	}
}
