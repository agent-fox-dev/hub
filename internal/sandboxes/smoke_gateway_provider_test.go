package sandboxes

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// tarBytes returns a tar archive of the files.
func tarBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TS-29-47 (smoke): An operator starts hub against a reachable remote gateway. The
// configuration is read from a file with the token in the environment, the real
// gateway.Dial reaches the in-process remote, start-up creates nothing there, logs
// one record with the provider and the profiles, and hub's gateway and HTTP server
// then listen.
// Path: 29-PATH-1. Requirements: 29-REQ-1.1, 29-REQ-5.2
func TestSmoke_OperatorStartsHubAgainstARemote_TS_29_47(t *testing.T) {
	prov := newRecordingProvider()
	remote := startRemoteGateway(t, prov)
	path := remoteConfigFile(t, remote.url(), remoteToken)
	logger, logs := newCapturedLogger()
	reaps, rpcs := prov.reapCount(), remote.rpcs()

	svc, err := Init(logger, path, noPodman(t), WithGatewayFactory(DialGateway))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if got := remote.rpcs() - rpcs; got != 2 {
		t.Errorf("the remote saw %d RPCs during start-up; want 2 (Dial's capabilities and one List)", got)
	}
	if prov.creates() != 0 || len(prov.liveBoxes()) != 0 || prov.reapCount() != reaps {
		t.Errorf("start-up cost the remote's backend %d Creates, %d live boxes, %d Reaps; want none", prov.creates(), len(prov.liveBoxes()), prov.reapCount()-reaps)
	}
	for _, box := range prov.boxes {
		if box.closes != 0 {
			t.Error("start-up destroyed a box on the remote")
		}
	}
	records := logRecords(t, logs)
	if len(records) != 1 || !slices.Equal(attributeKeys(records[0]), []string{"profiles", "provider"}) || records[0]["provider"] != "gateway" {
		t.Errorf("start-up logged %v; want one record with the attributes provider (gateway) and profiles", records)
	}
	if strings.Contains(logs.String(), remoteToken) {
		t.Errorf("the start-up log holds the token: %s", logs)
	}

	// Then hub's gateway and its HTTP server bind.
	env := newLeaseEnv(t, leaseEnvOptions{service: svc})
	gw, err := startGateway(svc, GatewayDeps{DB: env.db, Secrets: env.secrets, Now: env.clock.Now}, gatewaySeams{})
	if err != nil {
		t.Fatalf("StartGateway: %v", err)
	}
	stopAfter(t, gw)
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(gw.URL(), "http://"), 5*time.Second)
	if err != nil {
		t.Fatalf("hub's gateway at %s is not listening: %v", gw.URL(), err)
	}
	_ = conn.Close()
	server := httptest.NewServer(newHandlerEcho(svc))
	t.Cleanup(server.Close)
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/sandbox-profiles", nil)
	header, err := json.Marshal(apiKeyAuth())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Auth", string(header))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET sandbox-profiles: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), remoteToken) {
		t.Errorf("the profile listing answered %d: %s", resp.StatusCode, body)
	}
}

// TS-29-48 (smoke): An agent leases a box that runs on the remote gateway, uses it
// and deletes it, through the real lease API, SQLite store, hub's gateway, backend
// and clients, over a remote gatewayd on AgentKit's local provider.
// Path: 29-PATH-2. Requirements: 29-REQ-7.1, 29-REQ-7.4
func TestSmoke_AgentLeasesUsesAndDeletesARemoteBox_TS_29_48(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	lease := env.issueLease("tools")
	id, token := lease["id"].(string), lease["token"].(string)

	client, err := gateway.Dial(context.Background(), lease["gateway_url"].(string), gateway.Options{Token: token})
	if err != nil {
		t.Fatalf("gateway.Dial(lease.gateway_url): %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	box, err := client.Create(ctx, sandbox.Spec{Image: "ignored"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The box is on the remote with the lease's image, TTL and merged environment.
	spec := env.prov.lastSpec()
	if env.prov.creates() != 1 || spec.Image != "registry.example/tools:1" || spec.TTL != time.Hour ||
		spec.Env["SECRET_ENV"] != "envval" || spec.Env["GIT_PAT"] != plantedSecret {
		t.Errorf("the remote saw %d Creates, image %q, TTL %v, env %v", env.prov.creates(), spec.Image, spec.TTL, spec.Env)
	}
	if live := env.remoteLive(); len(live) != 1 || live[0] != box.ID() {
		t.Errorf("the remote holds %v; want the box %s", live, box.ID())
	}

	// The agent uses it: files in, a proxy tool out, through both gateways.
	if _, err := box.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarBytes(t, map[string]string{"hello.txt": "from the agent"}))}); err != nil {
		t.Fatalf("Sync through the two gateways: %v", err)
	}
	tools, err := box.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatalf("Tools through the two gateways: %v", err)
	}
	read := false
	for _, tool := range tools {
		if tool.Name != "read_file" {
			continue
		}
		read = true
		if res := tool.Execute(ctx, []byte(`{"path":"hello.txt"}`)); !res.OK || !strings.Contains(fmt.Sprint(res.Data), "from the agent") {
			t.Errorf("read_file through the two gateways: %+v", res)
		}
	}
	if !read {
		t.Error("the sandbox has no read_file tool")
	}

	// DELETE destroys it on the remote and revokes the token.
	if code, body := env.deleteLease(id); code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}
	if live := env.remoteLive(); len(live) != 0 {
		t.Errorf("after DELETE the remote holds %v", live)
	}
	if _, err := gateway.Dial(ctx, lease["gateway_url"].(string), gateway.Options{Token: token}); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("dialling with the deleted lease's token answered %v; want Unauthenticated", err)
	}
}

