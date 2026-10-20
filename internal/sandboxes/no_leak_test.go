package sandboxes

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// TS-29-34: Under provider gateway the scrub set holds the token, the gateway_url
// string and url.Host, besides the secrets; under podman it holds nothing extra.
// Requirement: 29-REQ-8.1
func TestScrubSet_HoldsTokenURLAndHost_TS_29_34(t *testing.T) {
	const url, host = "http://127.0.0.1:41234", "127.0.0.1:41234"
	cfg := remoteConfig(url)
	svc, err := Start(cfg, noPodman(t), WithGatewayFactory(stubFactory(newStubRemote(), nil).dial))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := svc.scrubValues()
	for _, want := range []string{testGatewayToken, url, host} {
		if !slices.Contains(got, want) {
			t.Errorf("the scrub set %q does not hold %q", got, want)
		}
	}
	text := "boom: token " + testGatewayToken + ", url " + url + ", host " + host + ", only the port :41234"
	out := scrub(text, svc.scrubValues())
	for _, leaked := range []string{testGatewayToken, url, host} {
		if strings.Contains(out, leaked) {
			t.Errorf("the scrubbed text still holds %q: %q", leaked, out)
		}
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("the scrubbed text %q has no [redacted]", out)
	}

	// With the Podman provider the same text is not altered for those values.
	podmanSvc, err := Start(validConfig(), func(podman.Options) (sandbox.Provider, error) { return newFakeProvider(), nil })
	if err != nil {
		t.Fatalf("Start with podman: %v", err)
	}
	if got := podmanSvc.scrubValues(); len(got) != 0 {
		t.Errorf("the scrub set under podman = %q; want none", got)
	}
	if out := scrub(text, podmanSvc.scrubValues()); out != text {
		t.Errorf("scrubbing under podman changed the text: %q", out)
	}
	if (*Service)(nil).scrubValues() != nil {
		t.Error("a nil service has a scrub set")
	}
}

// TS-29-35: A remote error that carries hub's token, the remote's URL and its host
// leaks into none of the surfaces: the caller's error, the lease, the HTTP
// bodies, the audit events and the log.
// Requirement: 29-REQ-8.2
func TestRemoteErrorLeaksNowhere_TS_29_35(t *testing.T) {
	env := newRemoteEnv(t, leaseEnvOptions{})
	gw := env.startGateway()
	secrets := []string{remoteToken, env.remote.url(), env.remote.host()}
	env.prov.mu.Lock()
	env.prov.createErr = fmt.Errorf("boom: %s refused at %s (%s)", remoteToken, env.remote.url(), env.remote.host())
	env.prov.mu.Unlock()

	id, token := env.issueToken(`{"profile":"tools"}`)
	_, callerErr := gw.dial(t, token).Create(context.Background(), sandbox.Spec{Image: "ignored"})
	if callerErr == nil {
		t.Fatal("Create succeeded against a failing remote")
	}
	read := env.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
	list := env.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes", "", apiKeyAuth())

	lease := env.lease(id)
	if lease.State != "failed" || lease.Error == nil || !strings.Contains(*lease.Error, "[redacted]") {
		t.Errorf("lease state %q, error %v; want failed with a scrubbed message", lease.State, lease.Error)
	}
	surfaces := map[string]string{
		"the caller's error":  callerErr.Error(),
		"the lease":           fmt.Sprintf("%+v", *lease),
		"the lease over HTTP": read.Body.String(),
		"the lease listing":   list.Body.String(),
		"the audit events":    fmt.Sprintf("%+v", env.emitter.recorded()),
		"the log":             env.logs.String(),
	}
	for what, text := range surfaces {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds %q:\n%s", what, secret, text)
			}
		}
	}
	if got := len(env.eventsOf(audit.EventSandboxFailed)); got != 1 {
		t.Errorf("hub.sandbox.failed events = %d; want 1", got)
	}
}

