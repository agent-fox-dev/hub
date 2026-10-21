package workspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// maxBranchPushBody bounds the request body of the branch push, which holds at
// most {"force": true}.
const maxBranchPushBody = 64 << 10

// branchPushDeadline is how long a push to origin may run (38-REQ-6.3).
const branchPushDeadline = 2 * time.Minute

// handleBranchPush handles POST /api/v1/workspaces/:slug/branches/:name/push:
// it pushes one branch of the workspace's trunk to its origin.
//
// Requirements: 38-REQ-1 to 38-REQ-8
func handleBranchPush(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// 38-REQ-2.1, 38-REQ-2.3: the scope is checked before the workspace
		// is looked up. A PAT bound to another workspace never gets here: the
		// binding guard answers 404 first (38-REQ-2.2).
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasWriteAccess(auth) {
			return respondError(c, http.StatusForbidden,
				"PAT requires workspaces:write scope to push branches")
		}

		// 38-REQ-2.4 to 38-REQ-2.6: write access, as for every workspace write.
		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth, wsaccess.NeedWrite)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		// 38-REQ-1.4, 38-REQ-1.5
		force, err := parseBranchPushBody(c.Request().Body)
		if err != nil {
			return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// 38-REQ-1.2, 38-REQ-3.1: the name is the segment as sent, decoded
		// once; a name git would not accept names no branch of the trunk.
		name, err := branchPushName(c.Request())
		if err == nil {
			err = gitcmd.ValidateRefName(name)
		}
		if err != nil {
			return respondErrorWithType(c, http.StatusNotFound,
				fmt.Sprintf("branch_not_found: %q is not a valid branch name: %v", name, err), "branch_not_found")
		}

		// 38-REQ-3.2, 38-REQ-3.3
		if ws.Status != "active" {
			return respondError(c, http.StatusBadRequest,
				"workspace is not active; push requires an active workspace")
		}
		if ws.CloneStatus != "ready" {
			return respondError(c, http.StatusBadRequest,
				"workspace clone is not ready; clone_status must be 'ready' to push")
		}

		// 38-REQ-4.1, 38-REQ-4.2, 38-REQ-4.4, 38-REQ-4.8: the refusals that
		// read only the database and the workspace row come before the lock,
		// so they answer the same whether the workspace is busy or not.
		refusal, err := ownedBranchRefusal(db, ws, name, force)
		if err != nil {
			return branchPushInternalError(c, slug, "deciding the branch's owner", err, "")
		}
		if refusal != nil {
			return refusal.write(c, "")
		}

		// 38-REQ-5.1, 38-REQ-5.2: every read of the trunk is serialized with
		// the other trunk operations; a rebuild between its phases does not
		// hold the lock and does not block the push.
		unlock, locked := wslock.TryLock(slug)
		if !locked {
			return respondErrorWithType(c, http.StatusConflict,
				"another operation is running on this workspace; retry later", "workspace_busy")
		}
		defer unlock()
		return pushBranchLocked(c, db, ws, name, force)
	}
}

