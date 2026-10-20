package sandboxes

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

// The names of two RPCs of the gateway protocol. The authentication of gatewayd
// answers before a request message is read, so an empty message is enough to see
// the code of a refused call.
const (
	rpcGatewayCreate = "/agentkit.sandbox.v1.Gateway/Create"
	rpcAgentHello    = "/agentkit.sandbox.v1.Agent/Hello"
)

// bearerToken sends a token the way the gateway client does.
type bearerToken string

func (b bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (bearerToken) RequireTransportSecurity() bool { return false }

// rawCall makes one RPC of the gateway protocol with a token and an empty
// message, with none of the checks the client makes first.
func rawCall(t *testing.T, gw *testGateway, token, method string) error {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///"+strings.TrimPrefix(gw.URL(), "http://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(bearerToken(token)))
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return cc.Invoke(ctx, method, &emptypb.Empty{}, &emptypb.Empty{})
}

// TS-27-18: Over real gRPC, a token hub never minted and a token whose lease has
// ended are refused with Unauthenticated on the very next RPC, the relayed calls
// on a box that exists included.
// Requirements: 27-REQ-3.2, 27-REQ-3.3
func TestGateway_RevokedAndUnknownTokens_TS_27_18(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	gw := env.startGateway()

	t.Run("a token hub never minted", func(t *testing.T) {
		unknown := testToken(99)
		if err := rawCall(t, gw, unknown, rpcGatewayCreate); status.Code(err) != codes.Unauthenticated {
			t.Errorf("Create with an unknown token = %v; want Unauthenticated", err)
		}
		if _, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: unknown}); !errors.Is(err, gateway.ErrUnauthenticated) {
			t.Errorf("Dial with an unknown token = %v; want ErrUnauthenticated", err)
		}
		if n := env.prov.creates(); n != 0 {
			t.Errorf("provider Create calls = %d; want 0", n)
		}
	})

	t.Run("a lease that was destroyed", func(t *testing.T) {
		id, token := env.issueToken(`{"profile":"tools"}`)
		client := gw.dial(t, token)
		sb := createVia(t, client)
		if err := rawCall(t, gw, token, rpcAgentHello); status.Code(err) == codes.Unauthenticated {
			t.Fatalf("the live lease was refused: %v", err)
		}

		if changed, err := env.store.DestroyLease(id, "active", "api"); err != nil || !changed {
			t.Fatalf("DestroyLease = (%v, %v)", changed, err)
		}
		if _, err := client.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) {
			t.Errorf("List = %v; want ErrUnauthenticated", err)
		}
		if _, err := client.Create(context.Background(), sandbox.Spec{Image: "x"}); !errors.Is(err, gateway.ErrUnauthenticated) {
			t.Errorf("Create = %v; want ErrUnauthenticated", err)
		}
		if err := rawCall(t, gw, token, rpcAgentHello); status.Code(err) != codes.Unauthenticated {
			t.Errorf("a relayed Agent call = %v; want Unauthenticated", err)
		}
		if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); err == nil {
			t.Error("a relayed Tools call on the box of a destroyed lease succeeded")
		}
	})

	t.Run("a lease that failed", func(t *testing.T) {
		_, token := env.issueToken(`{"profile":"tools"}`)
		client := gw.dial(t, token)
		env.prov.mu.Lock()
		env.prov.createErr = errors.New("engine refused")
		env.prov.mu.Unlock()
		if _, err := client.Create(context.Background(), sandbox.Spec{Image: "x"}); err == nil {
			t.Fatal("Create with a failing provider succeeded")
		}
		env.prov.mu.Lock()
		env.prov.createErr = nil
		env.prov.mu.Unlock()
		if _, err := client.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) {
			t.Errorf("List after the failed Create = %v; want ErrUnauthenticated", err)
		}
	})
}

