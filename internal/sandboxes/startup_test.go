package sandboxes

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// logRecords parses what a JSON slog handler wrote into one map per record.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v\n%s", err, line)
		}
		records = append(records, rec)
	}
	return records
}

// newCapturedLogger returns a logger that writes JSON records to the buffer.
func newCapturedLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// attributeKeys returns the keys of a record other than slog's own.
func attributeKeys(rec map[string]any) []string {
	var keys []string
	for k := range rec {
		if k != "time" && k != "level" && k != "msg" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// TS-25-2: A missing file or a file without [sandbox] starts nothing, returns
// a nil service and a nil error, and logs exactly one info line that says
// sandboxes are disabled.
// Requirement: 25-REQ-1.2
func TestInit_Disabled_TS_25_2(t *testing.T) {
	dir := t.TempDir()
	for name, path := range map[string]string{
		"no file":          filepath.Join(dir, "missing.toml"),
		"no [sandbox]":     writeConfigFile(t, dir, "[server]\nport = 9090\n"),
		"only apikit keys": writeConfigFile(t, t.TempDir(), "[database]\npath = \"x.db\"\n\n[workspace]\nworkers = 2\n"),
	} {
		t.Run(name, func(t *testing.T) {
			logger, buf := newCapturedLogger()
			factory := &recordingFactory{provider: newFakeProvider()}

			svc, err := Init(logger, path, factory.build)
			if svc != nil || err != nil {
				t.Fatalf("Init = (%v, %v); want (nil, nil)", svc, err)
			}
			records := logRecords(t, buf)
			if len(records) != 1 {
				t.Fatalf("%d log records; want exactly one: %s", len(records), buf)
			}
			if records[0]["level"] != "INFO" || !strings.Contains(records[0]["msg"].(string), "disabled") {
				t.Errorf("record = %v; want an info line saying sandboxes are disabled", records[0])
			}
			if factory.calls != 0 {
				t.Errorf("the provider factory was called %d times; want none", factory.calls)
			}
		})
	}
}

// TS-25-47: Enabling the feature logs one info line with only the provider and
// the profile names.
// Requirement: 25-REQ-8.2
func TestInit_Enabled_TS_25_47(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider     = "podman"
engine_binary = "/opt/engine-distinctive/podman"
runtime      = "krun-distinctive"
max_ttl      = "3h"
network_modes = ["none"]

[[sandbox.profiles]]
name    = "alpha"
image   = "registry.distinctive.example/image-alpha:9"
ttl     = "1h"
secrets = ["SECRET_NAME_DISTINCTIVE"]

[sandbox.profiles.env]
ENV_KEY_DISTINCTIVE = "env-value-distinctive"

[[sandbox.profiles]]
name  = "beta"
image = "registry.distinctive.example/image-beta:9"
ttl   = "2h"
`)
	logger, buf := newCapturedLogger()
	fake := newFakeProvider()
	factory := &recordingFactory{provider: fake}

	svc, err := Init(logger, path, factory.build)
	if err != nil || svc == nil {
		t.Fatalf("Init = (%v, %v); want a service", svc, err)
	}
	if svc.Provider() != fake {
		t.Error("the service does not hold the provider the factory built")
	}

	records := logRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("%d log records; want exactly one: %s", len(records), buf)
	}
	rec := records[0]
	if rec["level"] != "INFO" {
		t.Errorf("level = %v; want INFO", rec["level"])
	}
	if got := attributeKeys(rec); !slices.Equal(got, []string{"profiles", "provider"}) {
		t.Errorf("attributes = %v; want only provider and profiles", got)
	}
	if rec["provider"] != "podman" {
		t.Errorf("provider = %v; want podman", rec["provider"])
	}
	if got, _ := json.Marshal(rec["profiles"]); string(got) != `["alpha","beta"]` {
		t.Errorf("profiles = %s; want [\"alpha\",\"beta\"] in file order", got)
	}
	for _, hidden := range []string{"distinctive", "3h", "krun"} {
		if strings.Contains(buf.String(), hidden) {
			t.Errorf("the log line contains %q: %s", hidden, buf)
		}
	}
}

// An invalid section, an unreachable engine and an undecodable file refuse
// start without logging an enabled line, and no error or log line carries an
// env value.
// Requirements: 25-REQ-1.4, 25-REQ-8.1
func TestInit_Refusals(t *testing.T) {
	const secretValue = "env-value-never-printed"
	section := func(extra string) string {
		return "[sandbox]\nprovider = \"podman\"\n" + extra + `
[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "1h"
[sandbox.profiles.env]
TOKEN = "` + secretValue + `"
`
	}
	for name, tc := range map[string]struct {
		config string
		fail   bool // the provider's probe fails
		want   string
	}{
		"unknown key":        {config: section("listenx = \":9\"\n"), want: "sandbox.listenx"},
		"invalid ttl":        {config: strings.Replace(section(""), `"1h"`, `"never"`, 1), want: `profile "dev"`},
		"engine unreachable": {config: section(""), fail: true, want: "sandbox.engine_binary"},
		"invalid TOML":       {config: "[sandbox\n", want: "decoding"},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeConfigFile(t, t.TempDir(), tc.config)
			logger, buf := newCapturedLogger()
			fake := newFakeProvider()
			if tc.fail {
				fake.reapErr = errors.New("engine down")
			}
			factory := &recordingFactory{provider: fake}

			svc, err := Init(logger, path, factory.build)
			if svc != nil || err == nil {
				t.Fatalf("Init = (%v, %v); want a refusal", svc, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
			if !strings.HasPrefix(err.Error(), "sandbox:") {
				t.Errorf("error %q does not start with sandbox:", err)
			}
			if strings.Contains(err.Error()+buf.String(), secretValue) {
				t.Errorf("an env value leaked:\n%v\n%s", err, buf)
			}
			if buf.Len() != 0 {
				t.Errorf("a refused start logged: %s", buf)
			}
		})
	}
}
