package workspace

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// countListings makes the forge count the listings of its refs that are not
// part of a push (upload-pack advertisements), and fail them when failing is
// set.
func countListings(forge *standInForge, failing *atomic.Bool) *atomic.Int32 {
	var n atomic.Int32
	forge.setAnswer(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Get("service") != "git-upload-pack" {
			return false
		}
		n.Add(1)
		if failing != nil && failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		return false
	})
	return &n
}

// answerAll makes the forge answer every request with status.
func answerAll(forge *standInForge, status int) {
	forge.setAnswer(func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(status)
		return true
	})
}

// TS-38-32: A non-fast-forward push answers 409 non_fast_forward with origin's
// commit and the right hint, or without commit when listing fails.
// Requirement: 38-REQ-6.4
func TestBranchPushNonFastForward_TS_38_32(t *testing.T) {
	env := newBranchPushEnv(t)
	forge := newStandInForge(t)
	var failing atomic.Bool
	listings := countListings(forge, &failing)
	env.seedPushWorkspace(t, &Workspace{Slug: "nff", OwnerID: "user-a", Branch: strPtr("main")}, "feat")
	setGitURL(t, env, "nff", forge.repoURL())
	pushedOK(t, env.push(t, "nff", "feat", "", userAuth("user-a")))
	pushedOK(t, env.push(t, "nff", "main", "", userAuth("user-a")))
	featOrigin := divergeBare(t, forge.bare, "feat")
	mainOrigin := divergeBare(t, forge.bare, "main")
	listings.Store(0)

	rec := env.push(t, "nff", "feat", "", userAuth("user-a"))
	refusedWithType(t, rec, http.StatusConflict, "non_fast_forward")
	want := "non_fast_forward: origin's feat is at " + featOrigin + ", which is not in the workspace's branch; push with force to overwrite it"
	if msg := parseErrorEnvelope(t, env.push(t, "nff", "feat", "", userAuth("user-a"))).Error.Message; msg != want {
		t.Errorf("message %q; want %q", msg, want)
	}
	if n := listings.Load(); n != 2 {
		t.Errorf("origin's refs were listed %d times for two refusals; want 2", n)
	}

	rec = env.push(t, "nff", "main", "", userAuth("user-a"))
	refusedWithType(t, rec, http.StatusConflict, "non_fast_forward", mainOrigin)
	if msg := parseErrorEnvelope(t, env.push(t, "nff", "main", "", userAuth("user-a"))).Error.Message; !strings.HasSuffix(msg, "sync the workspace to bring it up to date") || strings.Contains(msg, "force") {
		t.Errorf("own branch message %q; want the sync hint and no force hint", msg)
	}

	failing.Store(true)
	rec = env.push(t, "nff", "feat", "", userAuth("user-a"))
	refusedWithType(t, rec, http.StatusConflict, "non_fast_forward", "push with force to overwrite it")
	if msg := parseErrorEnvelope(t, env.push(t, "nff", "feat", "", userAuth("user-a"))).Error.Message; strings.Contains(msg, featOrigin) {
		t.Errorf("message %q names origin's commit although the listing failed", msg)
	}
	if bareRef(t, forge.bare, "refs/heads/feat") != featOrigin || bareRef(t, forge.bare, "refs/heads/main") != mainOrigin {
		t.Error("origin changed")
	}
}

// TS-38-32: a forge that refuses a non-fast-forward with its per-ref status is
// answered the same way.
// Requirement: 38-REQ-6.4
func TestBranchPushForgeNonFastForward_TS_38_32(t *testing.T) {
	env := newBranchPushEnv(t)
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "nff-forge", OwnerID: "user-a"}, "feat")
	pushedOK(t, env.push(t, "nff-forge", "feat", "", userAuth("user-a")))
	origin := divergeBare(t, bare, "feat")
	pushGit(t, bare, "config", "receive.denyNonFastForwards", "true")

	rec := env.push(t, "nff-forge", "feat", `{"force":true}`, userAuth("user-a"))
	refusedWithType(t, rec, http.StatusConflict, "non_fast_forward", origin)
	if got := bareRef(t, bare, "refs/heads/feat"); got != origin {
		t.Errorf("origin's feat moved to %s", got)
	}
}

// TS-38-33: Forge 401 maps to 502 forge_unauthenticated naming the credential
// kind; 403 maps to 502 forge_forbidden.
// Requirement: 38-REQ-6.5
func TestBranchPushForgeAuthFailures_TS_38_33(t *testing.T) {
	env := newBranchPushEnv(t)
	for _, tc := range []struct {
		slug, kind string
		secrets    []string
	}{
		{"auth-pat", "GIT_PAT", []string{"GIT_PAT", "pat-secret-1"}},
		{"auth-basic", "GIT_USERNAME/GIT_PASSWORD", []string{"GIT_USERNAME", "bob", "GIT_PASSWORD", "pw-secret-2"}},
		{"auth-none", "no stored", nil},
	} {
		forge := newStandInForge(t)
		env.seedPushWorkspace(t, &Workspace{Slug: tc.slug, OwnerID: "user-a"}, "feat")
		setGitURL(t, env, tc.slug, forge.repoURL())
		if tc.secrets != nil {
			storeGitSecrets(t, env, tc.slug, tc.secrets...)
		}
		answerAll(forge, http.StatusUnauthorized)
		rec := env.push(t, tc.slug, "feat", "", userAuth("user-a"))
		refusedWithType(t, rec, http.StatusBadGateway, "forge_unauthenticated", tc.kind)
		answerAll(forge, http.StatusForbidden)
		rec2 := env.push(t, tc.slug, "feat", "", userAuth("user-a"))
		refusedWithType(t, rec2, http.StatusBadGateway, "forge_forbidden")
		for _, body := range []string{rec.Body.String(), rec2.Body.String()} {
			if strings.Contains(body, "pat-secret-1") || strings.Contains(body, "pw-secret-2") {
				t.Errorf("%s: the answer holds the credential: %s", tc.slug, body)
			}
		}
	}
	if msg := parseErrorEnvelope(t, env.push(t, "auth-basic", "feat", "", userAuth("user-a"))).Error.Message; strings.Contains(msg, "bob") {
		t.Errorf("message %q names the user", msg)
	}
}

