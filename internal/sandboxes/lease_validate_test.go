package sandboxes

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// noDefaultConfig is the default section without a default profile.
func noDefaultConfig() *Config {
	cfg := bigLeaseConfig()
	cfg.Profiles[0].Default = false
	return cfg
}

// TS-26-17: Unknown body fields and non-object bodies are refused with 400, and
// the message names the unknown field.
// Requirement: 26-REQ-3.3
func TestIssue_UnknownFieldsAndNonObjects_TS_26_17(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	for _, tc := range []struct {
		name, body, want string
	}{
		{"image", `{"image":"evil"}`, "image"},
		{"env", `{"env":{}}`, "env"},
		{"known field beside an unknown one", `{"profile":"tools","gateway_url":"x"}`, "gateway_url"},
		{"malformed JSON", `{"profile":`, ""},
		{"array", `[1]`, ""},
		{"string", `"tools"`, ""},
		{"null", `null`, ""},
		{"number", `7`, ""},
		{"trailing data", `{} {}`, ""},
		{"stray closing brace", `{"profile":"tools"}}`, ""},
		{"stray closing bracket", `{"profile":"tools"}]`, ""},
		{"text after the object", `{"profile":"tools"} x`, ""},
		{"a field name in capitals", `{"PROFILE":"tools"}`, "PROFILE"},
		{"a field name in mixed case", `{"Profile":"tools","TTL":"1h"}`, "Profile"},
		{"an unknown field in capitals", `{"Image":"x"}`, "Image"},
		{"profile is not a string", `{"profile":7}`, "profile"},
		{"purpose is not a string", `{"purpose":["x"]}`, "purpose"},
		{"ttl is not a string", `{"ttl":3600}`, "ttl"},
		{"labels is not an object", `{"labels":"k=v"}`, "labels"},
	} {
		rec := env.post("widgets", tc.body, apiKeyAuth())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400: %s", tc.name, rec.Code, rec.Body)
			continue
		}
		if !strings.Contains(message(t, rec), tc.want) {
			t.Errorf("%s: message %q does not contain %q", tc.name, message(t, rec), tc.want)
		}
	}
	if n := env.countLeases(); n != 0 {
		t.Errorf("%d leases stored for refused requests", n)
	}

	// The message for a field of the wrong type reads as a sentence.
	if msg := message(t, env.post("widgets", `{"labels":"k=v"}`, apiKeyAuth())); !strings.Contains(msg, "must be an object, not a string") {
		t.Errorf("message for labels as a string = %q; want 'must be an object, not a string'", msg)
	}
	if msg := message(t, env.post("widgets", `{"profile":7}`, apiKeyAuth())); !strings.Contains(msg, "must be a string, not a number") {
		t.Errorf("message for profile as a number = %q; want 'must be a string, not a number'", msg)
	}
}

// A body that cannot be read in full is a request the caller got wrong, not a
// server failure: one over the server's limit is 413, any other read error 400.
// Requirement: 26-REQ-3.3
func TestIssue_UnreadableBody(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	auth, _ := json.Marshal(apiKeyAuth())

	send := func(body io.Reader, limit int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/widgets/sandboxes", nil)
		req.Header.Set("X-Test-Auth", string(auth))
		rec := httptest.NewRecorder()
		req.Body = io.NopCloser(body)
		if limit > 0 {
			req.Body = http.MaxBytesReader(rec, req.Body, limit)
		}
		env.echo.ServeHTTP(rec, req)
		return rec
	}

	big := strings.NewReader(`{"purpose":"` + strings.Repeat("x", 100) + `"}`)
	if rec := send(big, 20); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the limit: status %d; want 413: %s", rec.Code, rec.Body)
	}
	if rec := send(io.MultiReader(strings.NewReader(`{"purpose":`), failingReader{}), 0); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that fails while being read: status %d; want 400: %s", rec.Code, rec.Body)
	}
	if n := env.countLeases(); n != 0 {
		t.Errorf("%d leases stored for unreadable bodies", n)
	}
}

