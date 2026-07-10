// Package logging handles structured logging initialization for af-hub.
// It configures logrus with a JSON formatter, stdout output, and the
// log level from the server configuration.
//
// Implementation will be added in task group 9.
package logging

import (
	"github.com/sirupsen/logrus"
)

// InitLogging configures the global logrus instance with:
//   - JSON formatter (&logrus.JSONFormatter{})
//   - stdout as the sole output destination
//   - the log level parsed from the config string
//
// The level string must already be validated by the config loader.
// Valid levels: trace, debug, info, warn, error, fatal, panic.
//
// This function operates on the global logrus instance (logrus.SetFormatter,
// logrus.SetOutput, logrus.SetLevel) as required by REQ-5.1.
func InitLogging(level string) {
	// Stub: no-op. Implementation in task group 9.
	_ = level
	_ = logrus.TraceLevel // ensure logrus is imported
}