// TS-27-39: A second Create under a token is refused: FailedPrecondition from the
// backend while the lease is active, which gatewayd hands the client as
// Unavailable with the message; Unauthenticated once the lease ended or failed.
// The provider is asked once.
// Requirement: 27-REQ-6.5
func TestGateway_SecondCreateIsRefused_TS_27_39(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	idA, tokenA := env.issueToken(`{"profile":"tools"}`)
	_, tokenB := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()

	clientA := gw.dial(t, tokenA)
	createVia(t, clientA)
	_, err := clientA.Create(context.Background(), sandbox.Spec{Image: "x"})
	if err == nil {
		t.Fatal("a second Create under the same token succeeded")
	}
	// gatewayd answers an error of the backend that is no refusal as Unavailable,
	// with the backend's text. The backend's own code is FailedPrecondition.
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "is active") {
		t.Errorf("second Create = %v (code %s); want a message that says the lease is active", err, status.Code(err))
	}
	if n := env.prov.creates(); n != 1 {
		t.Errorf("provider Create calls = %d; want 1", n)
	}

	if changed, err := env.store.DestroyLease(idA, "active", "api"); err != nil || !changed {
		t.Fatalf("DestroyLease = (%v, %v)", changed, err)
	}
	if _, err := clientA.Create(context.Background(), sandbox.Spec{Image: "x"}); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Create after the lease was destroyed = %v; want ErrUnauthenticated", err)
	}
	if n := env.prov.creates(); n != 1 {
		t.Errorf("provider Create calls = %d; want 1", n)
	}

	// A lease whose Create failed.
	clientB := gw.dial(t, tokenB)
	if err := env.secrets.DeleteSecret("workspace", "widgets", "GIT_PAT"); err != nil {
		t.Fatal(err)
	}
	_, err = clientB.Create(context.Background(), sandbox.Spec{Image: "x"})
	if err == nil || !strings.Contains(err.Error(), "secret_missing:") {
		t.Fatalf("Create with a missing secret = %v; want a secret_missing error", err)
	}
	if _, err := clientB.Create(context.Background(), sandbox.Spec{Image: "x"}); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Create after the lease failed = %v; want ErrUnauthenticated", err)
	}
	if n := env.prov.creates(); n != 1 {
		t.Errorf("provider Create calls = %d; want 1: the failed lease never reached the provider", n)
	}
}

// TS-27-40: However many Creates race under one token, with at most one DELETE
// among them, the provider is asked at most once, and when the lease ends
// destroyed no box of it is left alive.
// Requirement: 27-REQ-6.6
func TestGateway_ConcurrentCreatesAndDelete_TS_27_40(t *testing.T) {
	cfg := gatewayLeaseConfig()
	room := 1000.0
	cfg.MaxSandboxes = &room
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	gw := env.startGateway()

	rng := rand.New(rand.NewPCG(27, 40))
	for i := range 40 {
		creators := 1 + rng.IntN(8)
		env.prov.setCreateDelay(time.Duration(rng.IntN(6)) * time.Millisecond)
		deleteAfter := time.Duration(-1)
		if rng.IntN(3) != 0 {
			deleteAfter = time.Duration(rng.IntN(8)) * time.Millisecond
		}
		before := env.prov.creates()
		id, token := env.issueToken(`{"profile":"tools"}`)

		var wg sync.WaitGroup
		for range creators {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: token})
				if err != nil {
					return
				}
				defer p.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				_, _ = p.Create(ctx, sandbox.Spec{Image: "x"})
			}()
		}
		if deleteAfter >= 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(deleteAfter)
				env.deleteLease(id)
			}()
		}
		wg.Wait()

		if made := env.prov.creates() - before; made > 1 {
			t.Fatalf("schedule %d: the provider was asked %d times (%d creators, delete after %v); want at most 1", i, made, creators, deleteAfter)
		}
		lease := env.lease(id)
		live := 0
		for _, box := range env.prov.liveBoxes() {
			if box.spec.Labels["hub.lease"] == id {
				live++
			}
		}
		if lease.State == "destroyed" && live != 0 {
			t.Fatalf("schedule %d: the lease is destroyed and %d of its boxes are alive (%d creators, delete after %v)", i, live, creators, deleteAfter)
		}
		if lease.State == "active" && live != 1 {
			t.Fatalf("schedule %d: the lease is active and has %d live boxes; want 1", i, live)
		}
	}
}