// pushBranchLocked is the part of the branch push that reads the trunk and
// pushes; the caller holds the workspace lock.
func pushBranchLocked(c echo.Context, db *sql.DB, ws *Workspace, name string, force bool) error {
	slug := ws.Slug
	repoPath := filepath.Join(defaultWorkspaceRoot, slug, "trunk")
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return branchPushInternalError(c, slug, "opening the trunk", err, "")
	}

	// 38-REQ-4.5, 38-REQ-4.6: with no branch column the workspace's own
	// branch is the one HEAD points to, read under the lock, because a merge
	// checks out another branch while it holds the lock.
	own, err := ownBranch(repo, ws)
	if err != nil && force {
		return branchPushInternalError(c, slug, "reading the trunk's HEAD", err, "")
	}
	if force && own == name {
		return protectedBranchRefusal(name).write(c, "")
	}

	// 38-REQ-5.3, 38-REQ-5.4
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(name), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return respondErrorWithType(c, http.StatusNotFound,
			fmt.Sprintf("branch_not_found: branch %q does not exist in the workspace", name), "branch_not_found")
	}
	if err != nil {
		return branchPushInternalError(c, slug, "resolving the branch", err, "")
	}
	sha := ref.Hash().String()

	// 38-REQ-6.1, 38-REQ-6.2: the credential the archive's push uses; a
	// failure of the secrets store is hub's own and sends nothing.
	gitAuth, err := resolveCloneAuth(secrets.NewStore(db), slug)
	if err != nil {
		return branchPushInternalError(c, slug, "resolving the git credential", err, "")
	}
	secret := credentialSecret(gitAuth)

	refSpec := config.RefSpec("refs/heads/" + name + ":refs/heads/" + name)
	if force {
		refSpec = "+" + refSpec
	}
	ctx := c.Request().Context()
	progress := &remoteMessages{}
	err = pushToOriginFn(ctx, repoPath, ws.GitURL, gitAuth, []config.RefSpec{refSpec}, force, progress)
	var te *trunkError
	result := "pushed"
	switch {
	case errors.As(err, &te):
		// 38-REQ-6.8: hub's own failure, not a push attempt.
		return branchPushInternalError(c, slug, "opening the trunk", err, secret)
	case errors.Is(err, git.NoErrAlreadyUpToDate):
		result = "up_to_date"
	case err != nil:
		failure := classifyPushError(ctx, err, gitAuth, progress.String())
		if failure.errorType == "non_fast_forward" {
			failure.message = nonFastForwardMessage(ctx, ws.GitURL, gitAuth, name, own, force)
		}
		return failure.write(c, secret)
	case force:
		result = "forced"
	}
	return c.JSON(http.StatusOK, branchPushAnswer{
		Branch: name, SHA: sha, Remote: "origin", RemoteURL: stripUserinfoURL(ws.GitURL), Result: result,
	})
}

// ownBranch is the workspace's own branch: its branch column, or, when that is
// empty, the branch the trunk's HEAD points to ("" when HEAD is detached).
func ownBranch(repo *git.Repository, ws *Workspace) (string, error) {
	if ws.Branch != nil && *ws.Branch != "" {
		return *ws.Branch, nil
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	if !head.Name().IsBranch() {
		return "", nil
	}
	return head.Name().Short(), nil
}

// credentialSecret is the password or token of a git credential, "" when there
// is none.
func credentialSecret(auth transport.AuthMethod) string {
	if basic, ok := auth.(*githttp.BasicAuth); ok {
		return basic.Password
	}
	return ""
}

// maxRemoteMessages bounds what is kept of the messages the remote sends during
// a push.
const maxRemoteMessages = 4 << 10

// remoteMessages keeps the start of what the remote reports during a push (the
// sideband progress), such as the reason a hook gives for refusing a ref.
type remoteMessages struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (m *remoteMessages) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if room := maxRemoteMessages - m.buf.Len(); room > 0 {
		m.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (m *remoteMessages) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(strings.Fields(m.buf.String()), " ")
}

// classifyPushError maps a failed push attempt to its answer (38-REQ-6). go-git
// marks authentication, authorization and a missing repository with transport
// errors; its own fast-forward check and the forge's per-ref statuses are only
// text, so those are matched on the text. messages are what the remote reported
// while the push ran. The messages built here hold no part of go-git's error
// chain except the forge's reason for a refused ref, which is scrubbed when the
// answer is written.
func classifyPushError(ctx context.Context, err error, auth transport.AuthMethod, messages string) *pushRefusal {
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &pushRefusal{http.StatusGatewayTimeout, "forge_timeout",
			fmt.Sprintf("forge_timeout: origin did not finish the push within %s", branchPushDeadline)}
	case strings.Contains(text, "non-fast-forward") || strings.Contains(text, "fetch first"):
		return &pushRefusal{http.StatusConflict, "non_fast_forward", "non_fast_forward"}
	case errors.Is(err, transport.ErrAuthenticationRequired):
		return &pushRefusal{http.StatusBadGateway, "forge_unauthenticated", unauthenticatedMessage(auth)}
	case errors.Is(err, transport.ErrAuthorizationFailed):
		return &pushRefusal{http.StatusBadGateway, "forge_forbidden",
			"forge_forbidden: origin does not let the workspace's git credential push to this repository"}
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return &pushRefusal{http.StatusNotFound, "forge_not_found",
			"forge_not_found: origin has no repository at the workspace's git_url, or the credential cannot see it"}
	case strings.HasPrefix(text, "command error on "):
		reason := err.Error()[strings.LastIndex(err.Error(), ": ")+2:]
		if messages != "" {
			reason += " (" + messages + ")"
		}
		return &pushRefusal{http.StatusUnprocessableEntity, "forge_rejected", "forge_rejected: origin refused the update: " + reason}
	case isUnreachable(err):
		return &pushRefusal{http.StatusBadGateway, "forge_unreachable",
			"forge_unreachable: origin could not be reached (a DNS, connection or TLS failure)"}
	}
	return &pushRefusal{http.StatusBadGateway, "forge_error", "forge_error: the push to origin failed"}
}

