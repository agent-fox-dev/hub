package sandboxes

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// fakeLister holds key names per scope and records every call; it has no way
// to hand out a value.
type fakeLister struct {
	names map[Scope][]string
	err   error
	calls []Scope
}

func newFakeLister() *fakeLister { return &fakeLister{names: map[Scope][]string{}} }

func (f *fakeLister) add(scopeType, owner string, keys ...string) {
	f.names[Scope{scopeType, owner}] = append(f.names[Scope{scopeType, owner}], keys...)
}

func (f *fakeLister) ListSecrets(ownerType, ownerID string) ([]secrets.SecretEntry, error) {
	f.calls = append(f.calls, Scope{ownerType, ownerID})
	if f.err != nil {
		return nil, f.err
	}
	var out []secrets.SecretEntry
	for _, k := range f.names[Scope{ownerType, ownerID}] {
		out = append(out, secrets.SecretEntry{Key: k})
	}
	return out, nil
}

// The chain is the workspace, its org when the owner is a member, and the
// owner's user scope; an unknown workspace or a missing table is an error.
func TestResolutionChain(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.addWorkspace("a", "O", "active")
	env.addWorkspace("b", "P", "active")
	env.addMember("O", owner)
	testutil.SeedOrg(t, env.db, "P", "P", owner)
	for slug, want := range map[string][]Scope{
		"a":       {{"workspace", "a"}, {"org", "O"}, {"user", owner}},
		"b":       {{"workspace", "b"}, {"user", owner}},
		"widgets": {{"workspace", "widgets"}, {"user", owner}},
	} {
		if got, err := ResolutionChain(env.db, slug); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ResolutionChain(%s) = (%v, %v); want %v", slug, got, err, want)
		}
	}
	if _, err := ResolutionChain(env.db, "nosuch"); err == nil {
		t.Error("an unknown workspace returned no error")
	}
	testutil.Exec(t, env.db, `DROP TABLE org_members`)
	if _, err := ResolutionChain(env.db, "a"); err == nil {
		t.Error("a missing org_members table returned no error")
	}
}

// The presence check lists names only, matches ignoring case, reports every
// missing name in profile order with the scope types searched and no owner or
// org id, and passes a lister error on as it is.
func TestCheckRequiredSecrets(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.addWorkspace("teamed", "org-77", "active")
	env.addMember("org-77", owner)
	lister := newFakeLister()
	lister.add("workspace", "widgets", "git_pat")
	lister.add("user", owner, "B_KEY")
	lister.add("org", "org-77", "C_KEY")

	if err := CheckRequiredSecrets(lister, env.db, "widgets", []string{"GIT_PAT", "b_key"}); err != nil || len(lister.calls) != 2 {
		t.Errorf("present names: error %v, %d scopes listed; want nil and 2", err, len(lister.calls))
	}
	if err := CheckRequiredSecrets(lister, env.db, "widgets", nil); err != nil {
		t.Errorf("no required secrets: %v", err)
	}
	err := CheckRequiredSecrets(lister, env.db, "widgets", []string{"C_KEY", "A_KEY", "B_KEY", "D_KEY"})
	var missing *MissingSecretsError
	if !errors.As(err, &missing) || !reflect.DeepEqual(missing.Missing, []string{"C_KEY", "A_KEY", "D_KEY"}) || !reflect.DeepEqual(missing.Searched, []string{"workspace", "user"}) {
		t.Fatalf("error = %v; want C_KEY, A_KEY, D_KEY missing after workspace, user", err)
	}
	if !strings.Contains(err.Error(), "C_KEY, A_KEY, D_KEY (searched scopes: workspace, user)") || strings.Contains(err.Error(), owner) || strings.Contains(err.Error(), "widgets") {
		t.Errorf("message = %q", err)
	}
	err = CheckRequiredSecrets(lister, env.db, "teamed", []string{"C_KEY", "Z_KEY"})
	if !errors.As(err, &missing) || !reflect.DeepEqual(missing.Missing, []string{"Z_KEY"}) || !reflect.DeepEqual(missing.Searched, []string{"workspace", "org", "user"}) || strings.Contains(err.Error(), "org-77") {
		t.Errorf("teamed: error = %v; want Z_KEY missing after workspace, org, user without the org id", err)
	}

	failing := newFakeLister()
	failing.err = errors.New("boom")
	if err := CheckRequiredSecrets(failing, env.db, "widgets", []string{"A"}); !errors.Is(err, failing.err) || errors.As(err, &missing) {
		t.Errorf("a lister error gave %v; want it wrapped", err)
	}
	if err := CheckRequiredSecrets(lister, env.db, "nosuch", []string{"A"}); err == nil || errors.As(err, &missing) {
		t.Errorf("an unknown workspace gave %v; want a lookup error", err)
	}
}

// resolveSecrets reads values down the chain, the first scope wins, optional
// names found nowhere are left out, required ones are a secretMissingError
// naming the keys and the scope types, and no reader is an error.
func TestResolveSecrets(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.plantSecret("user", owner, "GIT_PAT", "USER-VALUE")
	env.plantSecret("user", owner, "extra_token", "extra-value")
	got, err := resolveSecrets(env.secrets, env.db, "widgets", []string{"GIT_PAT"}, []string{"EXTRA_TOKEN", "ABSENT"})
	if err != nil || !reflect.DeepEqual(got, map[string]string{"GIT_PAT": plantedSecret, "EXTRA_TOKEN": "extra-value"}) {
		t.Errorf("resolveSecrets = (%v, %v); want the workspace's GIT_PAT and the user's EXTRA_TOKEN", got, err)
	}
	if got, err := resolveSecrets(nil, env.db, "widgets", nil, nil); err != nil || len(got) != 0 {
		t.Errorf("nothing to resolve = (%v, %v)", got, err)
	}
	if _, err := resolveSecrets(nil, env.db, "widgets", []string{"X"}, nil); err == nil {
		t.Error("no reader returned no error")
	}
	var missing *secretMissingError
	_, err = resolveSecrets(env.secrets, env.db, "widgets", []string{"GIT_PAT", "NOPE", "ALSO"}, nil)
	if !errors.As(err, &missing) || err.Error() != "secret_missing: NOPE, ALSO (searched scopes: workspace, user)" {
		t.Errorf("missing names: %v", err)
	}
	testutil.Exec(t, env.db, `DROP TABLE secrets`)
	if _, err := resolveSecrets(env.secrets, env.db, "widgets", []string{"GIT_PAT"}, nil); err == nil || errors.As(err, &missing) {
		t.Errorf("a store error gave %v; want it as it is", err)
	}
}