// Every site that turns the provider's text into output applies the scrub set: a
// failed destroy for the caller's Close, DELETE, the sweep's destroy of an overdue
// box, its destroy of an orphan, its Reap, and what the lease holds.
func TestRemoteTextIsScrubbedAtEverySite(t *testing.T) {
	env := newScriptedEnv(t)
	const host = "gw.example"
	secrets := []string{remoteToken, "https://" + host, host}
	failure := func(what string) error {
		return fmt.Errorf("%s failed: token %s at https://%s (%s) and %s", what, remoteToken, host, host, plantedSecret)
	}
	var outputs []string
	collect := func(texts ...string) { outputs = append(outputs, texts...) }

	// The caller's Close and DELETE, each on a lease of its own.
	id, _, box := env.activeLease()
	env.prov.setCloseErr(failure("close"))
	err := box.Close(context.Background())
	if err == nil {
		t.Fatal("Close succeeded")
	}
	collect(err.Error())
	rec := env.deleteRecorded(id)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
	}
	collect(rec.Body.String())

	// The sweep: an overdue lease whose destroy fails, an ended lease whose box
	// could not be destroyed (the orphan step), and a Reap that fails.
	env.prov.setCloseErr(nil)
	env.activeLease()
	orphan, _, orphanBox := env.activeLease()
	env.prov.setCloseErr(failure("destroy"))
	if err := orphanBox.Close(context.Background()); err == nil {
		t.Fatal("Close of the orphan's box succeeded")
	}
	collect(fmt.Sprint(env.lease(orphan).Error))
	// A Reap failure belongs to no lease, so it carries only the remote's values.
	env.prov.setReap(nil, fmt.Errorf("reap failed: token %s at https://%s (%s)", remoteToken, host, host))
	env.clock.Advance(2 * time.Hour)
	sweepErr := env.gw.sweeper.Sweep(context.Background())
	if sweepErr == nil {
		t.Fatal("the sweep did not report the failing Reap")
	}
	collect(sweepErr.Error(), env.logs.String())

	for _, text := range outputs {
		for _, secret := range append(secrets, plantedSecret) {
			if strings.Contains(text, secret) {
				t.Errorf("an output holds %q:\n%s", secret, text)
			}
		}
	}
	if !strings.Contains(env.logs.String(), "[redacted]") {
		t.Errorf("no log record shows a scrubbed value:\n%s", env.logs.String())
	}
}

// TS-29-36: The token is in no HTTP response, no metric and no start-up record,
// and the one start-up record has the attributes provider and profiles and no
// others.
// Requirement: 29-REQ-8.3
func TestTokenIsInNoResponseMetricOrStartRecord_TS_29_36(t *testing.T) {
	metrics := audit.NewMetrics()
	env := newRemoteEnv(t, leaseEnvOptions{metrics: metrics})
	gw := env.startGateway()
	_, token := env.issueToken(`{"profile":"tools"}`)
	createVia(t, gw.dial(t, token))

	bodies := map[string]string{}
	bodies["the profile listing"] = getProfiles(t, newHandlerEcho(env.profile), apiKeyAuth()).Body.String()
	bodies["an error body"] = env.post("no-such-workspace", `{"profile":"tools"}`, apiKeyAuth()).Body.String()
	bodies["a bad profile"] = env.post("widgets", `{"profile":"missing"}`, apiKeyAuth()).Body.String()
	bodies["a lease response"] = env.post("widgets", `{"profile":"data"}`, apiKeyAuth()).Body.String()
	bodies["the lease listing"] = env.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes", "", apiKeyAuth()).Body.String()
	rec := httptest.NewRecorder()
	metrics.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	bodies["/metrics"] = rec.Body.String()
	for what, body := range bodies {
		if body == "" {
			t.Errorf("%s is empty", what)
		}
		for _, secret := range []string{remoteToken, env.remote.url(), env.remote.host()} {
			if strings.Contains(body, secret) {
				t.Errorf("%s holds %q:\n%s", what, secret, body)
			}
		}
	}

	// The start-up record.
	remote := startRemoteGateway(t, newRecordingProvider())
	logger, logs := newCapturedLogger()
	svc, err := Init(logger, remoteConfigFile(t, remote.url(), remoteToken), noPodman(t))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if closer, ok := svc.Provider().(io.Closer); ok {
			_ = closer.Close()
		}
	})
	records := logRecords(t, logs)
	if len(records) != 1 {
		t.Fatalf("start-up wrote %d records; want 1: %s", len(records), logs.String())
	}
	if got := attributeKeys(records[0]); !slices.Equal(got, []string{"profiles", "provider"}) {
		t.Errorf("the start-up record has the attributes %v; want provider and profiles", got)
	}
	for _, secret := range []string{remoteToken, remote.url(), remote.host()} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the start-up record holds %q: %s", secret, logs.String())
		}
	}
}

