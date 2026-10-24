package forge

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/cli"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// These smoke tests run the hub as main.go wires the webhook: the real forge routes and ingress
// on one router (the ingress outside the API group), the real workspaces and secrets tables and
// the delivery log, the real audit store and emitter (DuckDB) with the SSE manager and the audit
// query routes, and, where the path starts at a person, the real afc command tree against the
// hub's HTTP server. No forge is involved: the ingress never contacts one. The pruner and the
// wiring of main.go are run in the real af-hub binary.

// webhookSmoke is a hub and its audit store.
type webhookSmoke struct {
	t     *testing.T
	db    *sql.DB // the hub's database
	store *secrets.Store
	audit *sql.DB // the audit store's DuckDB database
	hub   *httptest.Server
}

func newWebhookSmoke(t *testing.T) *webhookSmoke {
	t.Helper()
	isolateForgeEnv(t)
	db, store := smokeDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	s := &webhookSmoke{t: t, db: db, store: store}

	auditDB, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auditDB.Close() })
	if err := audit.InitSchema(auditDB); err != nil {
		t.Fatal(err)
	}
	s.audit = auditDB
	auditStore := audit.NewStore(auditDB)
	sse := audit.NewSSEManager(5)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go sse.Run(done)

	router := echo.New()
	router.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := router.Group("/api/v1")
	api.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get("Authorization") == "Bearer "+smokeKey {
				apikit.SetAuthInfo(c, &apikit.AuthInfo{UserID: "user-1", CredentialType: "api_key"})
			}
			return next(c)
		}
	})
	cfg := Config{
		DB:                db,
		ResolveOriginAuth: func(slug string) (transport.AuthMethod, error) { return workspace.ResolveCloneAuth(store, slug) },
		ResolveUpstreamAuth: func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveUpstreamAuthMethod(store, slug)
		},
		GetVariable: store.GetVariableValue,
		Audit:       audit.NewEmitter(auditStore, sse),
		Secrets:     store,
		ExternalURL: "https://hub.example.com",
	}
	RegisterRoutes(api, cfg)
	audit.RegisterAuditQueryRoutes(api, auditStore, sse, db)
	MountWebhookIngress(router, cfg)

	s.hub = httptest.NewServer(router)
	t.Cleanup(s.hub.Close)
	return s
}

// workspace adds a workspace of user-1, active and cloned, a standard one on gitURL.
func (s *webhookSmoke) workspace(slug, gitURL string) {
	s.t.Helper()
	_, err := s.db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, upstream_url, created_at, updated_at)
		VALUES (?, ?, 'user-1', 'active', 'ready', 'standard', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, slug, gitURL)
	if err != nil {
		s.t.Fatal(err)
	}
}

// secret gives the workspace the webhook secret, as afc secrets create would.
func (s *webhookSmoke) secret(slug, value string) {
	s.t.Helper()
	if _, err := s.store.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: value}}); err != nil {
		s.t.Fatal(err)
	}
}

// afc runs the real afc command tree against the hub and returns stdout, stderr and the error.
func (s *webhookSmoke) afc(args ...string) (string, string, error) {
	s.t.Helper()
	home := s.t.TempDir()
	s.t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ak"), 0o700); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ak", "config.toml"), []byte("endpoint_url = \"\"\nuser_id = \"\"\napi_key = \"\"\n"), 0o600); err != nil {
		s.t.Fatal(err)
	}
	cmd := cli.BuildRootCommand()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"--endpoint-url", s.hub.URL, "--api-key", smokeKey}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// post sends the delivery to the hub over HTTP, as a forge would, with no credential of the hub.