// TS-29-49 (smoke): A remote error that carries hub's token and the remote's host is
// scrubbed from the lease, the caller's error, the HTTP bodies, the audit events (in
// the real DuckDB store) and the log.
// Path: 29-PATH-3. Requirement: 29-REQ-8.2
func TestSmoke_RemoteErrorWithTokenAndHostIsScrubbedEndToEnd_TS_29_49(t *testing.T) {
	auditDB, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatalf("audit.OpenDB: %v", err)
	}
	t.Cleanup(func() { auditDB.Close() })
	if err := audit.InitSchema(auditDB); err != nil {
		t.Fatalf("audit.InitSchema: %v", err)
	}
	env := &remoteLeaseEnv{}
	env.remoteEnv = newRemoteEnv(t, leaseEnvOptions{emitter: audit.NewEmitter(audit.NewStore(auditDB), nil)})
	env.gw = env.startGateway()
	env.prov.mu.Lock()
	env.prov.createErr = fmt.Errorf("create failed: token %s refused at %s (%s)", remoteToken, env.remote.url(), env.remote.host())
	env.prov.mu.Unlock()

	lease := env.issueLease("tools")
	id := lease["id"].(string)
	_, callerErr := env.gw.dial(t, lease["token"].(string)).Create(context.Background(), sandbox.Spec{Image: "ignored"})
	if callerErr == nil {
		t.Fatal("Create succeeded against a failing remote")
	}
	read := env.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())

	got := env.lease(id)
	if got.State != "failed" || got.Error == nil || !strings.Contains(*got.Error, "[redacted]") {
		t.Fatalf("lease state %q, error %v; want failed with a scrubbed message", got.State, got.Error)
	}
	rows, err := auditDB.Query(`SELECT event_type, metadata FROM hub_audit_events ORDER BY timestamp, id`)
	if err != nil {
		t.Fatalf("reading the audit database: %v", err)
	}
	defer rows.Close()
	var events []string
	failed := 0
	for rows.Next() {
		var eventType, metadata string
		if err := rows.Scan(&eventType, &metadata); err != nil {
			t.Fatal(err)
		}
		events = append(events, eventType+" "+metadata)
		if eventType == audit.EventSandboxFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("%d hub.sandbox.failed events in the audit database; want 1", failed)
	}
	surfaces := map[string]string{
		"the caller's error":  callerErr.Error(),
		"the lease's error":   *got.Error,
		"the lease over HTTP": read.Body.String(),
		"the audit events":    strings.Join(events, "\n"),
		"the log":             env.logs.String(),
	}
	for what, text := range surfaces {
		for _, secret := range []string{remoteToken, env.remote.url(), env.remote.host()} {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds %q:\n%s", what, secret, text)
			}
		}
	}
}

