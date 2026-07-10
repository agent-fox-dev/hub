// Package main implements the af-hub server binary.
//
// CLI flags:
//
//	--config <path>        Path to config.toml (default: ./config.toml)
//	--reset-admin-token    Bypass AF_HUB_ADMIN_TOKEN validation and generate
//	                       a fresh admin token, replacing the existing one.
//
// Both flags are independent and can be combined freely.
// Full startup sequence wiring is implemented in task group 14.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/agent-fox-dev/hub/internal/serverconfig"
	log "github.com/sirupsen/logrus"
)

// version is injected at build time via -ldflags.
var version = "0.1.0"

func main() {
	// Step 1: Parse CLI flags.
	configPath := flag.String("config", "./config.toml", "path to config.toml")
	resetAdminToken := flag.Bool("reset-admin-token", false, "bypass AF_HUB_ADMIN_TOKEN validation and generate a fresh admin token")
	flag.Parse()

	// Step 2: Load config.toml.
	result, err := serverconfig.LoadConfig(*configPath)
	if err != nil {
		log.WithError(err).Fatal("failed to load configuration")
	}

	// Emit warnings for unrecognized fields.
	for _, key := range result.UnrecognizedKeys {
		log.WithField("field", key).Warn("unrecognized config field")
	}

	// Emit warning for invalid log level.
	if result.InvalidLogLevel != "" {
		log.WithField("invalid_value", result.InvalidLogLevel).
			Warn("unrecognized log level; defaulting to info")
	}

	// Step 3: Initialize structured logging.
	log.SetFormatter(&log.JSONFormatter{})
	log.SetOutput(os.Stdout)
	level, err := log.ParseLevel(result.Config.Log.Level)
	if err != nil {
		// This should not happen since LoadConfig validates the level,
		// but handle it defensively.
		level = log.InfoLevel
	}
	log.SetLevel(level)

	// Steps 4-9: remaining startup sequence will be wired in task group 14.
	// For now, log the startup info and exit.
	_ = resetAdminToken // Will be used in step 5 (admin bootstrap/validation).
	_ = result.ConfigDir // Will be used for admin_token file placement.

	log.WithFields(log.Fields(serverconfig.StartupLogFields(result.Config))).Info()

	fmt.Fprintf(os.Stderr, "af-hub v%s: full startup sequence not yet implemented (task group 14)\n", version)
	os.Exit(1)
}
