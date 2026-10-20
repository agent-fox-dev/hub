package sandboxes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// smokeEnv is the stack the gateway paths run through, with nothing of it
// replaced but the provider underneath: the lease routes of cmd/af-hub over a
// real lease service and SQLite store, the secrets store, the authenticator, the
// wrapping backend and a gatewayd on a free loopback port, the real gateway
// client, and the DuckDB-backed audit emitter. The provider is the recording one
// over AgentKit's local provider.
type smokeEnv struct {
	*backendEnv
	gw      *testGateway
	auditDB *sql.DB
}

func newSmokeEnv(t *testing.T) *smokeEnv {
	t.Helper()
	auditDB, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatalf("audit.OpenDB: %v", err)
	}
	t.Cleanup(func() { auditDB.Close() })
	if err := audit.InitSchema(auditDB); err != nil {
		t.Fatalf("audit.InitSchema: %v", err)
	}
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig(), emitter: audit.NewEmitter(audit.NewStore(auditDB))})
	return &smokeEnv{backendEnv: env, gw: env.startGateway(), auditDB: auditDB}
}

// smokeEvent is a row of the audit database.
type smokeEvent struct {
	actorType, actorID, resourceType, resourceID, action, workspace string
	metadata                                                        map[string]any
}

// events reads the events of one type from the audit database, oldest first.
func (e *smokeEnv) events(eventType string) []smokeEvent {
	e.t.Helper()
	rows, err := e.auditDB.Query(`SELECT actor_type, actor_id, resource_type, resource_id, action, workspace, metadata
		FROM hub_audit_events WHERE event_type = ? ORDER BY timestamp, id`, eventType)
	if err != nil {
		e.t.Fatalf("reading the audit database: %v", err)
	}
	defer rows.Close()
	var out []smokeEvent
	for rows.Next() {
		var ev smokeEvent
		var meta string
		if err := rows.Scan(&ev.actorType, &ev.actorID, &ev.resourceType, &ev.resourceID, &ev.action, &ev.workspace, &meta); err != nil {
			e.t.Fatalf("scanning an event: %v", err)
		}
		if err := json.Unmarshal([]byte(meta), &ev.metadata); err != nil {
			e.t.Fatalf("metadata %q: %v", meta, err)
		}
		out = append(out, ev)
	}
	return out
}

// get reads a lease over HTTP.
func (e *smokeEnv) get(id string) map[string]any {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET one answered %d: %s", rec.Code, rec.Body)
	}
	return jsonObject(e.t, rec)
}

// issue posts a lease over HTTP and returns the 201 body.
func (e *smokeEnv) post() map[string]any {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/workspaces/widgets/sandboxes", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
	}
	return jsonObject(e.t, rec)
}

// dial connects the real client to the URL the lease gave.
func (e *smokeEnv) dial(url, token string) *gateway.Provider {
	e.t.Helper()
	p, err := gateway.Dial(context.Background(), url, gateway.Options{Token: token})
	if err != nil {
		e.t.Fatalf("gateway.Dial(%s): %v", url, err)
	}
	e.t.Cleanup(func() { _ = p.Close() })
	return p
}

// createFor creates a box as the holder of a lease would. The spec the client
// sends is the client's to choose and hub ignores all of it, but the client
// itself refuses an empty image when its own build has no published image to
// fall back on, which a development build has not, so it names one.
func createFor(t *testing.T, p *gateway.Provider) (sandbox.Sandbox, error) {
	t.Helper()
	return p.Create(context.Background(), sandbox.Spec{Image: "ignored"})
}