// isUnreachable reports whether err is a DNS, TCP or TLS failure.
func isUnreachable(err error) bool {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var certErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) || errors.As(err, &certErr) ||
		errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &recordHeader) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"no such host", "connection refused", "connection reset", "network is unreachable", "dial tcp", "tls:", "x509:"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// unauthenticatedMessage says whether the workspace has no stored credential or
// the forge rejected the one it has, and names the credential's kind, never its
// value.
func unauthenticatedMessage(auth transport.AuthMethod) string {
	basic, ok := auth.(*githttp.BasicAuth)
	switch {
	case !ok:
		return "forge_unauthenticated: origin requires authentication and the workspace has no stored git credential; store GIT_PAT, or GIT_USERNAME and GIT_PASSWORD"
	case basic.Username == "x-token-auth":
		return "forge_unauthenticated: origin rejected the workspace's GIT_PAT"
	default:
		return "forge_unauthenticated: origin rejected the workspace's GIT_USERNAME/GIT_PASSWORD"
	}
}

// nonFastForwardMessage is the message of a push that was not a fast-forward.
// It lists origin's refs once, with the push's URL, credential and deadline, to
// name origin's commit, and leaves the commit out when the listing fails. The
// hint is to force for any branch but the workspace's own, where force is
// refused and syncing is the way; when force was already given the forge
// refuses non-fast-forward updates of the branch and neither hint applies.
func nonFastForwardMessage(ctx context.Context, gitURL string, auth transport.AuthMethod, name, own string, force bool) string {
	msg := "non_fast_forward: origin's " + name
	if tip, ok := originBranchTip(ctx, gitURL, auth, name); ok {
		msg += " is at " + tip + ", which"
	}
	msg += " is not in the workspace's branch; "
	switch {
	case force:
		return msg + "origin refuses non-fast-forward updates of it, also with force"
	case name == own:
		return msg + "sync the workspace to bring it up to date"
	default:
		return msg + "push with force to overwrite it"
	}
}

// originBranchTip lists origin's refs once and returns the commit its branch
// name points at.
func originBranchTip(ctx context.Context, gitURL string, auth transport.AuthMethod, name string) (string, bool) {
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{gitURL}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return "", false
	}
	want := plumbing.NewBranchReferenceName(name)
	for _, ref := range refs {
		if ref.Name() == want {
			return ref.Hash().String(), true
		}
	}
	return "", false
}

