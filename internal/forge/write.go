package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// maxNames is how many labels or assignees a request may name.
const maxNames = 100

// writeRoute is what is a write route's own: what it reads of the request and what it
// sends to the forge. The checks and the steps around it are the same for every route.
type writeRoute struct {
	// operation names what the route does, for the log line of a call that failed.
	operation string
	// event identifies the route's audit event.
	event eventKind
	// prepare reads the path and the body of the request and checks them. It runs before
	// a client is built, and returns the plan, or the refusal to answer.
	prepare func(c echo.Context, body []byte) (*writePlan, *refusal)
}

// eventKind is what identifies the audit event of a route.
type eventKind struct {
	eventType, resource, action string
}

// writePlan is what a route has made of a request.
type writePlan struct {
	// resourceID is the resource_id of the event when the request already names it: the
	// number in the path, or the name of a label. A create sets it from the answer.
	resourceID string
	// number is the number of the issue or pull request the path names, 0 for none.
	number int
	// texts are the body-like fields, measured against the forge's limit once the forge
	// is known.
	texts []textField
	// extra is the metadata of the event besides the common ones.
	extra map[string]any
	// send sends the write to the forge, through w.call, and returns the answer.
	send func(w *writeRun) (*writeAnswer, error)
}

// writeAnswer is the success answer of a write.
type writeAnswer struct {
	status int
	// body is encoded as JSON; nil answers without a body.
	body any
}

func (a *writeAnswer) write(c echo.Context) error {
	if a.body == nil {
		return c.NoContent(a.status)
	}
	return c.JSON(a.status, a.body)
}

// writeRun is a write on its way to the forge, and what the route's event will say of it.
type writeRun struct {
	c  echo.Context
	fc *forgeClient
	// api is what the write calls on the forge.
	api writer

	// sent is whether a request has been sent to the forge: only then is there an event.
	sent       bool
	resourceID string
	number     int
	url        string
	extra      map[string]any
}

// call sends one request to the forge. The call runs on the request's context with its
// cancellation detached, so that a write which has left hub finishes and is recorded
// when the caller goes away, and has a deadline of its own: every request of a write
// that takes several gets the whole of it.
func (w *writeRun) call(f func(ctx context.Context) error) error {
	w.sent = true
	ctx := withCredential(context.WithoutCancel(w.c.Request().Context()), w.fc.token)
	ctx, cancel := context.WithTimeout(ctx, callDeadline)
	defer cancel()
	return f(ctx)
}

// write is the handler of a write route. It applies the checks that come first on every
// write route, in this order: no credential is 401, a credential without forge:write is
// 403, a workspace that does not exist or is not the caller's is 404, a workspace that
// is not active is 400. Then the request itself is read and checked (400), the client is
// resolved (422 and the like), the body-like fields are measured against the forge's
// limit (413), and only then is the write sent to the forge and recorded.
func (h *handlers) write(rt writeRoute) echo.HandlerFunc {
	return func(c echo.Context) error {
		req, err := h.authorize(c, writeAccess)
		if req == nil {
			return err
		}
		tgt, ref := resolveTarget(c.QueryParams(), req.remotes)
		if ref != nil {
			return writeRefusal(c, ref)
		}
		data, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return writeRefusal(c, badRequest("the request body cannot be read"))
		}
		plan, ref := rt.prepare(c, data)
		if ref != nil {
			return writeRefusal(c, ref)
		}
		fc, err := h.buildClient(req, tgt, true)
		if err != nil {
			return writeForgeError(c, err)
		}
		if ref := checkBodyLimits(fc.forge, plan.texts); ref != nil {
			return writeRefusal(c, ref)
		}

		w := &writeRun{c: c, fc: fc, api: h.writerFor(fc), resourceID: plan.resourceID, number: plan.number, extra: plan.extra}
		answer, err := plan.send(w)
		if err != nil {
			fe := h.mapCall(req, fc, rt.operation, err)
			h.emit(c, req, rt, w, fe.errorType)
			return writeForgeError(c, fe)
		}
		h.emit(c, req, rt, w, "")
		return answer.write(c)
	}
}

// notImplemented is the route of a write whose action is not delivered yet.
var notImplemented = writeRoute{
	prepare: func(echo.Context, []byte) (*writePlan, *refusal) {
		return nil, &refusal{status: http.StatusNotImplemented, message: "not implemented"}
	},
}

// body is the JSON object of a request body, each field still raw JSON. Fields a route
// does not know are ignored.
type body map[string]json.RawMessage

// parseBody reads the request body as a JSON object.
func parseBody(data []byte) (body, *refusal) {
	var b body
	if err := json.Unmarshal(data, &b); err != nil || b == nil {
		return nil, badRequest("the request body must be a JSON object")
	}
	return b, nil
}

// raw returns the JSON of a field; false when the field is absent or null, which both
// mean that the request does not set it.
func (b body) raw(name string) (json.RawMessage, bool) {
	v, ok := b[name]
	if !ok || string(bytes.TrimSpace(v)) == "null" {
		return nil, false
	}
	return v, true
}

// text reads an optional string field.
func (b body) text(name string) (value string, present bool, ref *refusal) {
	raw, ok := b.raw(name)
	if !ok {
		return "", false, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, badRequest("%s must be a string", name)
	}
	return value, true, nil
}

// required reads a string field that must be set and must not be blank.
func (b body) required(name string) (string, *refusal) {
	value, present, ref := b.text(name)
	switch {
	case ref != nil:
		return "", ref
	case !present:
		return "", badRequest("%s is required", name)
	case strings.TrimSpace(value) == "":
		return "", badRequest("%s must not be blank", name)
	}
	return value, nil
}

// names reads an optional list of at most maxNames names, none of them blank.
func (b body) names(name string) (list []string, present bool, ref *refusal) {
	raw, ok := b.raw(name)
	if !ok {
		return nil, false, nil
	}
	list, ref = decodeNames(name, raw)
	return list, true, ref
}

// decodeNames reads a JSON array of at most maxNames names, none of them blank. An
// empty array is a list.
func decodeNames(field string, raw json.RawMessage) ([]string, *refusal) {
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil || list == nil {
		return nil, badRequest("%s must be an array of strings", field)
	}
	if len(list) > maxNames {
		return nil, badRequest("%s must have at most %d entries", field, maxNames)
	}
	for _, name := range list {
		if strings.TrimSpace(name) == "" {
			return nil, badRequest("%s must not have a blank entry", field)
		}
	}
	return list, nil
}