// TS-26-18: An unknown profile name, or no profile when none is the default, is
// refused with the known profile names in configuration order.
// Requirement: 26-REQ-3.4
func TestIssue_UnknownProfile_TS_26_18(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: noDefaultConfig()})
	for _, tc := range []struct{ name, body string }{
		{"wrong case", `{"profile":"Tools"}`},
		{"unknown", `{"profile":"nope"}`},
		{"explicitly empty", `{"profile":""}`},
		{"absent, no default", `{}`},
		{"empty body, no default", ``},
	} {
		rec := env.post("widgets", tc.body, apiKeyAuth())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400: %s", tc.name, rec.Code, rec.Body)
			continue
		}
		msg := message(t, rec)
		tools, data := strings.Index(msg, "tools"), strings.Index(msg, "data")
		if tools < 0 || data < 0 || tools > data {
			t.Errorf("%s: message %q does not list tools before data", tc.name, msg)
		}
	}
	if n := env.countLeases(); n != 0 {
		t.Errorf("%d leases stored for refused requests", n)
	}

	// With a default, an absent profile is fine and a wrong name is still refused.
	env = newLeaseEnv(t, leaseEnvOptions{})
	if rec := env.post("widgets", `{}`, apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Errorf("absent profile with a default: status = %d; want 201", rec.Code)
	}
	if rec := env.post("widgets", `{"profile":"tools "}`, apiKeyAuth()); rec.Code != http.StatusBadRequest {
		t.Errorf("a profile name with a trailing space: status = %d; want 400", rec.Code)
	}
}

// TS-26-19: A ttl from 1 minute up to the profile's TTL is accepted, both
// boundaries included, and a longer one is not.
// Requirement: 26-REQ-3.5
func TestIssue_TTLAccepted_TS_26_19(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	for _, ttl := range []string{"1m", "15m", "1h", "3600s", "61.5s"} {
		rec := env.post("widgets", fmt.Sprintf(`{"profile":"tools","ttl":%q}`, ttl), apiKeyAuth())
		if rec.Code != http.StatusCreated {
			t.Errorf("ttl %q: status = %d; want 201: %s", ttl, rec.Code, rec.Body)
			continue
		}
		got := jsonObject(t, rec)
		created, _ := apikit.ParseUTC(got["created_at"].(string))
		expires, _ := apikit.ParseUTC(got["expires_at"].(string))
		want, _ := time.ParseDuration(ttl)
		if expires.Sub(created) != want.Truncate(time.Second) {
			t.Errorf("ttl %q: expires_at - created_at = %v; want %v (whole seconds)", ttl, expires.Sub(created), want.Truncate(time.Second))
		}
	}
	if rec := env.post("widgets", `{"profile":"tools","ttl":"90m"}`, apiKeyAuth()); rec.Code != http.StatusBadRequest {
		t.Errorf("ttl 90m over a 1h profile: status = %d; want 400", rec.Code)
	}
}

// TS-26-20: An unparseable, too short, non-positive or too long ttl is refused
// with 400, and a too long one names the profile's TTL.
// Requirement: 26-REQ-3.6
func TestIssue_TTLRefused_TS_26_20(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	for _, ttl := range []string{"soon", "", "59s", "0s", "-5m", "30s", "2h", "3601s"} {
		rec := env.post("widgets", fmt.Sprintf(`{"profile":"tools","ttl":%q}`, ttl), apiKeyAuth())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("ttl %q: status = %d; want 400: %s", ttl, rec.Code, rec.Body)
		}
	}
	if msg := message(t, env.post("widgets", `{"profile":"tools","ttl":"2h"}`, apiKeyAuth())); !strings.Contains(msg, "1h") {
		t.Errorf("message for a too long ttl = %q; want it to name the profile's ttl 1h", msg)
	}
	if msg := message(t, env.post("widgets", `{"profile":"data","ttl":"1h"}`, apiKeyAuth())); !strings.Contains(msg, "30m") {
		t.Errorf("message for a too long ttl on data = %q; want it to name 30m", msg)
	}
	if n := env.countLeases(); n != 0 {
		t.Errorf("%d leases stored for refused requests", n)
	}
}

