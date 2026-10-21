package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// logRecords decodes the JSON log lines.
func logRecords(t *testing.T, logs *lockedBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// pushWarnings are the push-attempt warnings logged for the workspace slug.
func pushWarnings(t *testing.T, logs *lockedBuffer, slug string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range logRecords(t, logs) {
		if rec["level"] == "WARN" && rec["workspace"] == slug && rec["error_type"] != nil {
			out = append(out, rec)
		}
	}
	return out
}

// TS-38-38: No response, audit event or log line contains the credential
// secret or the git_url userinfo.
// Requirement: 38-REQ-7.2
func TestBranchPushNoLeaks_TS_38_38(t *testing.T) {
	env := newBranchPushEnv(t)
	logs := captureBranchPushLog(t)
	const echoed = "forge-says tok-12345 at http://u:urlpw@forge.example/r.git"
	bodies := map[string]string{}
	run := func(slug string, setup func(forge *standInForge, gitURL *string)) {
		forge := newStandInForge(t)
		env.seedPushWorkspace(t, &Workspace{Slug: slug, OwnerID: "user-a"}, "feat")
		storeGitSecrets(t, env, slug, "GIT_PAT", "tok-12345")
		gitURL := strings.Replace(forge.repoURL(), "http://", "http://u:urlpw@", 1)
		setup(forge, &gitURL)
		setGitURL(t, env, slug, gitURL)
		rec := env.push(t, slug, "feat", "", userAuth("user-a"))
		bodies[slug] = rec.Body.String()
		if rec.Code == http.StatusInternalServerError {
			t.Errorf("%s: answered 500: %s", slug, rec.Body)
		}
	}
	answerWith := func(status int) func(*standInForge, *string) {
		return func(f *standInForge, _ *string) {
			f.setAnswer(func(w http.ResponseWriter, r *http.Request) bool {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(echoed))
				return true
			})
		}
	}
	run("leak-ok", func(*standInForge, *string) {})
	run("leak-401", answerWith(http.StatusUnauthorized))
	run("leak-403", answerWith(http.StatusForbidden))
	run("leak-rejected", func(f *standInForge, _ *string) {
		writeHook(t, f.bare, "pre-receive", "#!/bin/sh\necho '"+echoed+"' >&2\nexit 1\n")
	})
	run("leak-nff", func(f *standInForge, _ *string) {
		pushGit(t, filepath.Join(env.root, "leak-nff", "trunk"), "push", "-q", f.bare, "feat")
		divergeBare(t, f.bare, "feat")
	})
	run("leak-unreachable", func(_ *standInForge, gitURL *string) { *gitURL = "http://u:urlpw@127.0.0.1:1/r.git" })

	events, _ := json.Marshal(env.branchEvents())
	var responses []string
	for _, body := range bodies {
		responses = append(responses, body)
	}
	for name, text := range map[string]string{"responses": strings.Join(responses, "\n"), "events": string(events), "logs": logs.String()} {
		for _, secret := range []string{"tok-12345", "urlpw"} {
			if strings.Contains(text, secret) {
				t.Errorf("the %s hold %q:\n%s", name, secret, text)
			}
		}
	}
	// Only a refused ref carries the forge's own text, its reason (R6).
	for slug, body := range bodies {
		if strings.Contains(body, "authentication required") || strings.Contains(body, "authorization failed") ||
			slug != "leak-rejected" && strings.Contains(body, "forge-says") {
			t.Errorf("%s: the response carries go-git's error chain: %s", slug, body)
		}
	}
	if len(env.branchEvents()) != 6 {
		t.Errorf("%d events; want one per push attempt (6)", len(env.branchEvents()))
	}
}

// TS-38-39: Each failed push attempt logs exactly one warning with workspace,
// branch, error_type and the scrubbed error; success logs none.
// Requirement: 38-REQ-7.3
func TestBranchPushFailureWarnings_TS_38_39(t *testing.T) {
	env := newBranchPushEnv(t)
	logs := captureBranchPushLog(t)
	for i, o := range stubbedOutcomes {
		slug := fmt.Sprintf("warn-%d", i)
		env.seedPushWorkspace(t, &Workspace{Slug: slug, OwnerID: "user-a"}, "feat")
		stubPush(t, o.err)
		env.push(t, slug, "feat", o.body, userAuth("user-a"))
		warnings := pushWarnings(t, logs, slug)
		if o.errorType == "" {
			if len(warnings) != 0 {
				t.Errorf("%s: %d warnings; want none: %v", o.name, len(warnings), warnings)
			}
			continue
		}
		if len(warnings) != 1 {
			t.Errorf("%s: %d warnings; want exactly one: %v", o.name, len(warnings), warnings)
			continue
		}
		w := warnings[0]
		if w["branch"] != "feat" || w["error_type"] != o.errorType || w["error"] == nil || w["error"] == "" {
			t.Errorf("%s: warning %v; want workspace, branch, error_type %s and error", o.name, w, o.errorType)
		}
	}
}

// TS-38-40: A 500 answer is logged as an internal error with the credential
// and userinfo scrubbed.
// Requirement: 38-REQ-7.4
func TestBranchPushInternalErrorLog_TS_38_40(t *testing.T) {
	env := newBranchPushEnv(t)
	logs := captureBranchPushLog(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "internal", OwnerID: "user-a"}, "feat")
	setGitURL(t, env, "internal", "http://u:urlpw@forge.example/r.git")
	storeGitSecrets(t, env, "internal", "GIT_PAT", "tok-12345")
	stubPush(t, &trunkError{errors.New("open http://u:urlpw@forge.example/r.git with tok-12345: no such directory")})

	assertRefusal(t, env.push(t, "internal", "feat", "", userAuth("user-a")), http.StatusInternalServerError, "internal server error", "")
	var logged bool
	for _, rec := range logRecords(t, logs) {
		if rec["level"] == "ERROR" && rec["workspace"] == "internal" {
			logged = true
		}
	}
	if !logged {
		t.Errorf("no internal error was logged: %s", logs)
	}
	if out := logs.String(); strings.Contains(out, "tok-12345") || strings.Contains(out, "urlpw") {
		t.Errorf("the log holds a secret: %s", out)
	}
}
