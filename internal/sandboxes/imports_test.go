package sandboxes

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
)

// The gatewayd surface the gateway builds on. Pinning the signatures here
// verifies, at compile time, the external API the spec could only assume, and
// a change in agentkit-go breaks the build of this package.
var (
	_ gatewayd.Authenticator                                = func(context.Context, string) (string, bool) { return "", false }
	_                                                       = gatewayd.Config{Authenticate: func(context.Context, string) (string, bool) { return "", false }}
	_ func(context.Context) (string, bool)                  = gatewayd.PrincipalFromContext
	_ func(*gatewayd.Server, net.Listener) error            = (*gatewayd.Server).Serve
	_ func(*gatewayd.Server, context.Context, string) error = (*gatewayd.Server).Destroy
	_ func(*gatewayd.Server, context.Context) error         = (*gatewayd.Server).Shutdown
	_ error                                                 = gatewayd.ErrNotHeld
)

// goInRepo runs the go tool in the repository root with env added to the
// environment and returns its combined output.
func goInRepo(t *testing.T, env []string, args ...string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available")
	}
	cmd := exec.Command(goBin, args...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TS-27-60: The module requires the agentkit-go gatewayd module through a
// replace into the sibling checkout, the whole module builds, and the static
// afc binary keeps clear of gatewayd, gRPC and internal/sandboxes.
// Requirement: 27-REQ-10.2
func TestGatewayModuleAndAfcBoundary_TS_27_60(t *testing.T) {
	const gatewayd = "github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"

	t.Run("gatewayd is required and replaced by the sibling checkout", func(t *testing.T) {
		out := goInRepo(t, nil, "list", "-m", gatewayd)
		if !strings.Contains(out, "=> ../agentkit-go/sandbox/gatewayd") {
			t.Errorf("go list -m %s = %q; want a replace => ../agentkit-go/sandbox/gatewayd", gatewayd, strings.TrimSpace(out))
		}
	})

	t.Run("go build ./... succeeds", func(t *testing.T) {
		goInRepo(t, nil, "build", "./...")
	})

	t.Run("afc does not depend on gatewayd, grpc or internal/sandboxes", func(t *testing.T) {
		deps := strings.Fields(goInRepo(t, []string{"CGO_ENABLED=0"}, "list", "-deps", "./cmd/afc", "./internal/cli"))
		if len(deps) == 0 {
			t.Fatal("go list -deps returned no packages")
		}
		for _, dep := range deps {
			if strings.Contains(dep, "agentkit-go") || strings.Contains(dep, "google.golang.org/grpc") || strings.Contains(dep, "internal/sandboxes") {
				t.Errorf("afc depends on %s", dep)
			}
		}
	})
}