// TS-26-21: A purpose is accepted at 64 runes and refused at 65 runes or with a
// control character.
// Requirement: 26-REQ-3.7
func TestIssue_Purpose_TS_26_21(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	body := func(purpose string) string {
		raw, _ := json.Marshal(map[string]string{"purpose": purpose})
		return string(raw)
	}

	// 64 multi-byte runes are 128 bytes: the limit counts runes.
	if rec := env.post("widgets", body(strings.Repeat("é", 64)), apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Errorf("64 runes: status = %d; want 201: %s", rec.Code, rec.Body)
	}
	for name, purpose := range map[string]string{
		"65 runes":         strings.Repeat("a", 65),
		"65 wide runes":    strings.Repeat("é", 65),
		"newline":          "a\nb",
		"tab":              "a\tb",
		"NUL":              "a\x00b",
		"DEL":              "a\x7fb",
		"C1 control (NEL)": "a\u0085b",
	} {
		if rec := env.post("widgets", body(purpose), apiKeyAuth()); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400", name, rec.Code)
		}
	}
	if n := env.countLeases(); n != 1 {
		t.Errorf("%d leases stored; want only the accepted one", n)
	}
}

// TS-26-22: Invalid labels are refused with 400 naming the offending key.
// Requirement: 26-REQ-3.8
func TestIssue_LabelsRefused_TS_26_22(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})

	seventeen := map[string]any{}
	for i := 1; i <= 17; i++ {
		seventeen[fmt.Sprintf("k%02d", i)] = "v"
	}
	for _, tc := range []struct {
		name   string
		labels map[string]any
		key    string
	}{
		{"17 labels", seventeen, "k17"},
		{"non-string value", map[string]any{"numkey": 1}, "numkey"},
		{"null value", map[string]any{"nullkey": nil}, "nullkey"},
		{"leading dash", map[string]any{"-bad": "v"}, "-bad"},
		{"trailing dash", map[string]any{"bad-": "v"}, "bad-"},
		{"leading dot", map[string]any{".bad": "v"}, ".bad"},
		{"space", map[string]any{"has space": "v"}, "has space"},
		{"empty key", map[string]any{"": "v"}, ""},
		{"slash", map[string]any{"a/b": "v"}, "a/b"},
		{"reserved lease", map[string]any{"Lease": "v"}, "Lease"},
		{"reserved workspace", map[string]any{"WORKSPACE": "v"}, "WORKSPACE"},
		{"64-character key", map[string]any{strings.Repeat("k", 64): "v"}, strings.Repeat("k", 64)},
		{"257-byte value", map[string]any{"bigval": strings.Repeat("v", 257)}, "bigval"},
		{"129 two-byte runes are 258 bytes", map[string]any{"mbval": strings.Repeat("é", 129)}, "mbval"},
		{"control character in value", map[string]any{"ctlval": "a\x01b"}, "ctlval"},
		{"newline in value", map[string]any{"nlval": "a\nb"}, "nlval"},
	} {
		raw, _ := json.Marshal(map[string]any{"labels": tc.labels})
		rec := env.post("widgets", string(raw), apiKeyAuth())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400: %s", tc.name, rec.Code, rec.Body)
			continue
		}
		if !strings.Contains(message(t, rec), tc.key) {
			t.Errorf("%s: message %q does not name the label key %q", tc.name, message(t, rec), tc.key)
		}
	}
	if n := env.countLeases(); n != 0 {
		t.Errorf("%d leases stored for refused requests", n)
	}
}

// TS-26-23: Labels at the limits, 16 pairs, a 63-character key and a 256-byte
// value, are accepted and stored as given. A ttl of 1m and a 64-rune purpose
// are accepted in the same request.
// Requirements: 26-REQ-3.8, 26-REQ-3.5
func TestIssue_LabelsAtLimits_TS_26_23(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	labels := map[string]string{
		strings.Repeat("k", 63): strings.Repeat("v", 256),
		"a":                     "",
		"A.b_c-D":               "mixed Case ünï",
		"multibyte":             strings.Repeat("é", 128), // 256 bytes: the limit counts bytes
	}
	for i := 0; len(labels) < 16; i++ {
		labels[fmt.Sprintf("k%d", i)] = "v"
	}
	raw, _ := json.Marshal(map[string]any{"profile": "tools", "ttl": "1m", "purpose": strings.Repeat("p", 64), "labels": labels})

	rec := env.post("widgets", string(raw), apiKeyAuth())
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201: %s", rec.Code, rec.Body)
	}
	got, _ := jsonObject(t, rec)["labels"].(map[string]any)
	if len(got) != 16 {
		t.Fatalf("response has %d labels; want 16", len(got))
	}
	for k, v := range labels {
		if got[k] != v {
			t.Errorf("label %q = %v in the response; want %q as sent", k, got[k], v)
		}
	}
}
