package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// scriptedRemote is a remote client over a recording provider, so that a test can
// make its Create and the Close of its boxes fail the way a remote's do.
type scriptedRemote struct{ *recordingProvider }

func (scriptedRemote) List(context.Context) ([]gateway.Info, error) { return nil, nil }

// scriptedEnv is hub over a scriptedRemote, with hub's gateway serving.
type scriptedEnv struct {
	*backendEnv
	gw   *testGateway
	logs *logBuffer
}

func newScriptedEnv(t *testing.T) *scriptedEnv {
	t.Helper()
	logs := captureDefaultLog(t)
	rec := newRecordingProvider()
	svc, err := Start(remoteLeaseConfig("https://gw.example"), noPodman(t), WithGatewayFactory(stubFactory(scriptedRemote{rec}, nil).dial))
	if err != nil {
		t.Fatalf("Start over the scripted remote: %v", err)
	}
	env := newBackendEnv(t, leaseEnvOptions{service: svc, recording: rec})
	return &scriptedEnv{backendEnv: env, gw: env.startGateway(), logs: logs}
}

// activeLease issues a lease of profile tools and creates its box through hub's
// gateway; it returns the lease id, its token and the caller's handle.
func (e *scriptedEnv) activeLease() (id, token string, box sandbox.Sandbox) {
	e.t.Helper()
	id, token = e.issueToken(`{"profile":"tools"}`)
	return id, token, createVia(e.t, e.gw.dial(e.t, token))
}

// refusalRecords counts the error records that name sandbox.gateway_token.
func refusalRecords(t *testing.T, logs *logBuffer) int {
	t.Helper()
	n := 0
	for _, rec := range recordsOf(t, logs) {
		if rec["level"] == "ERROR" && strings.Contains(fmt.Sprint(rec), "sandbox.gateway_token") {
			n++
		}
	}
	return n
}

// notFoundForms are the ways a destroy can say the box is gone.
func notFoundForms() map[string]error {
	return map[string]error{
		"gatewayd.ErrNotHeld wrapped":         fmt.Errorf("gatewayd: destroy: %w", gatewayd.ErrNotHeld),
		"gRPC NotFound":                       status.Error(codes.NotFound, "no such sandbox"),
		"gRPC NotFound wrapped by the client": fmt.Errorf("gateway http://127.0.0.1:9: destroy: %w", status.Error(codes.NotFound, "no such sandbox")),
		"gRPC NotFound wrapped twice, joined": errors.Join(errors.New("also"), fmt.Errorf("again: %w", status.Error(codes.NotFound, "gone"))),
	}
}

// refusalForms are the ways a remote call says the remote refused hub's token; the
// text of each holds a secret of the lease.
func refusalForms() map[string]error {
	return map[string]error{
		"Unauthenticated status":     status.Error(codes.Unauthenticated, "refused "+plantedSecret),
		"PermissionDenied status":    status.Error(codes.PermissionDenied, "refused "+plantedSecret),
		"gateway.ErrUnauthenticated": fmt.Errorf("%w: gateway https://gw.example refused the token (create): %s", gateway.ErrUnauthenticated, plantedSecret),
		"PermissionDenied wrapped":   fmt.Errorf("gateway https://gw.example: destroy: %w", status.Error(codes.PermissionDenied, "refused "+plantedSecret)),
	}
}

// The two helpers read the status of an error the way the code does.
func TestIsNotFoundAndIsRemoteRefusal(t *testing.T) {
	for name, err := range notFoundForms() {
		if !isNotFound(err) || isRemoteRefusal(err) {
			t.Errorf("%s: isNotFound = %t, isRemoteRefusal = %t; want true, false", name, isNotFound(err), isRemoteRefusal(err))
		}
	}
	for name, err := range refusalForms() {
		if isNotFound(err) || !isRemoteRefusal(err) {
			t.Errorf("%s: isNotFound = %t, isRemoteRefusal = %t; want false, true", name, isNotFound(err), isRemoteRefusal(err))
		}
	}
	for name, err := range map[string]error{
		"nil": nil, "plain": errors.New("boom"), "Unavailable": status.Error(codes.Unavailable, "down"),
		"Internal": status.Error(codes.Internal, "bug"), "context": context.DeadlineExceeded,
		"capacity": fmt.Errorf("%w: full", gateway.ErrCapacity),
	} {
		if isNotFound(err) || isRemoteRefusal(err) {
			t.Errorf("%s: isNotFound = %t, isRemoteRefusal = %t; want false, false", name, isNotFound(err), isRemoteRefusal(err))
		}
	}
}

