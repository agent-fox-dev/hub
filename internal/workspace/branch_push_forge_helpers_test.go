package workspace

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// standInForge is an HTTP forge in front of one bare repository, served by git
// http-backend. It records the basic credential of every request and the refs
// every receive-pack request updates; answer, when set, may answer a request in
// the backend's place.
type standInForge struct {
	*httptest.Server
	bare string

	mu      sync.Mutex
	auths   []string   // "user:password" of every request, "" when it had none
	updates [][]string // the refs of every receive-pack request
	answer  func(w http.ResponseWriter, r *http.Request) bool
}

func newStandInForge(t *testing.T) *standInForge {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	pushGit(t, dir, "init", "--bare", "-b", "main", bare)
	pushGit(t, bare, "config", "http.receivepack", "true")
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git: %v", err)
	}
	backend := &cgi.Handler{Path: gitBin, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + dir, "GIT_HTTP_EXPORT_ALL=1"}}
	f := &standInForge{bare: bare}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := ""
		if user, pass, ok := r.BasicAuth(); ok {
			cred = user + ":" + pass
		}
		var refs []string
		if strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			refs = receivePackRefs(body)
		}
		f.mu.Lock()
		f.auths = append(f.auths, cred)
		if refs != nil {
			f.updates = append(f.updates, refs)
		}
		answer := f.answer
		f.mu.Unlock()
		if answer != nil && answer(w, r) {
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// repoURL is the URL of the forge's repository.
func (f *standInForge) repoURL() string { return f.Server.URL + "/origin.git" }

// received returns the recorded credentials and ref updates.
func (f *standInForge) received() (auths []string, updates [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...), append([][]string(nil), f.updates...)
}

// setAnswer replaces the forge's answer.
func (f *standInForge) setAnswer(answer func(w http.ResponseWriter, r *http.Request) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = answer
}

// receivePackRefs are the ref names of the commands at the start of a
// receive-pack request body: pkt-lines "<old> <new> <ref>[\0caps]" up to the
// flush packet.
func receivePackRefs(body []byte) []string {
	refs := []string{}
	for len(body) >= 4 {
		n, err := strconv.ParseUint(string(body[:4]), 16, 16)
		if err != nil || n < 4 || int(n) > len(body) {
			break
		}
		line := strings.SplitN(string(body[4:n]), "\x00", 2)[0]
		body = body[n:]
		if fields := strings.Fields(line); len(fields) == 3 {
			refs = append(refs, fields[2])
		}
	}
	return refs
}

// storeGitSecrets stores the given secrets of the workspace slug.
func storeGitSecrets(t *testing.T, env *branchPushEnv, slug string, kv ...string) {
	t.Helper()
	var entries []secrets.EntryInput
	for i := 0; i+1 < len(kv); i += 2 {
		entries = append(entries, secrets.EntryInput{Key: kv[i], Value: kv[i+1]})
	}
	if _, err := secrets.NewStore(env.db).CreateSecrets("workspace", slug, entries); err != nil {
		t.Fatalf("storing secrets of %s: %v", slug, err)
	}
}

// setGitURL changes the git_url of the workspace slug.
func setGitURL(t *testing.T, env *branchPushEnv, slug, gitURL string) {
	t.Helper()
	if _, err := env.db.Exec(`UPDATE workspaces SET git_url = ? WHERE slug = ?`, gitURL, slug); err != nil {
		t.Fatal(err)
	}
}

// lockedBuffer is a bytes.Buffer safe for the log handler and the test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureBranchPushLog sends the default slog logger's records, as JSON lines,
// to the returned buffer for the rest of the test.
func captureBranchPushLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}