// TS-29-38: For any configuration and any failure of the factory or of List, the
// error of Start holds neither the token nor URL user information.
// Requirement: 29-REQ-8.5
func TestStartErrorNeverHoldsTheTokenOrUserinfo_TS_29_38(t *testing.T) {
	r := rand.New(rand.NewPCG(29, 38))
	urls := []string{
		"https://gw.example", "https://gw.example:8443/", "http://127.0.0.1:9000", "http://gw.example:9000",
		"https://user:pw@gw.example", "https://gw.example/api", "ftp://gw.example", "https://:443", "gw.example",
	}
	for range 300 {
		token := genToken(r)
		cfg := remoteConfig(urls[r.IntN(len(urls))])
		cfg.GatewayToken = token
		if r.IntN(4) == 0 {
			cfg.Runtime = "krun"
		}
		if r.IntN(4) == 0 {
			cfg.MaxTTL = strPtr("bogus")
		}

		userinfo := fmt.Sprintf("u%d:p%d", r.IntN(1000), r.IntN(1000))
		failure := fmt.Errorf("dial https://%s@host.example:443 with %s failed; retry %s", userinfo, token, cfg.GatewayURL)
		stub := newStubRemote()
		var factory *remoteFactory
		switch r.IntN(3) {
		case 0:
			factory = stubFactory(nil, failure)
		case 1:
			stub.listErr = failure
			factory = stubFactory(stub, nil)
		default:
			stub.listBlock = false
			factory = stubFactory(stub, nil)
		}
		_, err := Start(cfg, noPodman(t), WithGatewayFactory(factory.dial))
		if err == nil {
			continue
		}
		text := err.Error()
		// A token short enough to fail validation can occur in a message by chance
		// ("6" in "16"); as in TS-29-14 only parts of eight characters count.
		if len(token) >= minGatewayTokenLength && strings.Contains(text, token) {
			t.Fatalf("the error holds the token %q:\n%v", token, err)
		}
		for i := 0; i+8 <= len(token); i++ {
			if part := token[i : i+8]; strings.Contains(text, part) {
				t.Fatalf("the error holds %q of the token %q:\n%v", part, token, err)
			}
		}
		for _, leaked := range []string{userinfo, userinfo[strings.Index(userinfo, ":")+1:] + "@", "user:pw", "pw@"} {
			if strings.Contains(text, leaked) {
				t.Fatalf("the error holds the user information %q (config url %q):\n%v", leaked, cfg.GatewayURL, err)
			}
		}
	}
}

// failingDestroyer is a BoxDestroyer that fails with err as it is.
type failingDestroyer struct{ err error }

func (f failingDestroyer) DestroyBox(context.Context, *Lease) error { return f.err }

// A Boxes or a BoxDestroyer that returns the remote's text as it is does not get it
// past the sweep or DELETE: both apply the scrub set themselves, since the scrub of
// the box's handle is not the only way provider text arrives.
func TestSweepAndDeleteScrubTheRemoteValuesThemselves(t *testing.T) {
	logs := captureDefaultLog(t)
	e := newSweepEnv(t)
	svc, err := Start(remoteLeaseConfig("https://gw.example"), noPodman(t), WithGatewayFactory(stubFactory(newStubRemote(), nil).dial))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cfg := e.sweeperConfig()
	cfg.Service = svc
	sweeper := NewSweeper(cfg)
	raw := fmt.Errorf("failed with %s at https://gw.example (gw.example)", remoteToken)
	secrets := []string{remoteToken, "https://gw.example", "gw.example"}

	// An overdue lease whose box cannot be destroyed, and an ended lease whose box is
	// left: the first sweep logs both failures.
	now := e.clock.Now()
	e.seed(1, "active", seedFields{created: now.Add(-2 * time.Hour), expires: now.Add(-time.Hour), boxID: "box-overdue"})
	e.seed(2, "destroyed", seedFields{created: now.Add(-2 * time.Hour), ended: timePtr(now.Add(-time.Minute)), boxID: "box-orphan"})
	e.boxes.setDestroyErr(raw)
	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	// The listing itself fails.
	e.boxes.mu.Lock()
	e.boxes.listErr = raw
	e.boxes.mu.Unlock()
	listErr := sweeper.Sweep(context.Background())
	if listErr == nil {
		t.Fatal("the sweep did not report the failing listing")
	}

	// DELETE of an active lease whose destroyer fails with the raw text.
	env := newScriptedEnv(t)
	id, _, _ := env.activeLease()
	env.svc.boxes = failingDestroyer{raw}
	rec := env.deleteRecorded(id)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
	}

	for what, text := range map[string]string{"the sweep's log": logs.String(), "the sweep's error": listErr.Error(), "the DELETE response": rec.Body.String()} {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds %q:\n%s", what, secret, text)
			}
		}
	}
	if n := strings.Count(logs.String(), "[redacted]"); n < 3 {
		t.Errorf("the log shows %d scrubbed values; want the overdue destroy, the orphan destroy and the listing:\n%s", n, logs.String())
	}
}
