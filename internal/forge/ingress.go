package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// The limits of a delivery: its body, the id the forge gives it and the event name that is
// kept of it.
const (
	maxDeliveryBody   = 1 << 20
	maxDeliveryIDLen  = 255
	maxEventNameBytes = 64
)

// unverifiedMessage is the one answer to a delivery that is not proven to come from the
// forge, whatever is wrong with its signature or token.
const unverifiedMessage = "delivery signature missing or invalid"

// The outcomes of a delivery. Duplicate is only answered: a duplicate is never a row.
const (
	outcomeRelayed   = "relayed"
	outcomeIgnored   = "ignored"
	outcomeMismatch  = "repository_mismatch"
	outcomeMalformed = "malformed"
	outcomeDuplicate = "duplicate"
)

// The forges that post deliveries, as the delivery log names them, and the headers by which a
// delivery shows which one sent it.
const (
	forgeGitHub = "github"
	forgeGitLab = "gitlab"

	githubSignatureHeader = "X-Hub-Signature-256"
	gitlabTokenHeader     = "X-Gitlab-Token"
)

// MountWebhookIngress mounts POST /hooks/forge/:slug on e, the Echo root, beside the git
// routes and outside the API group and its authentication: a delivery proves who it is with
// its signature. The ingress never contacts the forge and takes no workspace lock.
func MountWebhookIngress(e *echo.Echo, cfg Config) {
	h := &handlers{cfg: cfg, reduce: reduceDelivery}
	e.POST("/hooks/forge/:slug", h.ingress)
}

// ingress serves POST /hooks/forge/:slug. It looks the workspace and its secret up, checks the
// delivery against the secret, reads who the delivery is and what it says, records it once, and
// answers it; only then does it hand the events to the emitter. It never acts on a delivery.
func (h *handlers) ingress(c echo.Context) error {
	slug := c.Param("slug")
	remotes, secret, status := h.lookupWebhook(slug)
	switch status {
	case http.StatusNotFound:
		return apikit.WriteAPIError(c, http.StatusNotFound, "not found")
	case http.StatusInternalServerError:
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
	}

	header := c.Request().Header
	var scheme string
	switch {
	case len(header.Values(githubSignatureHeader)) > 0:
		scheme = forgeGitHub
	case len(header.Values(gitlabTokenHeader)) > 0:
		scheme = forgeGitLab
		if !tokenMatches(header.Values(gitlabTokenHeader), secret) {
			return unverified(c, slug, scheme)
		}
	default:
		return unverified(c, slug, "none")
	}

	body, err := readDelivery(c.Request())
	if err != nil {
		if errors.Is(err, errDeliveryTooLarge) {
			return apikit.WriteAPIErrorWithType(c, http.StatusRequestEntityTooLarge, "the delivery is larger than 1 MiB", "body_too_large")
		}
		return apikit.WriteAPIError(c, http.StatusBadRequest, "the delivery body cannot be read")
	}
	if scheme == forgeGitHub && !signatureMatches(header.Values(githubSignatureHeader), body, secret) {
		return unverified(c, slug, scheme)
	}

	d, ref := identify(header, scheme)
	if ref != nil {
		return writeRefusal(c, ref)
	}
	d.slug, d.body = slug, body
	red := h.reduce(d, remotes)

	inserted, err := h.recordDelivery(d, red)
	if err != nil {
		slog.Error("forge: recording a delivery failed", "slug", slug, "error", err)
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
	}
	switch {
	case !inserted:
		return writeOutcome(c, outcomeDuplicate)
	case red.outcome == outcomeMalformed:
		return apikit.WriteAPIError(c, http.StatusBadRequest, "the delivery payload is not what its event needs")
	}
	if err := writeOutcome(c, red.outcome); err != nil {
		return err
	}
	for _, event := range red.events {
		h.emitEvent(c.Request().Context(), event)
	}
	return nil
}

// lookupWebhook reads what the ingress needs of a workspace: its remotes and its webhook
// secret. The status is 0 when both are there, 404 for a workspace that does not exist and for
// one that has no secret, the same answer for both, and 500 for a failure of a store.
func (h *handlers) lookupWebhook(slug string) (remotes workspaceRemotes, secret string, status int) {
	if h.cfg.Secrets == nil {
		slog.Error("forge: the secrets store is not configured, the ingress cannot verify a delivery")
		return remotes, "", http.StatusInternalServerError
	}
	remotes, err := h.loadRemotes(slug)
	if errors.Is(err, sql.ErrNoRows) {
		return remotes, "", http.StatusNotFound
	}
	if err != nil {
		slog.Error("forge: reading the workspace of a delivery failed", "slug", slug, "error", err)
		return remotes, "", http.StatusInternalServerError
	}
	secret, err = h.cfg.Secrets.GetSecretValue("workspace", slug, webhookSecretKey)
	var notFound *secrets.NotFoundError
	switch {
	case errors.As(err, &notFound):
		return remotes, "", http.StatusNotFound
	case err != nil:
		// The error is not logged as text: it may carry what the store held.
		slog.Error("forge: reading the webhook secret of a delivery failed", "slug", slug, "error_type", fmt.Sprintf("%T", err))
		return remotes, "", http.StatusInternalServerError
	case secret == "":
		// An empty secret would accept an empty token and sign with an empty key.
		return remotes, "", http.StatusNotFound
	}
	return remotes, secret, 0
}

