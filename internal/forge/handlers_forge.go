package forge

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// forgeInfo is the body of GET /workspaces/:slug/forge: how hub reaches the forge of the
// workspace's origin and, on a carry_patch workspace, of its upstream.
type forgeInfo struct {
	remoteInfo
	// Upstream has the same four fields for the upstream_url of a carry_patch
	// workspace, and is null on any other workspace.
	Upstream *remoteInfo `json:"upstream"`
	// Webhook says whether the workspace's webhook is set up and when a forge last posted
	// to it.
	Webhook webhookInfo `json:"webhook"`
}

// webhookInfo is the webhook block of GET /workspaces/:slug/forge. It never holds the secret.
type webhookInfo struct {
	// Configured is true when the workspace has a webhook secret.
	Configured bool `json:"configured"`
	// URL is the URL a forge posts the workspace's deliveries to; null when external_url is
	// not set.
	URL *string `json:"url"`
	// LastDeliveryAt is the time the latest delivery of any outcome was received; null when
	// there is none.
	LastDeliveryAt *string `json:"last_delivery_at"`
}

// diagnostic serves GET /workspaces/:slug/forge. It sends nothing to the forge, takes
// no target, and answers with the kind of the credential and never its value, and with
// the state of the workspace's webhook.
func (h *handlers) diagnostic(c echo.Context) error {
	req, err := h.authorize(c, readAccess)
	if req == nil {
		return err
	}
	origin, err := h.describeRemote(req, targetOrigin)
	if err != nil {
		return writeForgeError(c, err)
	}
	info := forgeInfo{remoteInfo: *origin}
	if req.remotes.hasUpstream() {
		if info.Upstream, err = h.describeRemote(req, targetUpstream); err != nil {
			return writeForgeError(c, err)
		}
	}
	if info.Webhook, err = h.webhookInfo(req.slug); err != nil {
		return writeForgeError(c, err)
	}
	return c.JSON(http.StatusOK, info)
}

// webhookInfo reads the state of the workspace's webhook from the secrets store and the
// delivery log. A failure of either is an error, so that no partial block is answered.
func (h *handlers) webhookInfo(slug string) (webhookInfo, error) {
	if h.cfg.Secrets == nil {
		slog.Error("forge: the secrets store is not configured, the webhook state cannot be read")
		return webhookInfo{}, errInternal()
	}
	info := webhookInfo{URL: webhookURL(h.cfg.ExternalURL, slug)}
	_, err := h.cfg.Secrets.GetSecretValue("workspace", slug, webhookSecretKey)
	var notFound *secrets.NotFoundError
	switch {
	case err == nil:
		info.Configured = true
	case !errors.As(err, &notFound):
		// The error is not logged as text: it may carry what the store held.
		slog.Error("forge: reading the webhook secret failed", "slug", slug, "error_type", fmt.Sprintf("%T", err))
		return webhookInfo{}, errInternal()
	}
	var last sql.NullString
	if err := h.cfg.DB.QueryRow(`SELECT MAX(received_at) FROM forge_deliveries WHERE workspace_slug = ?`, slug).Scan(&last); err != nil {
		slog.Error("forge: reading the delivery log failed", "slug", slug, "error", err)
		return webhookInfo{}, errInternal()
	}
	if last.Valid {
		info.LastDeliveryAt = &last.String
	}
	return info, nil
}