func (s *webhookSmoke) post(slug string, d signedDelivery) (int, string) {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.hub.URL+"/hooks/forge/"+slug, strings.NewReader(d.body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header = d.header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// getJSON calls GET on the hub's API as user-1 and returns the status and the body.
func (s *webhookSmoke) getJSON(path string) (int, string) {
	s.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.hub.URL+"/api/v1"+path, nil)
	req.Header.Set("Authorization", "Bearer "+smokeKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// deliveryCount is how many rows the delivery log has.
func (s *webhookSmoke) deliveryCount() int {
	s.t.Helper()
	return countDeliveries(s.t, s.db)
}

// hubEvents reads the hub events of a workspace from the audit store, oldest first.
func (s *webhookSmoke) hubEvents(slug string) []auditEvent {
	s.t.Helper()
	rows, err := s.audit.Query(`SELECT event_type, actor_id, actor_type, resource_type, resource_id, action, workspace, metadata
		FROM hub_audit_events WHERE workspace = ? ORDER BY timestamp, id`, slug)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditEvent
	for rows.Next() {
		var ev auditEvent
		var metadata string
		if err := rows.Scan(&ev.EventType, &ev.ActorID, &ev.ActorType, &ev.ResourceType, &ev.ResourceID, &ev.Action, &ev.Workspace, &metadata); err != nil {
			s.t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(metadata), &ev.Metadata); err != nil {
			s.t.Fatalf("metadata %q: %v", metadata, err)
		}
		out = append(out, ev)
	}
	return out
}

// eventTypesOf lists the types of the events.
func eventTypesOf(events []auditEvent) []string {
	out := []string{}
	for _, ev := range events {
		out = append(out, ev.EventType)
	}
	return out
}

// waitForEvents waits until the audit store holds n events of the workspace; they are stored
// after the answer to the delivery, so a moment may pass.
func (s *webhookSmoke) waitForEvents(slug string, n int) []auditEvent {
	s.t.Helper()
	var events []auditEvent
	eventually(s.t, fmt.Sprintf("%d events in the audit store", n), func() bool {
		events = s.hubEvents(slug)
		return len(events) >= n
	})
	return events
}

// openStream opens GET /events?workspace=slug as user-1 and returns the audit events it carries.
func (s *webhookSmoke) openStream(slug string) <-chan audit.HubEvent {
	s.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.hub.URL+"/api/v1/events?workspace="+slug, nil)
	req.Header.Set("Authorization", "Bearer "+smokeKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("GET /events: status = %d; want 200", resp.StatusCode)
	}
	heartbeat, events := make(chan struct{}, 1), make(chan audit.HubEvent, 16)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		var name string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				switch name {
				case "heartbeat":
					select {
					case heartbeat <- struct{}{}:
					default:
					}
				case "audit_event":
					var ev audit.HubEvent
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil {
						events <- ev
					}
				}
			}
		}
	}()
	select {
	case <-heartbeat:
	case <-time.After(5 * time.Second):
		s.t.Fatal("the event stream did not open")
	}
	return events
}

// next waits for the next event of the stream.
func nextEvent(t *testing.T, events <-chan audit.HubEvent) audit.HubEvent {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("the stream carried no further event")
		return audit.HubEvent{}
	}
}

