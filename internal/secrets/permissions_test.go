package secrets

import (
	"testing"

	"github.com/txsvc/apikit"
)

// The package registers read and write scopes of both resources with apikit.
func TestPermissions_FourScopes(t *testing.T) {
	want := []apikit.Permission{
		{Resource: "secrets", Action: "read"}, {Resource: "secrets", Action: "write"},
		{Resource: "vars", Action: "read"}, {Resource: "vars", Action: "write"},
	}
	got := Permissions()
	if len(got) != len(want) {
		t.Fatalf("Permissions() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Permissions()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
