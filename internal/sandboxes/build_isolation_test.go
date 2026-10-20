package sandboxes

import (
	"strings"
	"testing"
)

// TS-29-46: The dial code of the remote provider (the gateway client and the gRPC
// stack under it) is in the hub's dependencies, and the static afc binary is still
// free of every agentkit-go and gRPC package and of internal/sandboxes.
// Requirement: 29-REQ-10.1
func TestAfcBuildIsolation_TS_29_46(t *testing.T) {
	const gatewayClient = "github.com/agent-fox-dev/agentkit-go/sandbox/gateway"

	hubDeps := strings.Fields(goInRepo(t, nil, "list", "-deps", "./internal/sandboxes"))
	for _, want := range []string{gatewayClient, "google.golang.org/grpc"} {
		found := false
		for _, dep := range hubDeps {
			found = found || dep == want
		}
		if !found {
			t.Errorf("internal/sandboxes does not depend on %s, so the isolation below would prove nothing", want)
		}
	}

	afcDeps := strings.Fields(goInRepo(t, []string{"CGO_ENABLED=0"}, "list", "-deps", "./cmd/afc"))
	if len(afcDeps) == 0 {
		t.Fatal("go list -deps ./cmd/afc returned no packages")
	}
	for _, dep := range afcDeps {
		if strings.Contains(dep, "agentkit-go") || strings.Contains(dep, "google.golang.org/grpc") || strings.Contains(dep, "internal/sandboxes") {
			t.Errorf("afc depends on %s", dep)
		}
	}
}
