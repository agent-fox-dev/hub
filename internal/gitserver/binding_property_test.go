package gitserver

import (
	"net/http"
	"strings"
	"testing"
)

// TS-33-52 (git server): API key and admin token git requests cause no
// binding query: with the binding table dropped every answer equals the one
// before. Requirement: 33-REQ-9.2
func TestGitBinding_NoQueryForKeysAndAdmin_TS_33_52(t *testing.T) {
	env := newBindingGitEnv(t)
	type answer struct {
		code int
		body string
	}
	creds := []string{"af_key_userA", "af_admin_test789", "af_key_userB"}
	services := []string{"git-upload-pack", "git-receive-pack"}
	before := map[string]answer{}
	for _, cred := range creds {
		for _, svc := range services {
			code, body := env.infoRefs(t, "s", svc, cred)
			before[cred+svc] = answer{code, body}
		}
	}
	if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	for _, cred := range creds {
		for _, svc := range services {
			code, body := env.infoRefs(t, "s", svc, cred)
			if want := before[cred+svc]; code == http.StatusInternalServerError || code != want.code || body != want.body {
				t.Errorf("%s %s: (%d, %q) after the drop; want (%d, %q)", cred, svc, code, body, want.code, want.body)
			}
		}
	}
}

// TS-33-53 (property, git server): whatever a bound token is allowed on a
// repository, an unbound PAT of the same user and scopes is allowed with the
// same answer. Requirement: 33-REQ-9.3
func TestGitBinding_PropertyNeverWidens_TS_33_53(t *testing.T) {
	env := newBindingGitEnv(t)
	env.seedWorkspace(t, "o", "https://github.com/org/o", "user-b", "org-1", "ready")
	env.initWorkspaceRepo(t, "o")

	scopeSets := map[string][]string{"none": nil, "read": {"git:read"}, "write": {"git:write"}, "both": {"git:read", "git:write"}}
	allowed := 0
	for name, scopes := range scopeSets {
		seedGitPAT(t, env.db, "b"+name, "user-a", scopes...)
		seedGitPAT(t, env.db, "u"+name, "user-a", scopes...)
		bindGitToken(t, env.db, "b"+name, "s")
		for _, slug := range []string{"s", "x", "o", "ghost"} {
			for _, svc := range []string{"git-upload-pack", "git-receive-pack"} {
				bcode, bbody := env.infoRefs(t, slug, svc, "af_pat_b"+name)
				ucode, ubody := env.infoRefs(t, slug, svc, "af_pat_u"+name)
				if bcode >= 200 && bcode < 300 {
					allowed++
					if ucode != bcode || ubody != bbody {
						t.Errorf("%s %s %s: bound (%d, %q) but unbound (%d, %q)", name, slug, svc, bcode, bbody, ucode, ubody)
					}
				}
				if slug == "s" && (bcode != ucode || bbody != ubody) {
					t.Errorf("%s %s %s on its own repository: bound (%d, %q); want (%d, %q)", name, slug, svc, bcode, bbody, ucode, ubody)
				}
			}
		}
	}
	if allowed == 0 {
		t.Error("the bound tokens were allowed nothing; the sweep proves nothing")
	}
}

// TS-33-55 (git server): a token whose bound workspace was deleted gets the
// pkt-line 404 repository not found for every repository.
// Requirement: 33-REQ-9.5
func TestGitBinding_DeletedWorkspaceFailsClosed_TS_33_55(t *testing.T) {
	env := newBindingGitEnv(t)
	seedGitPAT(t, env.db, "orphan", "user-a", "git:read", "git:write")
	bindGitToken(t, env.db, "orphan", "s")
	if _, err := env.db.Exec(`DELETE FROM workspaces WHERE slug = 's'`); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"s", "x"} {
		code, body := env.infoRefs(t, slug, "git-upload-pack", "af_pat_orphan")
		if code != http.StatusNotFound || !strings.Contains(body, "ERR repository not found") {
			t.Errorf("fetch %s: (%d, %q); want pkt-line 404 repository not found", slug, code, body)
		}
	}
}