// TS-29-31: A remote not-found, whether the sentinel gatewayd uses or a gRPC
// NotFound status, counts as destroyed wherever hub destroys a box: the caller's
// Close, DELETE and the expiry sweep end the lease as after a successful destroy.
// Requirement: 29-REQ-7.5
func TestRemoteNotFoundCountsAsDestroyed_TS_29_31(t *testing.T) {
	for name, form := range notFoundForms() {
		t.Run(name, func(t *testing.T) {
			env := newScriptedEnv(t)

			// The caller's Close.
			id, token, box := env.activeLease()
			env.prov.setCloseErr(form)
			if err := box.Close(context.Background()); err != nil {
				t.Errorf("Close answered %v; want it to succeed for a box that is gone", err)
			}
			if lease := env.lease(id); lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "client" {
				t.Errorf("Close: lease state %q, end_reason %v; want destroyed by the client", lease.State, lease.EndReason)
			}
			if _, ok := lookupToken(env.backendEnv, token); ok {
				t.Error("Close: the token still works")
			}

			// DELETE.
			env.prov.setCloseErr(nil)
			id, token, _ = env.activeLease()
			env.prov.setCloseErr(form)
			if code, body := env.deleteLease(id); code != http.StatusNoContent {
				t.Errorf("DELETE answered %d: %s; want 204", code, body)
			}
			if lease := env.lease(id); lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "api" {
				t.Errorf("DELETE: lease state %q, end_reason %v; want destroyed by the api", lease.State, lease.EndReason)
			}
			if _, ok := lookupToken(env.backendEnv, token); ok {
				t.Error("DELETE: the token still works")
			}

			// The expiry sweep.
			env.prov.setCloseErr(nil)
			id, _, _ = env.activeLease()
			env.prov.setCloseErr(form)
			env.clock.Advance(2 * time.Hour)
			if err := env.gw.sweeper.Sweep(context.Background()); err != nil {
				t.Errorf("Sweep returned %v", err)
			}
			if lease := env.lease(id); lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "expired" {
				t.Errorf("Sweep: lease state %q, end_reason %v; want destroyed by expiry", lease.State, lease.EndReason)
			}
			for _, rec := range recordsOf(t, env.logs) {
				if strings.Contains(fmt.Sprint(rec["msg"]), "destroying a box failed") {
					t.Errorf("a not-found was logged as a failed destroy: %v", rec)
				}
			}
			if n := refusalRecords(t, env.logs); n != 0 {
				t.Errorf("%d records name sandbox.gateway_token; want 0", n)
			}
		})
	}
}