// TS-27-61 (smoke): A caller gets a lease over HTTP and creates a box through
// hub's gateway with the real client.
// Path: 27-PATH-1. Requirements: 27-REQ-6.1, 27-REQ-4.6, 27-REQ-5.6
func TestSmoke_LeaseThenCreateBox_PATH_1_TS_27_61(t *testing.T) {
	env := newSmokeEnv(t)

	posted := env.post()
	id, token, url := posted["id"].(string), posted["token"].(string), posted["gateway_url"].(string)
	if posted["state"] != "leased" || !strings.HasPrefix(token, "sbl_") || url != env.gw.URL() {
		t.Fatalf("POST body: state %v, token %q, gateway_url %q; want a leased lease with its token and the gateway's URL %s", posted["state"], token, url, env.gw.URL())
	}

	sb, err := createFor(t, env.dial(url, token))
	if err != nil {
		t.Fatalf("Create through the gateway: %v", err)
	}

	got := env.prov.lastSpec()
	if got.Image != "registry.example/tools:1" || got.Network.Mode != sandbox.NetworkNone || got.TTL != time.Hour ||
		got.Resources != (sandbox.Resources{CPUs: 1, MemoryBytes: 512 << 20, PIDs: 100}) {
		t.Errorf("the provider got %+v; want the lease's image, network, resources and a TTL of the hour left", got)
	}
	wantEnv := map[string]string{"SECRET_ENV": "envval", "GIT_PAT": plantedSecret}
	if !reflect.DeepEqual(got.Env, wantEnv) {
		t.Errorf("Env = %v; want the fixed env plus the resolved secret %v", got.Env, wantEnv)
	}
	if got.Labels["hub.lease"] != id || got.Labels["hub.workspace"] != "widgets" {
		t.Errorf("Labels = %v; want hub.lease and hub.workspace", got.Labels)
	}
	if reported := sb.Spec().Env; reported["GIT_PAT"] != "[redacted]" {
		t.Errorf("the spec the client got back has Env %v; want the values redacted", reported)
	}

	read := env.get(id)
	if read["state"] != "active" || read["sandbox_id"] != sb.ID() {
		t.Errorf("GET one: state %v, sandbox_id %v; want active with the box %s", read["state"], read["sandbox_id"], sb.ID())
	}
	events := env.events(audit.EventSandboxCreate)
	if len(events) != 1 {
		t.Fatalf("hub.sandbox.create events = %d; want 1", len(events))
	}
	if ev := events[0]; ev.workspace != "widgets" || ev.resourceType != "sandbox" || ev.resourceID != id || ev.actorType != "lease" || ev.actorID != leaseOwner ||
		ev.metadata["sandbox_id"] != sb.ID() || ev.metadata["image"] != "registry.example/tools:1" || ev.metadata["network"] != "none" {
		t.Errorf("the create event is %+v", ev)
	}
}

// TS-27-62 (smoke): A required secret deleted after POST fails the lease at Create.
// Path: 27-PATH-2. Requirement: 27-REQ-5.3
func TestSmoke_SecretDeletedAfterPost_PATH_2_TS_27_62(t *testing.T) {
	env := newSmokeEnv(t)
	posted := env.post()
	id, token, url := posted["id"].(string), posted["token"].(string), posted["gateway_url"].(string)
	client := env.dial(url, token)

	if err := env.secrets.DeleteSecret("workspace", "widgets", "GIT_PAT"); err != nil {
		t.Fatalf("deleting the secret: %v", err)
	}
	_, err := createFor(t, client)
	if err == nil {
		t.Fatal("Create succeeded without its required secret")
	}
	// gatewayd hands the backend's FailedPrecondition on as Unavailable, with the text.
	if status.Code(err) != codes.Unavailable {
		t.Errorf("Create error has code %s; want Unavailable, as gatewayd answers a backend error", status.Code(err))
	}
	msg := err.Error()
	for _, part := range []string{"secret_missing:", "GIT_PAT", "workspace", "user"} {
		if !strings.Contains(msg, part) {
			t.Errorf("the error %q does not contain %q", msg, part)
		}
	}
	read := env.get(id)
	if read["state"] != "failed" || read["error"] == nil || !strings.HasPrefix(read["error"].(string), "secret_missing:") {
		t.Errorf("GET one: state %v, error %v; want failed with the secret_missing message", read["state"], read["error"])
	}
	if events := env.events(audit.EventSandboxFailed); len(events) != 1 || events[0].metadata["lease_id"] != id || events[0].metadata["error"] != read["error"] {
		t.Errorf("hub.sandbox.failed events = %+v; want one with the lease and the message", events)
	}
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0", n)
	}
	if _, err := client.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("the RPC after the failure = %v; want ErrUnauthenticated", err)
	}
}

