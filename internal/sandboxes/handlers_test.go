package sandboxes

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// testAuthMiddleware installs the AuthInfo carried by the X-Test-Auth header,
// the way the other handler tests stand in for apikit's auth middleware. A
// request without the header is unauthenticated.
func testAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if header := c.Request().Header.Get("X-Test-Auth"); header != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(header), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// newHandlerEcho returns an echo server with the sandbox routes on /api/v1,
// behind the test auth middleware. svc is nil for a hub without [sandbox].
func newHandlerEcho(svc *Service) *echo.Echo {
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterRoutes(api, svc)
	return e
}

// getProfiles sends GET /api/v1/sandbox-profiles as auth (unauthenticated
// when nil).
func getProfiles(t *testing.T, e *echo.Echo, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox-profiles", nil)
	if auth != nil {
		header, err := json.Marshal(auth)
		if err != nil {
			t.Fatalf("marshalling auth info: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(header))
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func adminAuth() *apikit.AuthInfo { return &apikit.AuthInfo{CredentialType: "admin_token"} }

func apiKeyAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "api_key", UserID: "user-1", KeyID: "key-1"}
}

func patAuth(perms ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: "user-1", KeyID: "pat-1", Permissions: perms}
}

// newTestService validates cfg and starts a service on a fresh double.
func newTestService(t *testing.T, cfg *Config) (*Service, *fakeProvider) {
	t.Helper()
	fake := newFakeProvider()
	svc, err := New(requireValid(t, cfg), fake)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return svc, fake
}

// decodeBody decodes a JSON response body.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not JSON: %v\n%s", err, rec.Body.String())
	}
	return body
}

// errorEnvelope returns the error object of an apikit error response.
func errorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	obj, ok := decodeBody(t, rec)["error"].(map[string]any)
	if !ok {
		t.Fatalf("response is not an apikit error envelope: %s", rec.Body.String())
	}
	return obj
}

// TS-25-39: No credential gets 401 before the feature-off check.
// Requirement: 25-REQ-7.1
func TestHandler_NoCredentialIs401_TS_25_39(t *testing.T) {
	enabled, _ := newTestService(t, validConfig())
	for name, svc := range map[string]*Service{"feature off": nil, "feature on": enabled} {
		t.Run(name, func(t *testing.T) {
			rec := getProfiles(t, newHandlerEcho(svc), nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d; want 401", rec.Code)
			}
			env := errorEnvelope(t, rec)
			if env["code"] != float64(401) {
				t.Errorf("error code = %v; want 401", env["code"])
			}
			if env["error_type"] == "sandboxes_disabled" {
				t.Error("an unauthenticated request got the sandboxes_disabled error")
			}
		})
	}
}

// TS-25-40: With the feature off every authenticated caller gets 404
// sandboxes_disabled, whatever its scopes.
// Requirement: 25-REQ-7.2
func TestHandler_FeatureOff_TS_25_40(t *testing.T) {
	want := map[string]any{"error": map[string]any{
		"code":       float64(404),
		"message":    "sandboxes are not enabled on this hub",
		"error_type": "sandboxes_disabled",
	}}
	e := newHandlerEcho(nil)
	for name, auth := range map[string]*apikit.AuthInfo{
		"admin token":       adminAuth(),
		"api key":           apiKeyAuth(),
		"PAT without scope": patAuth("secrets:list"),
		"PAT with scope":    patAuth("sandboxes:read"),
	} {
		t.Run(name, func(t *testing.T) {
			rec := getProfiles(t, e, auth)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d; want 404", rec.Code)
			}
			if got := decodeBody(t, rec); !reflect.DeepEqual(got, want) {
				t.Errorf("body = %v; want %v", got, want)
			}
		})
	}
}

// TS-25-41: With the feature on a PAT without sandbox scopes gets 403, and a
// PAT with sandboxes:read or sandboxes:write gets 200.
// Requirement: 25-REQ-7.3
func TestHandler_PATScopes_TS_25_41(t *testing.T) {
	svc, _ := newTestService(t, validConfig())
	e := newHandlerEcho(svc)

	rec := getProfiles(t, e, patAuth("secrets:list"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PAT with secrets:list: status = %d; want 403", rec.Code)
	}
	if env := errorEnvelope(t, rec); env["code"] != float64(403) || env["message"] == "" {
		t.Errorf("403 body = %v; want an apikit error envelope with a message", env)
	}

	if rec := getProfiles(t, e, patAuth()); rec.Code != http.StatusForbidden {
		t.Errorf("PAT without permissions: status = %d; want 403", rec.Code)
	}
	for _, scope := range []string{"sandboxes:read", "sandboxes:write"} {
		if rec := getProfiles(t, e, patAuth(scope)); rec.Code != http.StatusOK {
			t.Errorf("PAT with %s: status = %d; want 200", scope, rec.Code)
		}
	}
	for name, auth := range map[string]*apikit.AuthInfo{"admin token": adminAuth(), "api key": apiKeyAuth()} {
		if rec := getProfiles(t, e, auth); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d; want 200 without a scope check", name, rec.Code)
		}
	}
}

