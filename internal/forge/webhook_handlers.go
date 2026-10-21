package forge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
)

// The length of a webhook secret that a caller supplies, in characters, and of one that hub
// makes up, in random bytes (twice as many hex characters).
const (
	minWebhookSecret    = 16
	maxWebhookSecret    = 255
	generatedSecretSize = 32
)

// webhookState is the answer of PUT /workspaces/:slug/forge/webhook. Secret is set only
// when hub made the secret up: it is the one answer that holds it.
type webhookState struct {
	Configured bool    `json:"configured"`
	URL        *string `json:"url"`
	Secret     string  `json:"secret,omitempty"`
}

// secretsStore returns the secrets store of the webhook routes. Without one the routes are
// misconfigured, which is logged and answered 500; it returns nil after writing the answer,
// in which case the caller returns err.
func (h *handlers) secretsStore(c echo.Context) (store SecretStore, err error) {
	if h.cfg.Secrets == nil {
		slog.Error("forge: the secrets store is not configured, the webhook routes cannot work")
		return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
	}
	return h.cfg.Secrets, nil
}

// setWebhook serves PUT /workspaces/:slug/forge/webhook: it stores the workspace's webhook
// secret, the one the caller supplies or one that hub generates, and answers with the URL the
// forge is to post to. It reads the body only after the checks that come first, and it does
// not take a target.
func (h *handlers) setWebhook(c echo.Context) error {
	req, err := h.authorize(c, writeAccess)
	if req == nil {
		return err
	}
	store, err := h.secretsStore(c)
	if store == nil {
		return err
	}
	data, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return writeRefusal(c, badRequest("the request body cannot be read"))
	}
	secret, ref := webhookSecretOf(data)
	if ref != nil {
		return writeRefusal(c, ref)
	}
	generated := secret == ""
	if generated {
		if secret, err = generateSecret(); err != nil {
			slog.Error("forge: generating a webhook secret failed", "slug", req.slug, "error_type", fmt.Sprintf("%T", err))
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}
	}
	if err := storeWebhookSecret(store, req.slug, secret); err != nil {
		if isScopeFull(err) {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		return webhookStoreFailure(c, "storing the webhook secret", req.slug, err)
	}
	h.emitEvent(c.Request().Context(), webhookEvent(req, audit.EventForgeWebhookSet, "set", map[string]any{"generated": generated}))

	state := webhookState{Configured: true, URL: webhookURL(h.cfg.ExternalURL, req.slug)}
	if generated {
		state.Secret = secret
	}
	return c.JSON(http.StatusOK, state)
}

// unsetWebhook serves DELETE /workspaces/:slug/forge/webhook: it removes the workspace's
// webhook secret, so that deliveries are refused from then on, and answers 204 whether or not
// there was one. It works on a workspace of any status and does not take a target.
func (h *handlers) unsetWebhook(c echo.Context) error {
	req, err := h.authorize(c, writeAnyStatusAccess)
	if req == nil {
		return err
	}
	store, err := h.secretsStore(c)
	if store == nil {
		return err
	}
	existed := true
	if err := store.DeleteSecret("workspace", req.slug, webhookSecretKey); err != nil {
		var notFound *secrets.NotFoundError
		if !errors.As(err, &notFound) {
			return webhookStoreFailure(c, "removing the webhook secret", req.slug, err)
		}
		existed = false
	}
	h.emitEvent(c.Request().Context(), webhookEvent(req, audit.EventForgeWebhookUnset, "unset", map[string]any{"existed": existed}))
	return c.NoContent(http.StatusNoContent)
}

// webhookSecretOf reads the body of PUT .../forge/webhook. It returns the secret the caller
// supplied, or "" when there is none and hub is to make one up: for no body, for {} and for
// "secret": null. A body that is not a JSON object, a secret that is not a string and a
// secret that does not follow the rules are refused. The refusals name the rule and never the
// value.
func webhookSecretOf(data []byte) (string, *refusal) {
	if len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}
	fields, ref := parseBody(data)
	if ref != nil {
		return "", badRequest(`the request body must be a JSON object with an optional "secret" string`)
	}
	secret, present, ref := fields.text("secret")
	if ref != nil || !present {
		return "", ref
	}
	if n := utf8.RuneCountInString(secret); n < minWebhookSecret || n > maxWebhookSecret {
		return "", badRequest("secret must be %d to %d characters long", minWebhookSecret, maxWebhookSecret)
	}
	for _, r := range secret {
		if r < '!' || r > '~' {
			return "", badRequest("secret must only have the printable ASCII characters from ! to ~, so no space and no control character")
		}
	}
	return secret, nil
}

// generateSecret makes up a webhook secret: 32 random bytes as 64 lowercase hex characters.
func generateSecret() (string, error) {
	b := make([]byte, generatedSecretSize)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// storeWebhookSecret sets the workspace's webhook secret: it updates the secret that is there
// and creates one that is not. A create that finds the secret was beaten by a request that
// made it a moment before, and is followed by one more update, which is as far as the race
// is chased.
func storeWebhookSecret(store SecretStore, slug, value string) error {
	_, err := store.UpdateSecret("workspace", slug, webhookSecretKey, value)
	var notFound *secrets.NotFoundError
	if !errors.As(err, &notFound) {
		return err
	}
	_, err = store.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: webhookSecretKey, Value: value}})
	var conflict *secrets.ConflictError
	if !errors.As(err, &conflict) {
		return err
	}
	_, err = store.UpdateSecret("workspace", slug, webhookSecretKey, value)
	return err
}

// isScopeFull reports whether err is the store's refusal to hold more entries in a scope. The
// store gives it no type, so it is told by its text.
func isScopeFull(err error) bool {
	return err.Error() == fmt.Sprintf("maximum of %d entries per scope exceeded", secrets.MaxEntriesPerScope)
}

// webhookStoreFailure logs a failure of the secrets store and answers it 500. The error is
// not logged as text: it may carry what the store held, and the log must not hold a secret.
func webhookStoreFailure(c echo.Context, what, slug string, err error) error {
	slog.Error("forge: "+what+" failed", "slug", slug, "error_type", fmt.Sprintf("%T", err))
	return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
}

// webhookEvent is the event of a change to a workspace's webhook secret. The actor is the
// caller. The event holds the flag the caller needs to read it and never the secret.
func webhookEvent(req *request, eventType, action string, metadata map[string]any) audit.HubEvent {
	return audit.HubEvent{
		EventType:    eventType,
		ActorID:      req.auth.UserID,
		ActorType:    req.auth.CredentialType,
		ResourceType: "webhook",
		ResourceID:   req.slug,
		Action:       action,
		Workspace:    req.slug,
		Metadata:     metadata,
	}
}

// emitEvent hands event to the emitter, on a context that outlives the request. Without an
// emitter nothing is recorded, and an emitter that fails is logged and changes nothing about
// the answer.
func (h *handlers) emitEvent(ctx context.Context, event audit.HubEvent) {
	if h.cfg.Audit == nil {
		return
	}
	if err := h.cfg.Audit.Emit(context.WithoutCancel(ctx), event); err != nil {
		slog.Error("audit: failed to emit "+event.EventType, "workspace", event.Workspace, "error", err)
	}
}