// branchPushAnswer is the 200 answer of the branch push.
type branchPushAnswer struct {
	Branch    string `json:"branch"`
	SHA       string `json:"sha"`
	Remote    string `json:"remote"`
	RemoteURL string `json:"remote_url"`
	Result    string `json:"result"`
}

// pushRefusal is an answer of the branch push that refuses the request with an
// error_type of the route's own.
type pushRefusal struct {
	status    int
	errorType string
	message   string
}

// write answers the refusal, its message scrubbed of secret and of URL user
// information.
func (r *pushRefusal) write(c echo.Context, secret string) error {
	return respondErrorWithType(c, r.status, scrubText(r.message, secret), r.errorType)
}

// ownedBranchRefusal is the refusal of a branch the route does not push that can
// be decided from the database and the workspace row alone, nil when there is
// none: on a carry_patch workspace the integration branch (its column, or deploy
// when that is empty) and a registered patch (a patch row whose status is not
// deleted); in both modes a forced push of the branch column.
func ownedBranchRefusal(db *sql.DB, ws *Workspace, name string, force bool) (*pushRefusal, error) {
	if ws.WorkspaceMode == "carry_patch" {
		integration := "deploy"
		if ws.IntegrationBranch != nil && *ws.IntegrationBranch != "" {
			integration = *ws.IntegrationBranch
		}
		if name == integration {
			return &pushRefusal{http.StatusBadRequest, "integration_branch", fmt.Sprintf(
				"integration_branch: hub builds %q, this workspace's integration branch, on every rebuild; it reaches the fork through REBUILD_PUSH_INTEGRATION_BRANCH", name)}, nil
		}
		patch, err := getPatchByBranch(db, ws.Slug, name)
		if err != nil {
			return nil, err
		}
		if patch != nil && patch.Status != "deleted" {
			return &pushRefusal{http.StatusBadRequest, "patch_branch", fmt.Sprintf(
				"patch_branch: %q is a registered patch of this workspace; patches reach the fork through PUSH_PATCHES_TO_ORIGIN", name)}, nil
		}
	}
	if force && ws.Branch != nil && *ws.Branch != "" && name == *ws.Branch {
		return protectedBranchRefusal(name), nil
	}
	return nil, nil
}

// protectedBranchRefusal refuses a forced push of the workspace's own branch.
func protectedBranchRefusal(name string) *pushRefusal {
	return &pushRefusal{http.StatusBadRequest, "protected_branch", fmt.Sprintf(
		"protected_branch: %q is the workspace's own branch; hub does not force-push it to origin", name)}
}

// branchPushInternalError logs a failure of hub's own, scrubbed of secret, and
// answers 500.
func branchPushInternalError(c echo.Context, slug, what string, err error, secret string) error {
	slog.Error("branch push: "+what+" failed", "workspace", slug, "error", scrubText(err.Error(), secret))
	return respondError(c, http.StatusInternalServerError, "internal server error")
}

// parseBranchPushBody reads the optional body of the branch push and returns its
// force. An empty body, an object without force and unknown fields leave force
// false; a body that is not a JSON object, or a force that is not a boolean, is
// an error.
func parseBranchPushBody(r io.Reader) (bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBranchPushBody))
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return false, errors.New("the body must be a JSON object")
	}
	value, ok := body["force"]
	if !ok {
		return false, nil
	}
	var force bool
	if string(bytes.TrimSpace(value)) == "null" || json.Unmarshal(value, &force) != nil {
		return false, errors.New("force must be a boolean")
	}
	return force, nil
}

// branchPushName is the branch name of the request: the path segment before the
// final /push, percent-decoded once. It reads the path as the client sent it,
// because echo's own path parameter is still encoded when the path holds a %2F
// and decoded when it does not, so decoding that value would sometimes decode a
// name that holds a % twice.
func branchPushName(r *http.Request) (string, error) {
	path := strings.TrimSuffix(r.URL.EscapedPath(), "/push")
	return url.PathUnescape(path[strings.LastIndexByte(path, '/')+1:])
}
