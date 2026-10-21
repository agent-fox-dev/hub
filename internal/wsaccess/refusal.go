package wsaccess

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// ErrorTypeForbidden is the error_type of the 403 a member of a workspace's
// org receives on an endpoint that needs NeedWrite. A PAT's scope refusal
// carries no error_type, so a client can tell the two apart.
const ErrorTypeForbidden = "forbidden"

// WriteRefusal writes a refusal returned by AuthorizeWorkspace (status and
// msg) in the apikit error envelope: with error_type ErrorTypeForbidden on a
// 403, and without an error_type on any other status.
//
// It returns what apikit returns, which is nil once the response is written,
// so a caller must decide that it has refused from the status, not from the
// returned error.
func WriteRefusal(c echo.Context, status int, msg string) error {
	if status == http.StatusForbidden {
		return apikit.WriteAPIErrorWithType(c, status, msg, ErrorTypeForbidden)
	}
	return apikit.WriteAPIError(c, status, msg)
}
