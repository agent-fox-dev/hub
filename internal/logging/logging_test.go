package logging_test

import (
	"os"
	"testing"

	"github.com/agent-fox-dev/hub/internal/logging"
	"github.com/sirupsen/logrus"
)

// ---------------------------------------------------------------------------
// 3.1 — Structured Logging Initialization
// ---------------------------------------------------------------------------

// TestSpec01_LogrusJSONFormatterStdout verifies that InitLogging configures
// logrus with a JSONFormatter, output set to os.Stdout, and the log level
// parsed from the config string.
// TS-01-17, REQ: 01-REQ-5.1
func TestSpec01_LogrusJSONFormatterStdout(t *testing.T) {
	// Save and restore the original logrus state.
	origFormatter := logrus.StandardLogger().Formatter
	origOutput := logrus.StandardLogger().Out
	origLevel := logrus.GetLevel()
	t.Cleanup(func() {
		logrus.SetFormatter(origFormatter)
		logrus.SetOutput(origOutput)
		logrus.SetLevel(origLevel)
	})

	logging.InitLogging("debug")

	// Verify formatter is JSONFormatter.
	formatter := logrus.StandardLogger().Formatter
	if _, ok := formatter.(*logrus.JSONFormatter); !ok {
		t.Errorf("logrus formatter = %T, want *logrus.JSONFormatter", formatter)
	}

	// Verify output is os.Stdout.
	output := logrus.StandardLogger().Out
	if output != os.Stdout {
		t.Errorf("logrus output = %v, want os.Stdout", output)
	}

	// Verify level is debug.
	level := logrus.GetLevel()
	if level != logrus.DebugLevel {
		t.Errorf("logrus level = %v, want %v", level, logrus.DebugLevel)
	}
}

// TestSpec01_LogrusLevelFromConfig verifies that InitLogging sets the correct
// log level for each supported level string.
// TS-01-17, REQ: 01-REQ-5.1
func TestSpec01_LogrusLevelFromConfig(t *testing.T) {
	origLevel := logrus.GetLevel()
	origFormatter := logrus.StandardLogger().Formatter
	origOutput := logrus.StandardLogger().Out
	t.Cleanup(func() {
		logrus.SetLevel(origLevel)
		logrus.SetFormatter(origFormatter)
		logrus.SetOutput(origOutput)
	})

	cases := []struct {
		input    string
		expected logrus.Level
	}{
		{"trace", logrus.TraceLevel},
		{"debug", logrus.DebugLevel},
		{"info", logrus.InfoLevel},
		{"warn", logrus.WarnLevel},
		{"error", logrus.ErrorLevel},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			logging.InitLogging(tc.input)
			if logrus.GetLevel() != tc.expected {
				t.Errorf("InitLogging(%q) -> level = %v, want %v",
					tc.input, logrus.GetLevel(), tc.expected)
			}
		})
	}
}

// TestSpec01_LogrusNoStderrOrFileOutput verifies that after InitLogging,
// logrus output is exclusively os.Stdout — no stderr or file output.
// TS-01-17, REQ: 01-REQ-5.1
func TestSpec01_LogrusNoStderrOrFileOutput(t *testing.T) {
	origFormatter := logrus.StandardLogger().Formatter
	origOutput := logrus.StandardLogger().Out
	origLevel := logrus.GetLevel()
	t.Cleanup(func() {
		logrus.SetFormatter(origFormatter)
		logrus.SetOutput(origOutput)
		logrus.SetLevel(origLevel)
	})

	logging.InitLogging("info")

	output := logrus.StandardLogger().Out
	if output == os.Stderr {
		t.Error("logrus output should not be os.Stderr")
	}
	if output != os.Stdout {
		t.Errorf("logrus output = %v, want os.Stdout (not stderr or file)", output)
	}
}

// TestSpec01_AllSevenLogLevelsIncludingTrace verifies that logrus supports
// all seven log levels {trace, debug, info, warn, error, fatal, panic}
// and that trace maps to logrus.TraceLevel natively without aliasing.
// TS-01-19, REQ: 01-REQ-5.3
func TestSpec01_AllSevenLogLevelsIncludingTrace(t *testing.T) {
	levels := []struct {
		name     string
		expected logrus.Level
	}{
		{"trace", logrus.TraceLevel},
		{"debug", logrus.DebugLevel},
		{"info", logrus.InfoLevel},
		{"warn", logrus.WarnLevel},
		{"error", logrus.ErrorLevel},
		{"fatal", logrus.FatalLevel},
		{"panic", logrus.PanicLevel},
	}

	for _, tc := range levels {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := logrus.ParseLevel(tc.name)
			if err != nil {
				t.Fatalf("logrus.ParseLevel(%q) returned error: %v", tc.name, err)
			}
			if parsed != tc.expected {
				t.Errorf("logrus.ParseLevel(%q) = %v, want %v", tc.name, parsed, tc.expected)
			}
		})
	}
}

// TestSpec01_TraceLevelNative verifies that logrus.TraceLevel is a native
// level (available since logrus v1.4.0) and does not require aliasing
// or fallback. This confirms the go.mod pins >= v1.4.0.
// TS-01-19, REQ: 01-REQ-5.3
func TestSpec01_TraceLevelNative(t *testing.T) {
	origLevel := logrus.GetLevel()
	origFormatter := logrus.StandardLogger().Formatter
	origOutput := logrus.StandardLogger().Out
	t.Cleanup(func() {
		logrus.SetLevel(origLevel)
		logrus.SetFormatter(origFormatter)
		logrus.SetOutput(origOutput)
	})

	// ParseLevel must not return an error for "trace".
	level, err := logrus.ParseLevel("trace")
	if err != nil {
		t.Fatalf("logrus.ParseLevel(\"trace\") returned error: %v (ensure logrus >= v1.4.0)", err)
	}
	if level != logrus.TraceLevel {
		t.Errorf("logrus.ParseLevel(\"trace\") = %v, want logrus.TraceLevel", level)
	}

	// SetLevel to trace and verify it sticks.
	logrus.SetLevel(logrus.TraceLevel)
	if logrus.GetLevel() != logrus.TraceLevel {
		t.Errorf("after SetLevel(TraceLevel), GetLevel() = %v, want TraceLevel", logrus.GetLevel())
	}
}