// TS-29-32: A call the remote refuses with Unauthenticated or PermissionDenied
// reaches the caller as Unavailable with the scrubbed message and logs one error
// that names sandbox.gateway_token; a Create leaves the lease failed, a destroy
// leaves it active and DELETE answers 502.
// Requirement: 29-REQ-7.6
func TestRemoteRefusalReachesTheCallerAsUnavailable_TS_29_32(t *testing.T) {
	for name, form := range refusalForms() {
		t.Run(name, func(t *testing.T) {
			env := newScriptedEnv(t)
			wantRecords := 0
			noSecret := func(step, text string) {
				t.Helper()
				if strings.Contains(text, plantedSecret) {
					t.Errorf("%s holds a secret: %s", step, text)
				}
			}

			// A Create, called on the backend itself: it answers Unavailable, and the
			// lease fails with the scrubbed message.
			env.prov.mu.Lock()
			env.prov.createErr = form
			env.prov.mu.Unlock()
			id := env.issue(`{"profile":"tools"}`)
			_, err := env.create(id, sandbox.Spec{})
			wantCode(t, err, codes.Unavailable)
			noSecret("the backend's error", err.Error())
			if lease := env.lease(id); lease.State != "failed" || lease.Error == nil {
				t.Errorf("lease state %q, error %v; want failed with a message", lease.State, lease.Error)
			} else {
				noSecret("the lease's error", *lease.Error)
			}
			if wantRecords++; refusalRecords(t, env.logs) != wantRecords {
				t.Errorf("after a refused Create, %d records name sandbox.gateway_token; want %d", refusalRecords(t, env.logs), wantRecords)
			}

			// The same through hub's gateway and the real client.
			id, token := env.issueToken(`{"profile":"tools"}`)
			_, err = env.gw.dial(t, token).Create(context.Background(), sandbox.Spec{Image: "ignored"})
			wantCode(t, err, codes.Unavailable)
			noSecret("the caller's error", err.Error())
			if lease := env.lease(id); lease.State != "failed" {
				t.Errorf("lease state %q; want failed", lease.State)
			}
			if wantRecords++; refusalRecords(t, env.logs) != wantRecords {
				t.Errorf("after a second refused Create, %d records name sandbox.gateway_token; want %d", refusalRecords(t, env.logs), wantRecords)
			}

			// A destroy: DELETE answers 502, the lease stays active, and one record is logged.
			env.prov.mu.Lock()
			env.prov.createErr = nil
			env.prov.mu.Unlock()
			id, token, _ = env.activeLease()
			env.prov.setCloseErr(form)
			rec := env.deleteRecorded(id)
			if rec.Code != http.StatusBadGateway || errorType(t, rec) != "sandbox_provider_error" {
				t.Fatalf("DELETE answered %d (%s); want 502 sandbox_provider_error: %s", rec.Code, errorType(t, rec), rec.Body)
			}
			noSecret("the DELETE response", rec.Body.String())
			if lease := env.lease(id); lease.State != "active" || lease.EndReason != nil {
				t.Errorf("lease state %q, end_reason %v after the refused DELETE; want active", lease.State, lease.EndReason)
			}
			if _, ok := lookupToken(env.backendEnv, token); !ok {
				t.Error("the token stopped working")
			}
			if wantRecords++; refusalRecords(t, env.logs) != wantRecords {
				t.Errorf("after a refused DELETE, %d records name sandbox.gateway_token; want %d", refusalRecords(t, env.logs), wantRecords)
			}

			// The caller's Close, on a lease of its own (after a failed DELETE the gateway
			// no longer holds the box, and a Close finds nothing to do): an error, the
			// lease stays active, one more record.
			env.prov.setCloseErr(nil)
			id, _, box := env.activeLease()
			env.prov.setCloseErr(form)
			err = box.Close(context.Background())
			if err == nil {
				t.Fatal("Close succeeded although the remote refused the destroy")
			}
			noSecret("the Close error", err.Error())
			if lease := env.lease(id); lease.State != "active" {
				t.Errorf("lease state %q after the refused Close; want active", lease.State)
			}
			if wantRecords++; refusalRecords(t, env.logs) != wantRecords {
				t.Errorf("after a refused Close, %d records name sandbox.gateway_token; want %d", refusalRecords(t, env.logs), wantRecords)
			}
			for _, rec := range recordsOf(t, env.logs) {
				noSecret("a log record", fmt.Sprint(rec))
			}
			if got := len(env.eventsOf(audit.EventSandboxDestroy)); got != 0 {
				t.Errorf("hub.sandbox.destroy events = %d; want 0", got)
			}
		})
	}

	t.Run("a provider that is not remote is left alone", func(t *testing.T) {
		logs := captureDefaultLog(t)
		env := newBackendEnv(t, leaseEnvOptions{})
		env.prov.mu.Lock()
		env.prov.createErr = status.Error(codes.Unauthenticated, "the engine says no")
		env.prov.mu.Unlock()
		_, err := env.create(env.issue(`{"profile":"tools"}`), sandbox.Spec{})
		if err == nil {
			t.Fatal("Create succeeded")
		}
		if code := status.Code(err); code == codes.Unavailable || code == codes.Unauthenticated {
			t.Errorf("the error has code %s; want it to stay a plain error of the provider", code)
		}
		if n := refusalRecords(t, logs); n != 0 {
			t.Errorf("a provider that is not remote logged %d records naming sandbox.gateway_token; want 0", n)
		}
	})

	t.Run("other failures log nothing about the token", func(t *testing.T) {
		env := newScriptedEnv(t)
		env.prov.mu.Lock()
		env.prov.createErr = status.Error(codes.Unavailable, "remote down")
		env.prov.mu.Unlock()
		id := env.issue(`{"profile":"tools"}`)
		_, err := env.create(id, sandbox.Spec{})
		if err == nil {
			t.Fatal("Create succeeded")
		}
		if n := refusalRecords(t, env.logs); n != 0 {
			t.Errorf("a remote that is down logged %d records naming sandbox.gateway_token; want 0", n)
		}
	})
}

// TS-29-33: The remote sees one Create for each Create a caller makes, a failed one
// included, and none is retried.
// Requirement: 29-REQ-7.7
func TestRemoteCreateIsNeverRetried_TS_29_33(t *testing.T) {
	env := newRemoteEnv(t, leaseEnvOptions{})
	gw := env.startGateway()
	before := env.prov.creates()

	_, token1 := env.issueToken(`{"profile":"tools"}`)
	createVia(t, gw.dial(t, token1))

	env.prov.mu.Lock()
	env.prov.createErr = errors.New("the remote's backend failed")
	env.prov.mu.Unlock()
	id2, token2 := env.issueToken(`{"profile":"tools"}`)
	if _, err := gw.dial(t, token2).Create(context.Background(), sandbox.Spec{Image: "ignored"}); err == nil {
		t.Fatal("a Create that failed at the remote succeeded")
	}
	if lease := env.lease(id2); lease.State != "failed" {
		t.Errorf("lease state %q; want failed", lease.State)
	}

	time.Sleep(300 * time.Millisecond) // a retry would be in by now
	if got := env.prov.creates() - before; got != 2 {
		t.Errorf("the remote's backend saw %d Creates for two caller Creates; want 2", got)
	}
}