// TS-29-50 (smoke): A remote outage fails a lease with a scrubbed error, and hub's
// next Create works once the remote is back at the same address, with the same
// client: the factory was called once.
// Path: 29-PATH-4. Requirements: 29-REQ-9.2, 29-REQ-9.6
func TestSmoke_OutageFailsALeaseAndHubRecovers_TS_29_50(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	addr := env.remote.addr
	env.remote.stop()

	down := env.issueLease("tools")
	downID := down["id"].(string)
	if _, err := env.gw.dial(t, down["token"].(string)).Create(context.Background(), sandbox.Spec{Image: "ignored"}); err == nil {
		t.Fatal("Create succeeded against a remote that is down")
	}
	failed := env.lease(downID)
	if failed.State != "failed" || failed.Error == nil {
		t.Fatalf("lease state %q, error %v; want failed with a message", failed.State, failed.Error)
	}
	for _, secret := range []string{remoteToken, env.remote.url(), addr} {
		if strings.Contains(*failed.Error, secret) {
			t.Errorf("the lease's error holds %q: %s", secret, *failed.Error)
		}
	}

	env.remote.restart()
	var box sandbox.Sandbox
	within(t, "a Create to succeed on the restarted remote", func() bool {
		lease := env.issueLease("data")
		sb, err := env.gw.dial(t, lease["token"].(string)).Create(context.Background(), sandbox.Spec{Image: "ignored"})
		box = sb
		return err == nil
	})
	if live := env.prov.live(); len(live) != 1 || live[0] != box.ID() {
		t.Errorf("the restarted remote holds %v; want the new box %s", live, box.ID())
	}
	if n := env.factory.callCount(); n != 1 {
		t.Errorf("the gateway factory was called %d times; want 1", n)
	}
}

// TS-29-51 (smoke): Hub's shutdown destroys the active box through the remote client
// and only then closes the client. The client is the real gateway.Dial one, wrapped to
// log the order.
// Path: 29-PATH-5. Requirement: 29-REQ-9.7
func TestSmoke_ShutdownDestroysThroughTheClientThenClosesIt_TS_29_51(t *testing.T) {
	log := &callLog{}
	prov := newRecordingProvider()
	remote := startRemoteGateway(t, prov)
	factory := func(ctx context.Context, url string, opts gateway.Options) (RemoteClient, error) {
		client, err := DialGateway(ctx, url, opts)
		if err != nil {
			return nil, err
		}
		return orderedReal{RemoteClient: client, log: log}, nil
	}
	svc, err := Start(remoteLeaseConfig(remote.url()), noPodman(t), WithGatewayFactory(factory))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env := newBackendEnv(t, leaseEnvOptions{service: svc, recording: prov})
	gw := env.startGateway()
	id, token := env.issueToken(`{"profile":"tools"}`)
	box := createVia(t, gw.dial(t, token))
	if len(prov.live()) != 1 {
		t.Fatalf("the remote holds %v; want the box", prov.live())
	}

	if err := gw.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got, want := log.all(), []string{"destroy " + box.ID(), "close"}; !slices.Equal(got, want) {
		t.Errorf("the calls were %v; want %v", got, want)
	}
	if live := prov.live(); len(live) != 0 {
		t.Errorf("the box is still on the remote: %v", live)
	}
	if l := env.lease(id); l.State != "destroyed" || l.EndReason == nil || *l.EndReason != "shutdown" {
		t.Errorf("lease state %q, end_reason %v; want destroyed by the shutdown", l.State, l.EndReason)
	}
}

// orderedReal wraps the real remote client to log the destroy of its boxes and its
// Close, and passes everything else on.
type orderedReal struct {
	RemoteClient
	log *callLog
}

func (o orderedReal) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	box, err := o.RemoteClient.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return orderedBox{Sandbox: box, log: o.log}, nil
}