// TS-27-63 (smoke): DELETE on an active lease destroys the box end to end.
// Path: 27-PATH-3. Requirement: 27-REQ-8.1
func TestSmoke_DeleteDestroysTheBox_PATH_3_TS_27_63(t *testing.T) {
	env := newSmokeEnv(t)
	posted := env.post()
	id, token, url := posted["id"].(string), posted["token"].(string), posted["gateway_url"].(string)
	client := env.dial(url, token)
	sb, err := createFor(t, client)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none", live)
	}
	read := env.get(id)
	if read["state"] != "destroyed" || read["end_reason"] != "api" {
		t.Errorf("GET one: state %v, end_reason %v; want destroyed by the api", read["state"], read["end_reason"])
	}
	if _, err := client.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("a call with the revoked token = %v; want ErrUnauthenticated", err)
	}
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); err == nil {
		t.Error("a relayed call on the destroyed box succeeded")
	}
	events := env.events(audit.EventSandboxDestroy)
	if len(events) != 1 || events[0].metadata["reason"] != "api" || events[0].metadata["sandbox_id"] != sb.ID() ||
		events[0].actorType != "api_key" || events[0].actorID != leaseOwner {
		t.Errorf("hub.sandbox.destroy events = %+v; want one for the box with the HTTP credential's actor", events)
	}
}

// TS-27-64 (smoke): The lease holder closes its box through the gateway.
// Path: 27-PATH-4. Requirement: 27-REQ-7.3
func TestSmoke_ClientClosesTheBox_PATH_4_TS_27_64(t *testing.T) {
	env := newSmokeEnv(t)
	posted := env.post()
	id, token, url := posted["id"].(string), posted["token"].(string), posted["gateway_url"].(string)
	sb, err := createFor(t, env.dial(url, token))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none", live)
	}
	read := env.get(id)
	if read["state"] != "destroyed" || read["end_reason"] != "client" {
		t.Errorf("GET one: state %v, end_reason %v; want destroyed by the client", read["state"], read["end_reason"])
	}
	events := env.events(audit.EventSandboxDestroy)
	if len(events) != 1 || events[0].metadata["reason"] != "client" || events[0].metadata["sandbox_id"] != sb.ID() ||
		events[0].actorType != "lease" || events[0].actorID != leaseOwner {
		t.Errorf("hub.sandbox.destroy events = %+v; want one with reason client and the lease as the actor", events)
	}
}

// TS-27-65 (smoke): Hub refuses to start with an unsafe gateway configuration:
// the section is loaded, validated and started as af-hub does, and the failures
// that name sandbox.listen and sandbox.external_url are collected together
// before anything is bound.
// Path: 27-PATH-5. Requirements: 27-REQ-1.5, 27-REQ-1.6
func TestSmoke_UnsafeGatewayRefusesStart_PATH_5_TS_27_65(t *testing.T) {
	addr := "0.0.0.0:" + strings.Split(freeAddr(t), ":")[1]
	path := writeConfigFile(t, t.TempDir(), fmt.Sprintf(`[sandbox]
provider = "podman"
listen   = %q

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "30m"
`, addr))

	built := 0
	svc, err := Init(slog.New(slog.NewTextHandler(io.Discard, nil)), path, func(podman.Options) (sandbox.Provider, error) {
		built++
		return newRecordingProvider(), nil
	})
	if err == nil || svc != nil {
		t.Fatalf("Init = (%v, %v); want a refusal", svc, err)
	}
	for _, want := range []string{"sandbox.listen", "loopback", "sandbox.external_url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not contain %q:\n%v", want, err)
		}
	}
	if built != 0 {
		t.Errorf("the provider was built %d times; want 0: a static failure comes before the engine", built)
	}
	if !bindable(addr) {
		t.Errorf("%s is bound after the refusal", addr)
	}
}

