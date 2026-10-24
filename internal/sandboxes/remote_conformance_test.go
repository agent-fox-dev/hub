package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/hub/internal/scrub"
)

// The remote's capabilities carry a note the gateway client writes, "Sandboxes run
// on the gateway at <host>, ...", and hub's gateway serves the capabilities to every
// lease holder: the snapshot must not name the remote.
func TestRemoteCapabilitiesNoteDoesNotNameTheRemote(t *testing.T) {
	stub := newStubRemote()
	stub.caps.Notes = "Container sandboxes. Sandboxes run on the gateway at gw.example, which accepts a TTL of at most 24h0m0s and holds at most 16 sandboxes at once."
	cfg := remoteConfig("https://gw.example:8443")
	svc, err := Start(cfg, noPodman(t), WithGatewayFactory(stubFactory(stub, nil).dial))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for what, notes := range map[string]string{
		"the snapshot":  svc.Capabilities().Notes,
		"the backend's": newBackend(backendConfig{Service: svc}).Capabilities().Notes,
		"the notes of the stub's own answer, unchanged": stub.caps.Notes,
	} {
		named := strings.Contains(notes, "gw.example")
		if what == "the notes of the stub's own answer, unchanged" {
			if !named {
				t.Fatal("the test's note does not name the remote")
			}
			continue
		}
		if named {
			t.Errorf("%s name the remote: %q", what, notes)
		}
		if !strings.Contains(notes, "Container sandboxes") {
			t.Errorf("%s lost the rest of the note: %q", what, notes)
		}
	}

	// The real client, against the in-process remote.
	env := newRemoteEnv(t, leaseEnvOptions{})
	if notes := env.profile.Capabilities().Notes; strings.Contains(notes, "127.0.0.1") || notes == "" {
		t.Errorf("the notes of the snapshot are %q; want them without the remote's address", notes)
	}
}

// The scrub set holds the URL's host with its port and the bare host name: DNS and
// TLS errors name the host without the port.
func TestScrubSetHoldsTheBareHostName(t *testing.T) {
	svc, err := Start(remoteConfig("https://gw.example:8443"), noPodman(t), WithGatewayFactory(stubFactory(newStubRemote(), nil).dial))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	text := "lookup gw.example on 10.0.0.2:53: no such host; certificate is valid for other.example, not gw.example; dial gw.example:8443"
	out := scrub.Text(text, svc.scrubValues()...)
	for _, leaked := range []string{"gw.example"} {
		if strings.Contains(out, leaked) {
			t.Errorf("the scrubbed text still holds %q: %q", leaked, out)
		}
	}
	if !strings.Contains(out, "other.example") {
		t.Errorf("the scrub took out more than the remote's names: %q", out)
	}

	// Without a port the host and the bare name are one value.
	svc, err = Start(remoteConfig("https://gw.example"), noPodman(t), WithGatewayFactory(stubFactory(newStubRemote(), nil).dial))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := svc.scrubValues(); len(got) != 3 {
		t.Errorf("the scrub set is %q; want the token, the URL and the host once", got)
	}

	// The slice is a copy: a caller that changes it does not change the set.
	got := svc.scrubValues()
	got[0] = "changed"
	if again := svc.scrubValues(); again[0] == "changed" {
		t.Error("changing the slice scrubValues returned changed the scrub set")
	}
	if (RedactedString{}).reveal() != "" {
		t.Error("the zero RedactedString reveals something")
	}
}

// The production dialer keeps the reason a dial failed and drops the address, so
// that the text gRPC puts in the status of a failed call, which gatewayd relays to
// a lease holder as it is, does not name the remote.
func TestDialErrorsDoNotNameTheAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = dialWithoutAddress(ctx, closed)
	if err == nil {
		t.Fatal("dialling a closed port succeeded")
	}
	if strings.Contains(err.Error(), closed) || strings.Contains(err.Error(), "127.0.0.1") || !strings.Contains(err.Error(), "refused") {
		t.Errorf("the error is %q; want the reason without the address", err)
	}
	_, err = dialWithoutAddress(ctx, "no-such-host.invalid:443")
	if err == nil {
		t.Fatal("dialling a name that does not exist succeeded")
	}
	if strings.Contains(err.Error(), "no-such-host") {
		t.Errorf("the error is %q; want it without the host name", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err = dialWithoutAddress(cancelled, closed); err == nil || strings.Contains(err.Error(), closed) {
		t.Errorf("a cancelled dial answered %v", err)
	}

	// Through the relay: a caller whose remote went away gets an error without the
	// remote's address.
	env := newRemoteLeaseEnv(t)
	lease := env.issueLease("tools")
	box := createVia(t, env.gw.dial(t, lease["token"].(string)))
	env.remote.stop()
	for i := range 3 {
		_, err := box.Tools(context.Background(), sandbox.ToolOptions{})
		if err == nil {
			continue
		}
		for _, secret := range []string{env.remote.host(), env.remote.url(), remoteToken} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("call %d: the error of a relayed call names %q: %v", i, secret, err)
			}
		}
	}
}

// A refused token at the sweep's Reap is logged once with the key to look at, as
// at a Create or a destroy; any other failure of Reap logs nothing about the token.
func TestReapRefusalIsLoggedNamingTheToken(t *testing.T) {
	for name, form := range refusalForms() {
		t.Run(name, func(t *testing.T) {
			env := newScriptedEnv(t)
			env.prov.setReap(nil, form)
			if err := env.gw.sweeper.Sweep(context.Background()); err == nil {
				t.Fatal("the sweep did not report the failing Reap")
			}
			if n := refusalRecords(t, env.logs); n != 1 {
				t.Errorf("%d records name sandbox.gateway_token; want 1\n%s", n, env.logs)
			}
		})
	}
	env := newScriptedEnv(t)
	env.prov.setReap(nil, errors.New("the remote's reap failed"))
	_ = env.gw.sweeper.Sweep(context.Background())
	if n := refusalRecords(t, env.logs); n != 0 {
		t.Errorf("a Reap that failed otherwise logged %d records naming sandbox.gateway_token; want 0", n)
	}
}

// The error of closing the client is scrubbed like every other text of the remote:
// Shutdown hands it to main, which logs it.
func TestServiceClose_ErrorIsScrubbed(t *testing.T) {
	boom := fmt.Errorf("gateway https://gw.example: destroy: connection to gw.example:8443 lost; token %s", remoteToken)
	env := newClosingEnv(t, func(rec *recordingProvider) RemoteClient {
		return orderedRemote{scriptedRemote: scriptedRemote{rec}, log: &callLog{}, closeErr: boom}
	})
	for what, err := range map[string]error{
		"Shutdown":       env.gw.Shutdown(context.Background()),
		"Close":          env.svc.Close(),
		"a second Close": env.svc.Close(),
	} {
		if err == nil {
			t.Errorf("%s returned no error", what)
			continue
		}
		for _, secret := range []string{remoteToken, "https://gw.example", "gw.example"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error holds %q: %v", what, secret, err)
			}
		}
		if !strings.Contains(err.Error(), "[redacted]") {
			t.Errorf("%s: the error shows no scrubbed value: %v", what, err)
		}
	}
}

// A nil Service is not remote.
func TestIsRemote_NilService(t *testing.T) {
	var svc *Service
	if svc.isRemote() {
		t.Error("a nil service is remote")
	}
	if got := svc.remoteFailure("create", errors.New("x"), "x"); got == nil || got.Error() != "x" {
		t.Errorf("remoteFailure on a nil service answered %v", got)
	}
}