// TS-32-64: An operator sets a webhook with afc and a GitHub label delivery signed with the
// generated secret reaches the SSE stream and the audit query.
// Path: 32-PATH-1. Requirements: 32-REQ-1.1, 32-REQ-8.6
func TestSmoke_OperatorConfiguresAWebhookAndALabelReachesTheStream_TS_32_64(t *testing.T) {
	s := newWebhookSmoke(t)
	s.workspace("widgets", "https://github.com/acme/widgets")
	stream := s.openStream("widgets")

	stdout, stderr, err := s.afc("forge", "webhook", "set", "widgets")
	if err != nil {
		t.Fatalf("afc forge webhook set: %v\n%s", err, stdout)
	}
	doc, _ := decodeJSON(t, stdout).(map[string]any)
	secret, _ := doc["secret"].(string)
	if doc["configured"] != true || doc["url"] != "https://hub.example.com/hooks/forge/widgets" || !regexp.MustCompile(hexSecret).MatchString(secret) {
		t.Fatalf("afc printed %s; want the configured document with a 64-hex secret and the ingress URL", stdout)
	}
	if strings.Count(strings.TrimSpace(stderr), "\n") != 0 || !strings.Contains(stderr, "again") {
		t.Errorf("stderr = %q; want one line that says the secret is not shown again", stderr)
	}

	status, body := s.post("widgets", githubDelivery(secret, "gh-1", "issues", issuesPayload("labeled", "acme/widgets")))
	if status != http.StatusAccepted || body != `{"outcome":"relayed"}` {
		t.Fatalf("the delivery: %d %s; want 202 relayed", status, body)
	}
	if n := s.deliveryCount(); n != 1 {
		t.Errorf("%d rows in the delivery log; want 1", n)
	}

	// On the stream: the webhook.set of the command, then the label.
	if ev := nextEvent(t, stream); ev.EventType != audit.EventForgeWebhookSet || ev.Metadata["generated"] != true {
		t.Errorf("first event on the stream = %+v; want hub.forge.webhook.set with generated true", ev)
	}
	ev := nextEvent(t, stream)
	if ev.EventType != audit.EventForgeIssueLabeled || ev.Workspace != "widgets" || ev.Metadata["target"] != "origin" ||
		ev.Metadata["number"] != float64(7) || ev.Metadata["label"] != "af:ready" ||
		!reflect.DeepEqual(ev.Metadata["labels"], []any{"bug", "af:ready"}) {
		t.Errorf("second event on the stream = %+v; want hub.forge.issue.labeled of issue 7 with the label af:ready, bound to the origin", ev)
	}

	// In the audit query and the audit store.
	events := s.waitForEvents("widgets", 2)
	if got := eventTypesOf(events); !reflect.DeepEqual(got, []string{audit.EventForgeWebhookSet, audit.EventForgeIssueLabeled}) {
		t.Errorf("hub_audit_events holds %v", got)
	}
	if events[0].Metadata["generated"] != true || len(events[0].Metadata) != 1 {
		t.Errorf("the webhook.set event holds %v; want only generated: true", events[0].Metadata)
	}
	code, audited := s.getJSON("/audit?workspace=widgets")
	if code != http.StatusOK || !strings.Contains(audited, audit.EventForgeIssueLabeled) || !strings.Contains(audited, audit.EventForgeWebhookSet) {
		t.Errorf("GET /audit: %d %s; want both events", code, audited)
	}
	if strings.Contains(audited, secret) {
		t.Errorf("GET /audit holds the secret")
	}
}

// decodeJSON decodes a document that must be one JSON value.
func decodeJSON(t *testing.T, text string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &v); err != nil {
		t.Fatalf("not JSON: %v\n%q", err, text)
	}
	return v
}

// TS-32-65: A GitLab label change is relayed as unlabeled and then labeled.
// Path: 32-PATH-2. Requirements: 32-REQ-3.4, 32-REQ-7.2
func TestSmoke_AGitLabLabelChangeIsUnlabeledThenLabeled_TS_32_65(t *testing.T) {
	s := newWebhookSmoke(t)
	s.workspace("widgets", "https://gitlab.example.com/group/widgets")
	const secret = "a-known-secret-of-16-chars"
	s.secret("widgets", secret)

	body := strings.ReplaceAll(gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"old"}, []string{"new"})}, "new"), "acme/widgets", "group/widgets")
	status, answer := s.post("widgets", gitlabDelivery(secret, "gl-1", "Issue Hook", body))
	if status != http.StatusAccepted || answer != `{"outcome":"relayed"}` {
		t.Fatalf("the delivery: %d %s; want 202 relayed", status, answer)
	}
	if n := s.deliveryCount(); n != 1 {
		t.Errorf("%d rows in the delivery log; want 1", n)
	}
	events := s.waitForEvents("widgets", 2)
	if got := eventTypesOf(events); !reflect.DeepEqual(got, []string{audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled}) {
		t.Fatalf("hub_audit_events holds %v; want unlabeled then labeled", got)
	}
	if events[0].Metadata["label"] != "old" || events[1].Metadata["label"] != "new" || events[0].Metadata["forge"] != "gitlab" ||
		events[0].ActorType != "forge" || events[0].Workspace != "widgets" {
		t.Errorf("events = %+v; want the labels old and new from GitLab, by the forge", events)
	}
}