// TS-25-42: The listing returns every profile sorted by name with the
// documented fields.
// Requirement: 25-REQ-7.4
func TestHandler_ListsProfiles_TS_25_42(t *testing.T) {
	cfg := configWith(
		profileWith("zeta", func(p *ProfileConfig) {
			p.Default = true
			p.Image = "registry.example/zeta:2"
			p.TTL = "90m"
			p.CPUs, p.MemoryMB, p.PIDs, p.DiskMB = 1, 512, 100, 0
			p.Secrets = []string{"GIT_PAT"}
			p.OptionalSecrets = []string{"EXTRA"}
			p.Env = map[string]string{"LANG": "C"}
			p.RequireLandlock = true
		}),
		validProfile("alpha"),
	)
	svc, _ := newTestService(t, cfg)

	rec := getProfiles(t, newHandlerEcho(svc), adminAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200: %s", rec.Code, rec.Body)
	}
	want := map[string]any{"profiles": []any{
		map[string]any{
			"name": "alpha", "default": false, "image": "registry.example/alpha:1", "network": "none",
			"ttl_seconds": float64(3600),
			"resources":   map[string]any{"cpus": float64(0), "memory_mb": float64(0), "pids": float64(0), "disk_mb": float64(0)},
			"secrets":     []any{}, "optional_secrets": []any{}, "env_keys": []any{}, "require_landlock": false,
		},
		map[string]any{
			"name": "zeta", "default": true, "image": "registry.example/zeta:2", "network": "none",
			"ttl_seconds": float64(5400),
			"resources":   map[string]any{"cpus": float64(1), "memory_mb": float64(512), "pids": float64(100), "disk_mb": float64(0)},
			"secrets":     []any{"GIT_PAT"}, "optional_secrets": []any{"EXTRA"}, "env_keys": []any{"LANG"}, "require_landlock": true,
		},
	}}
	if got := decodeBody(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("body = %v\nwant   %v", got, want)
	}
	if !regexp.MustCompile(`"ttl_seconds":5400[,}]`).Match(rec.Body.Bytes()) {
		t.Errorf("ttl_seconds is not the JSON integer 5400: %s", rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
}

// randomCase returns name with a random case for every letter.
func randomCase(r *rand.Rand, name string) string {
	var sb strings.Builder
	for _, c := range name {
		if r.IntN(2) == 0 {
			sb.WriteString(strings.ToLower(string(c)))
		} else {
			sb.WriteString(strings.ToUpper(string(c)))
		}
	}
	return sb.String()
}

// TS-25-43: For any profile the three name lists are sorted ascending JSON
// arrays, never null.
// Requirement: 25-REQ-7.5
func TestHandler_ListsAreSortedArrays_Property_TS_25_43(t *testing.T) {
	r := rand.New(rand.NewPCG(25, 43))
	base := []string{"alpha", "beta", "gamma", "delta", "eps", "zeta", "eta", "theta", "iota", "kappa", "lambda", "mu", "nu", "xi", "pi", "rho"}

	for i := range 150 {
		// Distinct names, so that no two collide ignoring case, dealt into the
		// three lists in a random order and random case.
		names := slices.Clone(base)
		r.Shuffle(len(names), func(a, b int) { names[a], names[b] = names[b], names[a] })
		take := func(n int) []string {
			out := make([]string, n)
			for j := range out {
				out[j] = randomCase(r, names[0])
				names = names[1:]
			}
			return out
		}
		secretNames, optionalNames, envNames := take(r.IntN(5)), take(r.IntN(5)), take(r.IntN(5))
		env := map[string]string{}
		for _, k := range envNames {
			env[k] = "value"
		}

		cfg := configWith(profileWith("p", func(p *ProfileConfig) {
			p.Secrets, p.OptionalSecrets, p.Env = secretNames, optionalNames, env
		}))
		svc, _ := newTestService(t, cfg)
		body := decodeBody(t, getProfiles(t, newHandlerEcho(svc), adminAuth()))
		profile := body["profiles"].([]any)[0].(map[string]any)

		for field, want := range map[string][]string{"secrets": secretNames, "optional_secrets": optionalNames, "env_keys": envNames} {
			got, ok := profile[field].([]any)
			if !ok {
				t.Fatalf("case %d: %s = %v (%T); want a JSON array", i, field, profile[field], profile[field])
			}
			gotNames := make([]string, len(got))
			for j, v := range got {
				gotNames[j] = v.(string)
			}
			wantSorted := slices.Sorted(slices.Values(want))
			if len(want) == 0 {
				wantSorted = []string{}
			}
			if !slices.Equal(gotNames, wantSorted) {
				t.Errorf("case %d: %s = %v; want %v sorted ascending", i, field, gotNames, wantSorted)
			}
		}
	}
}

// TS-25-44: The listing makes no provider call and no database read and still
// answers after the engine fails.
// Requirement: 25-REQ-7.6
func TestHandler_ServesFromMemory_TS_25_44(t *testing.T) {
	svc, fake := newTestService(t, configWith(validProfile("alpha"), validProfile("beta")))
	e := newHandlerEcho(svc)

	fake.failAll()
	fake.reset()
	rec := getProfiles(t, e, adminAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 after the engine failed: %s", rec.Code, rec.Body)
	}
	if n := len(decodeBody(t, rec)["profiles"].([]any)); n != 2 {
		t.Errorf("profiles = %d; want 2", n)
	}
	if fake.total() != 0 {
		t.Errorf("the listing made %d provider calls; want none", fake.total())
	}

	// The routes are wired from the service alone: no database or secret
	// store can reach the handler.
	assertNoStoreParams(t, "RegisterRoutes", RegisterRoutes)
}

// TS-25-45: Every authorised caller receives an identical profile list.
// Requirement: 25-REQ-7.7
func TestHandler_SameListForEveryCaller_TS_25_45(t *testing.T) {
	svc, _ := newTestService(t, configWith(validProfile("alpha"), validProfile("beta")))
	e := newHandlerEcho(svc)
	base := getProfiles(t, e, adminAuth())
	if base.Code != http.StatusOK {
		t.Fatalf("admin: status = %d; want 200", base.Code)
	}

	callers := []*apikit.AuthInfo{apiKeyAuth(), patAuth("sandboxes:read"), patAuth("sandboxes:write")}
	for _, user := range []string{"user-2", "user-3", ""} {
		callers = append(callers,
			&apikit.AuthInfo{CredentialType: "api_key", UserID: user, KeyID: "k-" + user},
			&apikit.AuthInfo{CredentialType: "pat", UserID: user, KeyID: "p-" + user, Permissions: []string{"workspaces:read", "sandboxes:read"}},
		)
	}
	for _, auth := range callers {
		rec := getProfiles(t, e, auth)
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s: status = %d; want 200", auth.CredentialType, auth.UserID, rec.Code)
			continue
		}
		if !bytes.Equal(rec.Body.Bytes(), base.Body.Bytes()) {
			t.Errorf("%s %s got a different body:\n%s\nwant\n%s", auth.CredentialType, auth.UserID, rec.Body, base.Body)
		}
	}
}

// captureDefaultLogs routes the default slog logger to a buffer for the length
// of the test and returns the buffer.
func captureDefaultLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TS-25-46: For any sandbox section, valid or with static faults, no env value
// appears in what loading, validating, starting and listing it produce: the
// errors, the logs or the listing body.
// Requirement: 25-REQ-8.1
func TestNoEnvValuesLeak_Property_TS_25_46(t *testing.T) {
	r := rand.New(rand.NewPCG(25, 46))
	logs := captureDefaultLogs(t)
	dir := t.TempDir()

	var started, refused int
	for i := range 150 {
		var sentinels []string
		sentinel := func() string {
			s := fmt.Sprintf("sv%016x", r.Uint64())
			sentinels = append(sentinels, s)
			return s
		}

		// One env value reaches the section through ${NAME}, the others as
		// literal text.
		referenced := sentinel()
		t.Setenv("SBXT_ENV_REF", referenced)

		profile := ProfileConfig{
			Name:    "p",
			Image:   "registry.example/p:1",
			TTL:     "1h",
			Secrets: []string{"GIT_PAT"},
			Env:     map[string]string{"LITERAL": sentinel(), "FROM_ENV": "${SBXT_ENV_REF}"},
		}
		other := ProfileConfig{Name: "q", Image: "registry.example/q:1", TTL: "1h", Env: map[string]string{"OTHER": sentinel()}}
		cfg := Config{Provider: "podman", Profiles: []ProfileConfig{profile, other}}

		if r.IntN(4) == 0 {
			cfg.Profiles[0].Env["BAD-KEY"] = sentinel()
		}
		if r.IntN(4) == 0 {
			cfg.Profiles[0].Env["git_pat"] = sentinel()
		}
		if r.IntN(4) == 0 {
			cfg.Profiles[1].TTL = "never"
		}
		if r.IntN(4) == 0 {
			cfg.Profiles[1].Name = "p"
		}
		if r.IntN(4) == 0 {
			cfg.MaxTTL = strPtr("soon")
		}

		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(struct {
			Sandbox Config `toml:"sandbox"`
		}{cfg}); err != nil {
			t.Fatalf("case %d: encoding the section: %v", i, err)
		}
		if r.IntN(4) == 0 {
			fmt.Fprintf(&buf, "\n[sandbox.unknown_table]\nsecret = %q\n", sentinel())
		}

		var errTexts []string
		note := func(err error) {
			if err != nil {
				errTexts = append(errTexts, err.Error())
			}
		}
		var body string

		// Init is the start-up path: it loads, validates, starts and logs.
		initLogger, initLog := newCapturedLogger()
		fake := newFakeProvider()
		if r.IntN(4) == 0 {
			fake.reapErr = errors.New("engine down")
		}
		factory := &recordingFactory{provider: fake}
		svc, err := Init(initLogger, writeConfigFile(t, dir, buf.String()), factory.build)
		note(err)
		if err == nil {
			started++
			if !strings.Contains(initLog.String(), "sandboxes are enabled") {
				t.Fatalf("case %d: Init started without logging the enabled line: %s", i, initLog)
			}
			for _, auth := range []*apikit.AuthInfo{adminAuth(), patAuth("sandboxes:read")} {
				body += getProfiles(t, newHandlerEcho(svc), auth).Body.String()
			}
		} else {
			refused++
		}

		output := strings.Join(errTexts, "\n") + "\n" + body + "\n" + logs.String() + "\n" + initLog.String()
		for _, s := range sentinels {
			if strings.Contains(output, s) {
				t.Fatalf("case %d: the env value %q leaked into errors, logs or the listing:\n%s", i, s, output)
			}
		}
	}
	if started == 0 || refused == 0 {
		t.Errorf("the generator produced %d started and %d refused sections; want both kinds", started, refused)
	}
}

// assertNoStoreParams fails the test when fn takes a database handle or a
// secrets store.
func assertNoStoreParams(t *testing.T, name string, fn any) {
	t.Helper()
	typ := reflect.TypeOf(fn)
	forbidden := []reflect.Type{reflect.TypeOf((*sql.DB)(nil)), reflect.TypeOf((*secrets.Store)(nil))}
	for i := range typ.NumIn() {
		if slices.Contains(forbidden, typ.In(i)) {
			t.Errorf("%s takes a %s parameter; the sandbox definition needs no database or secret store", name, typ.In(i))
		}
	}
}

// TS-25-48: The listing omits the provider, its options and max_ttl, and
// nothing that builds or serves the definition takes a store or a database.
// Requirement: 25-REQ-8.3
func TestListingOmitsDeploymentSettings_TS_25_48(t *testing.T) {
	cfg := configWith(validProfile("dev"))
	cfg.EngineBinary, cfg.Runtime, cfg.MaxTTL = "/opt/podman-x", "runsc", strPtr("3h")
	svc, _ := newTestService(t, cfg)

	body := getProfiles(t, newHandlerEcho(svc), adminAuth()).Body.String()
	for _, hidden := range []string{"podman", "podman-x", "runsc", "max_ttl", "3h", "provider", "engine"} {
		if strings.Contains(body, hidden) {
			t.Errorf("the listing contains %q: %s", hidden, body)
		}
	}

	assertNoStoreParams(t, "LoadConfig", LoadConfig)
	assertNoStoreParams(t, "Validate", Validate)
	assertNoStoreParams(t, "New", New)
	assertNoStoreParams(t, "Start", Start)
	assertNoStoreParams(t, "BuildProvider", BuildProvider)
}