// unverified answers a delivery that is not proven to come from the forge: 401 with one message
// whatever is wrong with it, nothing recorded, and a warning that has the slug and the scheme and
// never a header value or the body.
func unverified(c echo.Context, slug, scheme string) error {
	slog.Warn("forge: unverified webhook delivery", "slug", slug, "scheme", scheme)
	return apikit.WriteAPIError(c, http.StatusUnauthorized, unverifiedMessage)
}

// tokenMatches reports whether the X-Gitlab-Token header, given once, is the secret. Both are
// hashed first, so that the comparison takes the same time whatever the lengths are.
func tokenMatches(values []string, secret string) bool {
	if len(values) != 1 {
		return false
	}
	got, want := sha256.Sum256([]byte(values[0])), sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// signatureMatches reports whether the X-Hub-Signature-256 header, given once, is "sha256="
// and the 64 lowercase hex digits of the HMAC-SHA256 of the body under the secret.
func signatureMatches(values []string, body []byte, secret string) bool {
	if len(values) != 1 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal([]byte(values[0]), []byte("sha256="+hex.EncodeToString(mac.Sum(nil))))
}

// errDeliveryTooLarge is a delivery body of more than maxDeliveryBody bytes.
var errDeliveryTooLarge = errors.New("the delivery body is too large")

// readDelivery reads the body of a delivery, at most maxDeliveryBody bytes. A body that is
// longer, whether its length was announced or not and whether a limit installed before the handler
// or this one found out, is errDeliveryTooLarge.
func readDelivery(r *http.Request) ([]byte, error) {
	if r.ContentLength > maxDeliveryBody {
		return nil, errDeliveryTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxDeliveryBody+1))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return nil, errDeliveryTooLarge
	case err != nil:
		return nil, err
	case len(data) > maxDeliveryBody:
		return nil, errDeliveryTooLarge
	}
	return data, nil
}

// identify reads the id the forge gave the delivery and its event name from the headers of the
// scheme. A delivery that has none of them, or an id of more than maxDeliveryIDLen characters or
// with one that is not printable ASCII, is refused; the event name is cut to maxEventNameBytes
// where it is recorded, not here.
func identify(header http.Header, scheme string) (delivery, *refusal) {
	d := delivery{forge: scheme}
	idHeaders := "X-GitHub-Delivery"
	if scheme == forgeGitHub {
		d.id, d.event = header.Get("X-GitHub-Delivery"), header.Get("X-GitHub-Event")
	} else {
		idHeaders = "Idempotency-Key or X-Gitlab-Event-UUID"
		if d.id = header.Get("Idempotency-Key"); d.id == "" {
			d.id = header.Get("X-Gitlab-Event-UUID")
		}
		d.event = header.Get("X-Gitlab-Event")
	}
	switch {
	case d.id == "":
		return d, badRequest("the delivery has no id (%s)", idHeaders)
	case d.event == "":
		return d, badRequest("the delivery has no event name")
	case len(d.id) > maxDeliveryIDLen || !printableASCII(d.id):
		return d, badRequest("the delivery id must be at most %d printable ASCII characters", maxDeliveryIDLen)
	}
	return d, nil
}

// printableASCII reports whether every character of s is a printable ASCII one, the space
// included.
func printableASCII(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r > '~' })
}

// recordDelivery writes the delivery log row of a delivery and reports whether it was written:
// false when the log already had the delivery, which makes this one a duplicate. The insert is
// the one step that decides it, so that copies of a delivery that arrive together are told apart.
func (h *handlers) recordDelivery(d delivery, red reduction) (bool, error) {
	relayed := make([]string, len(red.events))
	for i, event := range red.events {
		relayed[i] = event.EventType
	}
	relayedJSON, err := json.Marshal(relayed)
	if err != nil {
		return false, err
	}
	var tgt any
	if red.target != "" {
		tgt = string(red.target)
	}
	res, err := h.cfg.DB.Exec(`INSERT INTO forge_deliveries
		(workspace_slug, delivery_id, forge, event, action, target, outcome, relayed, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (workspace_slug, delivery_id) DO NOTHING`,
		d.slug, d.id, d.forge, truncateUTF8(strings.ToValidUTF8(d.event, "\uFFFD"), maxEventNameBytes),
		red.action, tgt, red.outcome, string(relayedJSON), apikit.NowUTC())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// writeOutcome answers 202 {"outcome": ...} and flushes the answer, so that the forge has it
// before hub starts on the events. The length is set, so that the forge sees a complete answer
// at once and not one that ends when the handler returns.
func writeOutcome(c echo.Context, outcome string) error {
	body, err := json.Marshal(map[string]string{"outcome": outcome})
	if err != nil {
		return err
	}
	res := c.Response()
	res.Header().Set(echo.HeaderContentType, "application/json; charset=utf-8")
	res.Header().Set(echo.HeaderContentLength, strconv.Itoa(len(body)))
	res.WriteHeader(http.StatusAccepted)
	if _, err := res.Write(body); err != nil {
		return err
	}
	res.Flush()
	return nil
}