// TS-32-66: A redelivered GitHub delivery is a duplicate, with no extra row and no extra event.
// Path: 32-PATH-3. Requirements: 32-REQ-4.5
func TestSmoke_ARedeliveryIsADuplicate_TS_32_66(t *testing.T) {
	s := newWebhookSmoke(t)
	s.workspace("widgets", "https://github.com/acme/widgets")
	const secret = "a-known-secret-of-16-chars"
	s.secret("widgets", secret)

	d := githubDelivery(secret, "gh-1", "issues", issuesPayload("labeled", "acme/widgets"))
	if status, body := s.post("widgets", d); status != http.StatusAccepted || body != `{"outcome":"relayed"}` {
		t.Fatalf("the first delivery: %d %s; want 202 relayed", status, body)
	}
	s.waitForEvents("widgets", 1)
	if status, body := s.post("widgets", d); status != http.StatusAccepted || body != `{"outcome":"duplicate"}` {
		t.Fatalf("the second delivery: %d %s; want 202 duplicate", status, body)
	}
	time.Sleep(100 * time.Millisecond) // an event of the duplicate would be stored by now
	if n := s.deliveryCount(); n != 1 {
		t.Errorf("%d rows in the delivery log; want 1", n)
	}
	if got := eventTypesOf(s.hubEvents("widgets")); !reflect.DeepEqual(got, []string{audit.EventForgeIssueLabeled}) {
		t.Errorf("hub_audit_events holds %v; want only the event of the first delivery", got)
	}
}

// TS-32-67: An operator reads the delivery log with afc and the webhook state with GET /forge.
// Path: 32-PATH-4. Requirements: 32-REQ-9.1, 32-REQ-2.4
func TestSmoke_AnOperatorReadsTheLogAndTheState_TS_32_67(t *testing.T) {
	s := newWebhookSmoke(t)
	s.workspace("widgets", "https://github.com/acme/widgets")
	const secret = "a-known-secret-of-16-chars"
	s.secret("widgets", secret)
	for i := 0; i < 12; i++ {
		addDelivery(t, s.db, deliveryRecord{slug: "widgets", id: fmt.Sprintf("d-%02d", i), action: "labeled", target: ptr("origin"),
			relayed: `["hub.forge.issue.labeled"]`, receivedAt: at(i)})
	}

	stdout, _, err := s.afc("forge", "deliveries", "widgets", "--limit", "10")
	if err != nil {
		t.Fatalf("afc forge deliveries: %v\n%s", err, stdout)
	}
	rows, _ := decodeJSON(t, stdout).([]any)
	if len(rows) != 10 {
		t.Fatalf("afc printed %d rows; want 10", len(rows))
	}
	for i, row := range rows {
		if got, want := row.(map[string]any)["received_at"], at(11-i); got != want {
			t.Fatalf("row %d received_at = %v; want %s (newest first)", i, got, want)
		}
	}

	code, state := s.getJSON("/workspaces/widgets/forge")
	if code != http.StatusOK {
		t.Fatalf("GET /forge: %d %s", code, state)
	}
	var forge struct {
		Webhook webhookBlock `json:"webhook"`
	}
	if err := json.Unmarshal([]byte(state), &forge); err != nil {
		t.Fatal(err)
	}
	if !forge.Webhook.Configured || forge.Webhook.URL == nil || *forge.Webhook.URL != "https://hub.example.com/hooks/forge/widgets" ||
		forge.Webhook.LastDeliveryAt == nil || *forge.Webhook.LastDeliveryAt != at(11) {
		t.Errorf("webhook = %+v; want configured, the ingress URL and the time of the newest row %s", forge.Webhook, at(11))
	}
	if strings.Contains(stdout, secret) || strings.Contains(state, secret) {
		t.Errorf("an output holds the secret")
	}
}

