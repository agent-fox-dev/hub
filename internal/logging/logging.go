// Package logging configures structured JSON logging via logrus.
package logging

import (
	"fmt"

	"github.com/sirupsen/logrus"
)

// ConfigureLogging sets up logrus with JSONFormatter and the given log level.
// It sets the global logrus formatter to JSON and parses the provided level
// string to set the global log level. Returns an error if the level string
// is not a valid logrus level.
func ConfigureLogging(level string) error {
	logrus.SetFormatter(&logrus.JSONFormatter{})

	parsed, err := logrus.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("invalid log level %q: %w", level, err)
	}

	logrus.SetLevel(parsed)
	return nil
}
