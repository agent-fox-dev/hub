// Package middleware provides Echo middleware for request logging.
package middleware

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// RequestLoggerMiddleware returns an Echo middleware that logs each HTTP
// request as a structured JSON entry containing method, path, status, and
// duration in milliseconds.
func RequestLoggerMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			err := next(c)

			duration := time.Since(start).Milliseconds()

			// Determine the status code. If the handler returned an
			// *echo.HTTPError (e.g. 404 Not Found), use its code; otherwise
			// use the already-committed response status.
			status := c.Response().Status
			if err != nil {
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				} else {
					// Non-HTTP errors default to 500 if no status was written.
					if status == 0 || status == http.StatusOK {
						status = http.StatusInternalServerError
					}
				}
			}

			logrus.WithFields(logrus.Fields{
				"method":   c.Request().Method,
				"path":     c.Request().URL.Path,
				"status":   status,
				"duration": duration,
			}).Info("request completed")

			return err
		}
	}
}