// TS-32-69: An operator turns the ingress off and later signed deliveries answer 404.
// Path: 32-PATH-6. Requirements: 32-REQ-2.1, 32-REQ-2.3
func TestSmoke_AnOperatorTurnsTheIngressOff_TS_32_69(t *testing.T) {
	s := newWebhookSmoke(t)
	s.workspace("widgets", "https://github.com/acme/widgets")
	const secret = "a-known-secret-of-16-chars"
	s.secret("widgets", secret)
	d := githubDelivery(secret, "gh-1", "issues", issuesPayload("labeled", "acme/widgets"))
	if status, _ := s.post("widgets", d); status != http.StatusAccepted {
		t.Fatalf("a delivery before the unset: status = %d; want 202", status)
	}
	s.waitForEvents("widgets", 1)

	stdout, _, err := s.afc("forge", "webhook", "unset", "widgets")
	if err != nil {
		t.Fatalf("afc forge webhook unset: %v\n%s", err, stdout)
	}
	if got, want := decodeJSON(t, stdout), any(map[string]any{"status": "unset", "workspace": "widgets"}); !reflect.DeepEqual(got, want) {
		t.Errorf("afc printed %v; want %v", got, want)
	}
	events := s.waitForEvents("widgets", 2)
	if last := events[len(events)-1]; last.EventType != audit.EventForgeWebhookUnset || last.Metadata["existed"] != true {
		t.Errorf("the last event = %+v; want hub.forge.webhook.unset with existed true", last)
	}

	if status, _ := s.post("widgets", githubDelivery(secret, "gh-2", "issues", issuesPayload("labeled", "acme/widgets"))); status != http.StatusNotFound {
		t.Errorf("a delivery after the unset: status = %d; want 404", status)
	}
	if n := s.deliveryCount(); n != 1 {
		t.Errorf("%d rows in the delivery log; want only the earlier one", n)
	}
	code, listed := s.getJSON("/workspaces/widgets/forge/deliveries")
	if code != http.StatusOK || !strings.Contains(listed, `"delivery_id":"gh-1"`) {
		t.Errorf("GET deliveries: %d %s; want the earlier row", code, listed)
	}
}

// ---------------------------------------------------------------------------
// The real af-hub binary
// ---------------------------------------------------------------------------

// binaryHub is one af-hub process of the real binary: its directories and port.
type binaryHub struct {
	bin       string
	configDir string
	dataDir   string
	port      int
}

// newBinaryHub builds af-hub (it links DuckDB through cgo, so this is the slow part) and writes
// a config.toml for it on a free port.
func newBinaryHub(t *testing.T) *binaryHub {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real-binary test in -short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available; cannot build the af-hub binary")
	}
	bin := filepath.Join(t.TempDir(), "af-hub")
	build := exec.Command(goBin, "build", "-o", bin, "github.com/agent-fox-dev/hub/cmd/af-hub")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build af-hub failed: %v\n%s", err, out)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	h := &binaryHub{bin: bin, configDir: t.TempDir(), dataDir: t.TempDir(), port: port}
	config := fmt.Sprintf("[server]\nport = %d\nbind = \"127.0.0.1\"\n\n[database]\npath = %q\n\n[workspace]\npath = %q\n",
		port, h.dbPath(), filepath.Join(h.dataDir, "workspaces"))
	if err := os.WriteFile(filepath.Join(h.configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *binaryHub) dbPath() string { return filepath.Join(h.dataDir, "afhub.db") }

// syncedBuffer is a bytes.Buffer that the process and the test use together.
type syncedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// hubProcess is a running af-hub.
type hubProcess struct {
	cmd    *exec.Cmd
	out    *syncedBuffer
	exited chan struct{}
}

// start runs af-hub and waits until it answers its health probe; the test ends it.
func (h *binaryHub) start(t *testing.T) *hubProcess {
	t.Helper()
	cmd := exec.Command(h.bin)
	cmd.Dir = h.dataDir
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+h.configDir,
		"XDG_DATA_HOME="+h.dataDir,
		"HOME="+h.dataDir,
		"AF_AUDIT_DB_PATH="+filepath.Join(h.dataDir, "audit.duckdb"),
	)
	p := &hubProcess{cmd: cmd, out: &syncedBuffer{}, exited: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting af-hub: %v", err)
	}
	go func() { _ = cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.exited
	})

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", h.port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.exited:
			t.Fatalf("af-hub exited while starting:\n%s", p.out)
		default:
		}
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("af-hub did not become healthy:\n%s", p.out)
	return nil
}