// TS-38-34: Repository-not-found maps to 404 forge_not_found and a refused ref
// maps to 422 forge_rejected with the scrubbed reason.
// Requirement: 38-REQ-6.6
func TestBranchPushForgeNotFoundAndRejected_TS_38_34(t *testing.T) {
	env := newBranchPushEnv(t)
	forge := newStandInForge(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "gone", OwnerID: "user-a"}, "feat")
	setGitURL(t, env, "gone", forge.repoURL())
	answerAll(forge, http.StatusNotFound)
	refusedWithType(t, env.push(t, "gone", "feat", "", userAuth("user-a")), http.StatusNotFound, "forge_not_found")

	bare := env.seedPushWorkspace(t, &Workspace{Slug: "hooked", OwnerID: "user-a"}, "feat")
	hook := "#!/bin/sh\necho 'declined: protected ref, see https://user:pw@forge.example/rules' >&2\nexit 1\n"
	writeHook(t, bare, "pre-receive", hook)
	rec := env.push(t, "hooked", "feat", "", userAuth("user-a"))
	refusedWithType(t, rec, http.StatusUnprocessableEntity, "forge_rejected", "declined", "https://forge.example/rules")
	if strings.Contains(rec.Body.String(), "user:pw@") {
		t.Errorf("the answer holds the userinfo: %s", rec.Body)
	}
	if refs := bareRefs(t, bare); len(refs) != 0 {
		t.Errorf("origin received %v", refs)
	}
}

// writeHook installs an executable hook in the bare repository.
func writeHook(t *testing.T, bare, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bare, "hooks", name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TS-38-35: DNS/TCP/TLS failures map to 502 forge_unreachable and other
// failures to 502 forge_error.
// Requirement: 38-REQ-6.7
func TestBranchPushForgeUnreachableAndError_TS_38_35(t *testing.T) {
	env := newBranchPushEnv(t)
	for slug, gitURL := range map[string]string{
		"dns":    "http://hub-branch-push.invalid/r.git",
		"closed": "http://127.0.0.1:1/r.git",
	} {
		env.seedPushWorkspace(t, &Workspace{Slug: slug, OwnerID: "user-a"}, "feat")
		setGitURL(t, env, slug, gitURL)
		refusedWithType(t, env.push(t, slug, "feat", "", userAuth("user-a")), http.StatusBadGateway, "forge_unreachable")
	}

	forge := newStandInForge(t)
	forge.setAnswer(func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		_, _ = w.Write([]byte("this is not the git protocol\n"))
		return true
	})
	env.seedPushWorkspace(t, &Workspace{Slug: "garbled", OwnerID: "user-a"}, "feat")
	setGitURL(t, env, "garbled", forge.repoURL())
	refusedWithType(t, env.push(t, "garbled", "feat", "", userAuth("user-a")), http.StatusBadGateway, "forge_error")
	if got := len(*env.pushes); got != 3 {
		t.Errorf("the shared push was called %d times; want 3 push attempts", got)
	}
}

// TS-38-36: A trunk that cannot be opened or has no origin answers 500 with no
// event and no push-attempt warning.
// Requirement: 38-REQ-6.8
func TestBranchPushTrunkUnavailable_TS_38_36(t *testing.T) {
	env := newBranchPushEnv(t)
	logs := captureBranchPushLog(t)
	env.seedWorkspace(t, &Workspace{Slug: "no-trunk", GitURL: "https://github.com/org/x", OwnerID: "user-a", Status: "active", CloneStatus: "ready"})
	env.seedPushWorkspace(t, &Workspace{Slug: "no-origin", OwnerID: "user-a"}, "feat")
	pushGit(t, filepath.Join(env.root, "no-origin", "trunk"), "remote", "remove", "origin")

	for _, slug := range []string{"no-trunk", "no-origin"} {
		assertRefusal(t, env.push(t, slug, "feat", "", userAuth("user-a")), http.StatusInternalServerError, "internal server error", "")
		assertLockFree(t, slug)
	}
	if evs := env.branchEvents(); len(evs) != 0 {
		t.Errorf("%d events; want none", len(evs))
	}
	if out := logs.String(); strings.Contains(out, `"error_type"`) {
		t.Errorf("a push-attempt warning was logged: %s", out)
	}
}