func (o orderedReal) Close() error {
	o.log.add("close")
	if closer, ok := o.RemoteClient.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// remoteSection is the [sandbox] section of a config file for provider gateway,
// with the token as an environment reference, and one profile.
func remoteSection(url string) string {
	return fmt.Sprintf(`[sandbox]
provider      = "gateway"
gateway_url   = %q
gateway_token = "${AGENTKIT_GATEWAY_TOKEN}"

[[sandbox.profiles]]
name  = "tools"
image = "registry.example/tools:1"
ttl   = "1h"
`, url)
}

// The real binary refuses to start against a remote that refuses its token, with
// the error of 29-REQ-5.4 and without either token.
func TestAfHubBinary_RemoteGatewayThatRefusesTheTokenRefusesStart(t *testing.T) {
	const otherToken = "other-hub-token-9876543210fedcba"
	bin := buildAfHub(t)
	remote := startRemoteGateway(t, newRecordingProvider())
	t.Setenv("AGENTKIT_GATEWAY_TOKEN", otherToken)
	env := newAfHubEnv(t, remoteSection(remote.url()))

	output, exit := env.run(t, bin, time.Minute)
	if exit == 0 {
		t.Fatalf("af-hub started against a remote that refuses its token:\n%s", output)
	}
	for _, want := range []string{"sandbox:", "sandbox.provider", "sandbox.gateway_url", "sandbox.gateway_token", "refused the token"} {
		if !strings.Contains(output, want) {
			t.Errorf("the output does not contain %q:\n%s", want, output)
		}
	}
	for _, secret := range []string{otherToken, remoteToken} {
		if strings.Contains(output, secret) {
			t.Errorf("the output holds a token:\n%s", output)
		}
	}
}

// The real binary starts against the in-process remote through main's wiring
// (PATH-1), makes a box on the remote for a lease whose holder dials hub's gateway
// (PATH-2), and on SIGTERM destroys that box through the remote client and exits
// (PATH-5), with the lease ended and the token in no output.
func TestAfHubBinary_RunsSandboxesOnARemoteGateway(t *testing.T) {
	bin := buildAfHub(t)
	prov := newRecordingProvider()
	remote := startRemoteGateway(t, prov)
	t.Setenv("AGENTKIT_GATEWAY_TOKEN", remoteToken)
	env := newAfHubEnv(t, remoteSection(remote.url()))
	adminToken := bootstrapAfHub(t, bin, env)
	rpcs := remote.rpcs()
	cmd, output, exited := startAfHubProcess(t, bin, env)

	if got := remote.rpcs() - rpcs; got < 2 {
		t.Errorf("the remote saw %d RPCs from the hub at start; want at least Dial's capabilities and the List", got)
	}
	if prov.creates() != 0 {
		t.Errorf("start-up created %d boxes on the remote", prov.creates())
	}
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/sandbox-profiles", env.port), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET sandbox-profiles: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"tools"`) {
		t.Errorf("the profile listing answered %d: %s", resp.StatusCode, body)
	}

	// A lease for the profile, and its holder creating the box through hub's gateway.
	db, err := sql.Open("sqlite", filepath.Join(env.dataDir, "afhub.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("opening the hub's database: %v", err)
	}
	defer db.Close()
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))
	if _, err := db.Exec(`UPDATE sandbox_leases SET expires_at = ?, image = 'registry.example/tools:1' WHERE id = ?`, apikit.FormatUTC(time.Now().Add(time.Hour)), leaseID(1)); err != nil {
		t.Fatal(err)
	}
	var gatewayURL string
	within(t, "the gateway's address in the output", func() bool {
		m := regexp.MustCompile(`the sandbox gateway is serving.*url=(\S+)`).FindStringSubmatch(output.String())
		if m != nil {
			gatewayURL = m[1]
		}
		return m != nil
	})
	client, err := gateway.Dial(context.Background(), gatewayURL, gateway.Options{Token: testToken(1)})
	if err != nil {
		t.Fatalf("gateway.Dial(%s): %v\n%s", gatewayURL, err, output)
	}
	t.Cleanup(func() { _ = client.Close() })
	box, err := client.Create(context.Background(), sandbox.Spec{Image: "ignored"})
	if err != nil {
		t.Fatalf("Create through the hub's gateway: %v\nhub output:\n%s", err, output)
	}
	if live := prov.live(); len(live) != 1 || live[0] != box.ID() {
		t.Fatalf("the remote holds %v; want the box %s", live, box.ID())
	}
	if spec := prov.lastSpec(); spec.Image != "registry.example/tools:1" || spec.TTL <= 0 || spec.TTL > time.Hour {
		t.Errorf("the box's image is %q and its TTL %v; want the lease's image and the time it has left", spec.Image, spec.TTL)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling af-hub: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("af-hub did not stop within 30 seconds of SIGTERM:\n%s", output)
	}
	if live := prov.live(); len(live) != 0 {
		t.Errorf("the box is still on the remote after the hub stopped: %v\nhub output:\n%s", live, output)
	}
	var state, reason string
	if err := db.QueryRow(`SELECT state, COALESCE(end_reason, '') FROM sandbox_leases WHERE id = ?`, leaseID(1)).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "destroyed" || reason != "shutdown" {
		t.Errorf("the lease reads %s/%s after SIGTERM; want destroyed/shutdown\nhub output:\n%s", state, reason, output)
	}
	if strings.Contains(output.String(), remoteToken) {
		t.Errorf("the hub's output holds the token:\n%s", output)
	}
	for _, token := range remote.tokens() {
		if token != remoteToken {
			t.Error("the remote was shown a token that is not hub's")
		}
	}
}
