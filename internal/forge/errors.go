package forge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// forgeError is a failure of a forge route with the status and the error_type it is
// answered with. Failures of hub's own checks, of building the client and of the call
// to the forge are all of this type.
type forgeError struct {
	status    int
	errorType string
	message   string
	// retryAfter is the whole seconds for the Retry-After header; 0 sets none.
	retryAfter int
	// forgeStatus is the status the forge answered, when it answered one.
	forgeStatus int
}

func (e *forgeError) Error() string { return e.message }

// errRedirectRefused is what the shared HTTP client's redirect policy returns for a
// redirect it does not follow.
var errRedirectRefused = errors.New("redirect to another host refused")

// basicTokenHint is added to the answer of a forge that refused a basic credential: its
// password is sent as the token, and the forge API wants a token.
const basicTokenHint = " The forge API needs a token: store one as GIT_PAT, or as GIT_PASSWORD."

// errInternal is the answer to a failure of hub's own stores. Its cause is logged
// where it happens, without any secret value, and never put in the response.
func errInternal() *forgeError {
	return &forgeError{status: http.StatusInternalServerError, message: "internal server error"}
}

// writeForgeError answers err on c. An error that is not a *forgeError is a failure of
// hub's own and is answered 500.
func writeForgeError(c echo.Context, err error) error {
	var fe *forgeError
	if !errors.As(err, &fe) {
		fe = errInternal()
	}
	if fe.retryAfter > 0 {
		c.Response().Header().Set("Retry-After", strconv.Itoa(fe.retryAfter))
	}
	return apikit.WriteAPIErrorWithType(c, fe.status, fe.message, fe.errorType)
}

// failCall answers a call to the forge that failed. A 5xx or 429 answer is logged once,
// with the workspace, the target, the operation and the status the forge gave; the line
// holds no message, token or body.
func (h *handlers) failCall(c echo.Context, req *request, fc *forgeClient, operation string, err error) error {
	fe := mapForgeError(err, fc.kind, fc.token)
	if fe.status >= http.StatusInternalServerError || fe.status == http.StatusTooManyRequests {
		slog.Warn("forge: request failed",
			"slug", req.slug, "target", string(fc.target), "operation", operation,
			"forge_status", fe.forgeStatus, "status", fe.status, "error_type", fe.errorType)
	}
	return writeForgeError(c, fe)
}

// mapForgeError maps the error of a call to the forge, issuex's or the transport's, to
// the one answer the error table of the API gives for it. The forge's message is scrubbed
// of the token and of URL userinfo and cut to 1024 bytes.
func mapForgeError(err error, kind credentialKind, token string) *forgeError {
	var he *issuex.HTTPError
	errors.As(err, &he)
	forgeStatus := 0
	if he != nil {
		forgeStatus = he.Status
	}
	answer := func(status int, errorType, text string) *forgeError {
		return &forgeError{status: status, errorType: errorType, message: scrub(text, token), forgeStatus: forgeStatus}
	}

	switch {
	case errors.Is(err, errRedirectRefused):
		return answer(http.StatusBadGateway, "forge_error", "the forge redirected the request to another host, which hub does not follow")
	case errors.Is(err, context.DeadlineExceeded):
		return answer(http.StatusGatewayTimeout, "forge_timeout", "the forge did not answer in time")
	case errors.Is(err, context.Canceled):
		return answer(http.StatusBadGateway, "forge_error", "the request was cancelled")
	case issuex.IsRateLimited(err):
		out := answer(http.StatusTooManyRequests, "forge_rate_limited", forgeText(he, err))
		if he != nil {
			if wait, ok := he.RetryAfter(); ok {
				out.retryAfter = retryAfterSeconds(wait)
			}
		}
		return out
	case issuex.IsNotFound(err):
		return answer(http.StatusNotFound, "forge_not_found", forgeText(he, err))
	case he != nil:
		switch {
		case he.Status == http.StatusUnauthorized:
			out := answer(http.StatusBadGateway, "forge_unauthenticated", forgeText(he, err))
			if kind == credentialBasic {
				out.message += basicTokenHint
			}
			return out
		case he.Status == http.StatusForbidden:
			return answer(http.StatusBadGateway, "forge_forbidden", forgeText(he, err))
		case he.Status >= 400 && he.Status < 500:
			return answer(http.StatusUnprocessableEntity, "forge_rejected", forgeText(he, err))
		}
		return answer(http.StatusBadGateway, "forge_error", forgeText(he, err))
	case errors.Is(err, issuex.ErrConflict):
		return answer(http.StatusUnprocessableEntity, "forge_rejected", err.Error())
	case isConnectionFailure(err):
		return answer(http.StatusBadGateway, "forge_unreachable", err.Error())
	}
	return answer(http.StatusBadGateway, "forge_error", err.Error())
}

// forgeText is the forge's own message for an error, before it is scrubbed.
func forgeText(he *issuex.HTTPError, err error) string {
	switch {
	case he == nil:
		return err.Error()
	case he.Message == "":
		return fmt.Sprintf("the forge answered %d", he.Status)
	}
	return fmt.Sprintf("the forge answered %d: %s", he.Status, he.Message)
}

// isConnectionFailure reports whether the request itself failed: issuex wraps what the
// HTTP client returns for DNS, TCP and TLS failures, and for a connection that went away,
// as a *url.Error, and the errors of reading and decoding a response are not.
func isConnectionFailure(err error) bool {
	var ue *url.Error
	return errors.As(err, &ue)
}

// retryAfterSeconds rounds a wait up to whole seconds.
func retryAfterSeconds(wait time.Duration) int {
	return int(math.Ceil(wait.Seconds()))
}
