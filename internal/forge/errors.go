package forge

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// forgeError is a failure of a forge route with the status and the error_type it is
// answered with. Failures of hub's own checks and of building the client are all of
// this type.
type forgeError struct {
	status    int
	errorType string
	message   string
}

func (e *forgeError) Error() string { return e.message }

// errInternal is the answer to a failure of hub's own stores. Its cause is logged
// where it happens, without any secret value, and never put in the response.
func errInternal() *forgeError {
	return &forgeError{status: http.StatusInternalServerError, message: "internal server error"}
}

// writeForgeError answers err on c. A *forgeError is answered as it says. Any other
// error is a failure of the call to the forge and is answered 502 forge_error with a
// fixed message.
func writeForgeError(c echo.Context, err error) error {
	var fe *forgeError
	if errors.As(err, &fe) {
		return apikit.WriteAPIErrorWithType(c, fe.status, fe.message, fe.errorType)
	}
	return apikit.WriteAPIErrorWithType(c, http.StatusBadGateway, "the forge request failed", "forge_error")
}