// The same refusal through the real af-hub: it exits non-zero with the message,
// and nothing listens on the gateway's address.
// Path: 27-PATH-5 through cmd/af-hub.
func TestAfHubBinary_UnsafeGatewayRefusesStart_PATH_5(t *testing.T) {
	bin := buildAfHub(t)
	addr := "0.0.0.0:" + strings.Split(freeAddr(t), ":")[1]
	env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider = "podman"
listen   = %q

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "30m"
`, addr))
	bootstrapAfHub(t, bin, env)

	output, exit := env.run(t, bin, 30*time.Second)
	if exit <= 0 {
		t.Fatalf("af-hub exited with status %d; want a refusal\n%s", exit, output)
	}
	for _, want := range []string{"sandbox.listen", "sandbox.external_url"} {
		if !strings.Contains(output, want) {
			t.Errorf("the output does not name %s:\n%s", want, output)
		}
	}
	if !bindable(addr) {
		t.Errorf("%s is bound after the refusal", addr)
	}
	if env.listening() {
		t.Error("something listens on the hub's port after the refusal")
	}
}

// A key pair that cannot be loaded stops the real af-hub: it exits with a failure,
// names both fields and the loader's complaint, and binds nothing.
// Requirement: 27-REQ-1.8
func TestAfHubBinary_UnloadableKeyPairRefusesStart(t *testing.T) {
	bin := buildAfHub(t)
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a PEM file"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	engine, _ := writeFakeEngine(t)
	env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider      = "podman"
engine_binary = %q
listen        = %q
tls_cert      = %q
tls_key       = %q

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "30m"
`, engine, addr, garbage, garbage))
	bootstrapAfHub(t, bin, env)

	output, exit := env.run(t, bin, 30*time.Second)
	if exit <= 0 {
		t.Fatalf("af-hub exited with status %d; want a refusal\n%s", exit, output)
	}
	for _, want := range []string{"sandbox.tls_cert", "sandbox.tls_key", "cannot load the key pair"} {
		if !strings.Contains(output, want) {
			t.Errorf("the output does not contain %q:\n%s", want, output)
		}
	}
	if !bindable(addr) {
		t.Errorf("%s is bound after the refusal", addr)
	}
	if env.listening() {
		t.Error("something listens on the hub's port after the refusal")
	}
}