// stop ends the process with SIGTERM, as an operator would, and waits for it to exit.
func (p *hubProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling af-hub: %v", err)
	}
	select {
	case <-p.exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("af-hub did not exit on SIGTERM:\n%s", p.out)
	}
}

// TS-32-68: The real af-hub creates the delivery log, prunes it at once and mounts the ingress
// outside the API group: old rows and rows of deleted workspaces are gone after the start, the
// recent row of an existing workspace remains, and POST /hooks/forge/:slug answers without a token.
// Path: 32-PATH-5. Requirements: 32-REQ-9.7, 32-REQ-9.4
func TestAfHubBinary_PrunesTheLogAndMountsTheIngress_TS_32_68(t *testing.T) {
	h := newBinaryHub(t)

	// The first start creates every table, the delivery log included.
	first := h.start(t)
	first.stop(t)

	db, err := sql.Open("sqlite", h.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'forge_deliveries'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("after the first start the database has %d forge_deliveries tables (%v); want 1\n%s", tables, err, first.out)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at)
		VALUES ('widgets', 'https://github.com/acme/widgets', 'user-1', 'active', 'ready', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seeding a workspace: %v", err)
	}
	addDelivery(t, db, deliveryRecord{slug: "widgets", id: "old", receivedAt: ago(9 * day)})
	addDelivery(t, db, deliveryRecord{slug: "widgets", id: "recent", receivedAt: ago(1 * day)})
	addDelivery(t, db, deliveryRecord{slug: "deleted", id: "orphan", receivedAt: ago(1 * day)})
	const secret = "binary-test-secret-16+"
	if _, err := secrets.NewStore(db).CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: secret}}); err != nil {
		t.Fatalf("seeding the webhook secret: %v", err)
	}

	// The second start prunes before it serves.
	second := h.start(t)
	eventually(t, "the pass at the start of af-hub", func() bool {
		ids := deliveryIDs(t, db)
		return len(ids) == 1 && ids[0] == "recent"
	})

	// The ingress is mounted on the root and works with the secrets store the hub gave it: a
	// signed delivery, with no token of the hub, is received and recorded.
	d := githubDelivery(secret, "gh-binary", "issues", issuesPayload("labeled", "acme/widgets"))
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/hooks/forge/widgets", h.port), strings.NewReader(d.body))
	req.Header = d.header.Clone()
	delivered, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := io.ReadAll(delivered.Body)
	delivered.Body.Close()
	if delivered.StatusCode != http.StatusAccepted || strings.TrimSpace(string(answer)) != `{"outcome":"relayed"}` {
		t.Errorf("a signed delivery to the running af-hub: %d %s; want 202 relayed", delivered.StatusCode, answer)
	}
	if ids := deliveryIDs(t, db); !reflect.DeepEqual(ids, []string{"gh-binary", "recent"}) {
		t.Errorf("the delivery log holds %v; want the delivery just received and the recent one", ids)
	}

	// An unknown workspace: no token, and the answer is the ingress's own 404.
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/hooks/forge/nosuch", h.port), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "not found") {
		t.Errorf("POST /hooks/forge/nosuch: %d %s; want the 404 of the ingress, not a 401 or a 405", resp.StatusCode, body)
	}

	// The loop stops with the hub.
	second.stop(t)
	if strings.Contains(second.out.String(), "pruning the delivery log failed") {
		t.Errorf("af-hub logged a failed pass:\n%s", second.out)
	}
}

// repoRoot is the directory of the hub module.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Fatal("not inside a Go module")
	}
	return filepath.Dir(mod)
}
