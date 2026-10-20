package forge

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// readOp is a call to the forge for a built client. Its result is what the route
// answers 200 with.
type readOp func(ctx context.Context, fc *forgeClient) (any, error)

// prepareFunc reads and checks the parameters of a route, after the target and before
// a client is built. It returns the call to make, or the refusal to answer.
type prepareFunc func(c echo.Context) (readOp, *refusal)

// serve is the handler of a read route. It applies the checks every forge route
// shares, then the target, then the route's own parameters, and only then builds the
// client and calls the forge.
func (h *handlers) serve(prepare prepareFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req, err := h.authorize(c)
		if req == nil {
			return err
		}
		tgt, ref := resolveTarget(c.QueryParams(), req.remotes)
		if ref != nil {
			return writeRefusal(c, ref)
		}
		op, ref := prepare(c)
		if ref != nil {
			return writeRefusal(c, ref)
		}
		fc, err := h.buildClient(req, tgt)
		if err != nil {
			return writeForgeError(c, err)
		}
		result, err := op(c.Request().Context(), fc)
		if err != nil {
			return writeForgeError(c, err)
		}
		return c.JSON(http.StatusOK, result)
	}
}

// writeRefusal answers a request that hub refuses before it contacts the forge.
func writeRefusal(c echo.Context, ref *refusal) error {
	return apikit.WriteAPIErrorWithType(c, ref.status, ref.message, ref.errorType)
}

// prepareRepository serves GET /workspaces/:slug/forge/repository.
func prepareRepository(echo.Context) (readOp, *refusal) {
	return func(ctx context.Context, fc *forgeClient) (any, error) {
		return fc.api.GetRepository(ctx, fc.repo)
	}, nil
}
