package forge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// deliveryEntry is a row of the delivery log as GET /workspaces/:slug/forge/deliveries shows
// it. Target is null for a delivery that concerned no repository of the workspace, and
// Relayed lists the event types handed to the emitter.
type deliveryEntry struct {
	DeliveryID string   `json:"delivery_id"`
	Forge      string   `json:"forge"`
	Event      string   `json:"event"`
	Action     string   `json:"action"`
	Target     *string  `json:"target"`
	Outcome    string   `json:"outcome"`
	Relayed    []string `json:"relayed"`
	ReceivedAt string   `json:"received_at"`
}

// listDeliveries serves GET /workspaces/:slug/forge/deliveries: the workspace's recorded
// deliveries, newest first, whatever the workspace's status. It answers a bare array.
func (h *handlers) listDeliveries(c echo.Context) error {
	req, err := h.authorize(c, readAccess)
	if req == nil {
		return err
	}
	since, limit, ref := parseDeliveryQuery(c.QueryParams())
	if ref != nil {
		return writeRefusal(c, ref)
	}
	entries, err := h.deliveries(req.slug, since, limit)
	if err != nil {
		slog.Error("forge: reading the delivery log failed", "slug", req.slug, "error", err)
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
	}
	return c.JSON(http.StatusOK, entries)
}

// parseDeliveryQuery reads since and limit. since is returned as the lower bound of
// received_at, a timestamp in the form the log stores, or "" for none. The log keeps whole
// seconds, so a since with a fraction of a second is rounded up to the next one: the rows
// from since on are the rows from that second on.
func parseDeliveryQuery(q url.Values) (since string, limit int, ref *refusal) {
	if values, present := q["since"]; present {
		if len(values) != 1 {
			return "", 0, badRequest("since must be given once")
		}
		t, err := apikit.ParseUTC(values[0])
		if err != nil {
			return "", 0, badRequest("since must be an RFC 3339 timestamp, for example 2026-10-21T09:00:00Z")
		}
		if t.Nanosecond() != 0 {
			t = t.Truncate(time.Second).Add(time.Second)
		}
		since = apikit.FormatUTC(t)
	}
	limit, ref = parseLimit(q)
	return since, limit, ref
}

// deliveries reads at most limit rows of the workspace's delivery log received at since or
// later, newest first. Rows of the same second come last recorded first.
func (h *handlers) deliveries(slug, since string, limit int) ([]deliveryEntry, error) {
	rows, err := h.cfg.DB.Query(`SELECT delivery_id, forge, event, action, target, outcome, relayed, received_at
		FROM forge_deliveries WHERE workspace_slug = ? AND received_at >= ?
		ORDER BY received_at DESC, rowid DESC LIMIT ?`, slug, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []deliveryEntry{}
	for rows.Next() {
		var e deliveryEntry
		var target *string
		var relayed string
		if err := rows.Scan(&e.DeliveryID, &e.Forge, &e.Event, &e.Action, &target, &e.Outcome, &relayed, &e.ReceivedAt); err != nil {
			return nil, err
		}
		e.Target = target
		if err := json.Unmarshal([]byte(relayed), &e.Relayed); err != nil {
			return nil, err
		}
		if e.Relayed == nil {
			e.Relayed = []string{}
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
