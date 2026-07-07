package logging

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sirupsen/logrus"
)

// TS-01-31: Verify that logrus is configured with JSONFormatter as the sole
// logging backend.
func TestConfigureLogging_JSONFormatter(t *testing.T) {
	err := ConfigureLogging("info")
	if err != nil {
		t.Fatalf("ConfigureLogging returned error: %v", err)
	}

	// Capture log output.
	var buf bytes.Buffer
	logrus.SetOutput(&buf)
	t.Cleanup(func() { logrus.SetOutput(nil) })

	logrus.Info("test message")

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output, got empty string")
	}

	// Verify the output is valid JSON.
	var entry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\nOutput: %s", err, output)
	}

	// Verify required fields exist.
	if _, ok := entry["msg"]; !ok {
		t.Error("log entry should contain 'msg' field")
	}
	if _, ok := entry["level"]; !ok {
		t.Error("log entry should contain 'level' field")
	}
}

// TS-01-33: Verify that the server applies the configured log level from
// config.toml to the logrus global log level before starting.
func TestConfigureLogging_SetsLogLevel(t *testing.T) {
	tests := []struct {
		level    string
		expected logrus.Level
	}{
		{"debug", logrus.DebugLevel},
		{"info", logrus.InfoLevel},
		{"warn", logrus.WarnLevel},
		{"error", logrus.ErrorLevel},
		{"fatal", logrus.FatalLevel},
		{"panic", logrus.PanicLevel},
	}

	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			err := ConfigureLogging(tc.level)
			if err != nil {
				t.Fatalf("ConfigureLogging(%q) returned error: %v", tc.level, err)
			}

			if logrus.GetLevel() != tc.expected {
				t.Errorf("expected logrus level %v, got %v",
					tc.expected, logrus.GetLevel())
			}
		})
	}
}

// TS-01-33 continued: Verify that trace level is also accepted.
func TestConfigureLogging_TraceLevel(t *testing.T) {
	err := ConfigureLogging("trace")
	if err != nil {
		t.Fatalf("ConfigureLogging('trace') returned error: %v", err)
	}

	if logrus.GetLevel() != logrus.TraceLevel {
		t.Errorf("expected logrus level Trace, got %v", logrus.GetLevel())
	}
}

// TS-01-33: Verify that an invalid log level returns an error.
func TestConfigureLogging_InvalidLevel(t *testing.T) {
	err := ConfigureLogging("nonexistent")
	if err == nil {
		t.Fatal("expected error for invalid log level 'nonexistent', got nil")
	}
}