// An address that is taken stops the real af-hub: it exits with a failure and names
// sandbox.listen.
// Requirement: 27-REQ-2.2
func TestAfHubBinary_TakenGatewayAddressRefusesStart(t *testing.T) {
	bin := buildAfHub(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	engine, _ := writeFakeEngine(t)
	env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider      = "podman"
engine_binary = %q
listen        = %q

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "30m"
`, engine, taken.Addr().String()))
	bootstrapAfHub(t, bin, env)

	output, exit := env.run(t, bin, 30*time.Second)
	if exit <= 0 {
		t.Fatalf("af-hub exited with status %d; want a refusal\n%s", exit, output)
	}
	for _, want := range []string{"sandbox.listen", "cannot bind the sandbox gateway"} {
		if !strings.Contains(output, want) {
			t.Errorf("the output does not contain %q:\n%s", want, output)
		}
	}
	if env.listening() {
		t.Error("something listens on the hub's port after the refusal")
	}
}

// The real af-hub serves a gateway: a lease issued over HTTP shows the address it
// was configured on, the real client connects to it with the lease's token, and
// the token stops working when the lease is deleted.
// Path: 27-PATH-1 and 27-PATH-3 through cmd/af-hub, as far as a fake engine goes.
func TestAfHubBinary_ServesTheGateway_PATH_1(t *testing.T) {
	bin := buildAfHub(t)
	engine, _ := writeFakeEngine(t)
	addr := freeAddr(t)
	env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider      = "podman"
engine_binary = %q
listen        = %q

[[sandbox.profiles]]
name    = "tools"
default = true
image   = "registry.example/tools:1"
ttl     = "30m"
`, engine, addr))
	token := bootstrapAfHub(t, bin, env)
	startAfHub(t, bin, env)

	db := openHubDB(t, env)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at)
		VALUES ('widgets', 'https://example.com/widgets.git', 'user-1', 'active', 'ready', ?, ?)`, now, now); err != nil {
		t.Fatalf("inserting a workspace: %v", err)
	}

	status, body := callHub(t, env, http.MethodPost, "/api/v1/workspaces/widgets/sandboxes", token, `{}`)
	if status != http.StatusCreated {
		t.Fatalf("POST: status %d: %s", status, body)
	}
	var posted map[string]any
	if err := json.Unmarshal([]byte(body), &posted); err != nil {
		t.Fatalf("POST body is not JSON: %v: %s", err, body)
	}
	id, leaseToken, url := posted["id"].(string), posted["token"].(string), posted["gateway_url"].(string)
	if url != "http://"+addr {
		t.Fatalf("gateway_url = %q; want http://%s, the configured address", url, addr)
	}

	client, err := gateway.Dial(context.Background(), url, gateway.Options{Token: leaseToken})
	if err != nil {
		t.Fatalf("gateway.Dial against the running af-hub: %v", err)
	}
	defer client.Close()
	if boxes, err := client.List(context.Background()); err != nil || len(boxes) != 0 {
		t.Errorf("List = (%v, %v); want no boxes yet", boxes, err)
	}

	status, body = callHub(t, env, http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/"+id, token, "")
	if status != http.StatusOK || !strings.Contains(body, `"gateway_url":"http://`+addr+`"`) {
		t.Errorf("GET one: status %d: %s; want the gateway_url on the lease", status, body)
	}
	if status, body := callHub(t, env, http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, token, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE: status %d: %s", status, body)
	}
	if _, err := client.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("List with the revoked token = %v; want ErrUnauthenticated", err)
	}
}

// cmd/af-hub is the one place the gateway is started and handed to the lease
// service, and a real box cannot be made by the fake engine the binary tests use,
// so the wiring that matters for DELETE is checked in the source: the gateway is
// built after the lease table exists and set running (spec 28: after the recovery
// of the leases a crash left, and the first sweep) before the HTTP server serves,
// and the lease service gets its address and its way to destroy boxes.
// Path: 27-PATH-1, 27-PATH-3.
func TestAfHubMain_WiresTheGateway(t *testing.T) {
	src := readRepoFile(t, "cmd/af-hub/main.go")
	positions := map[string]int{}
	for _, token := range []string{
		"sandboxes.InitSchema(", "sandboxes.NewGateway(", "gateway.Run(ctx)", "GatewayURL: ", "Boxes:", "sandboxes.RegisterLeaseRoutes(", "runServer(server.Start,",
	} {
		i := strings.Index(src, token)
		if i < 0 {
			t.Fatalf("cmd/af-hub/main.go does not contain %q", token)
		}
		positions[token] = i
	}
	order := []string{"sandboxes.InitSchema(", "sandboxes.NewGateway(", "gateway.Run(ctx)", "GatewayURL: ", "sandboxes.RegisterLeaseRoutes(", "runServer(server.Start,"}
	for i := 1; i < len(order); i++ {
		if positions[order[i-1]] >= positions[order[i]] {
			t.Errorf("main.go has %q before %q; want the gateway started after the lease table and before the server serves", order[i], order[i-1])
		}
	}
	if positions["Boxes:"] < positions["sandboxes.NewGateway("] || positions["Boxes:"] > positions["sandboxes.RegisterLeaseRoutes("] {
		t.Error("main.go does not give the lease service the gateway to destroy boxes with before it mounts the routes")
	}
}
