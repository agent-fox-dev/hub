// Package main is the entrypoint for the af-hub server.
package main

import (
	"fmt"

	"github.com/agent-fox/af-hub/internal/config"
	"github.com/sirupsen/logrus"
)

func main() {
	// Step 1: Load configuration from config.toml in the current directory.
	cfg, err := config.LoadConfig("config.toml")
	if err != nil {
		logrus.WithError(err).Fatal("failed to load configuration")
	}

	// Step 2: Validate configuration.
	if err := config.ValidateConfig(cfg); err != nil {
		logrus.WithError(err).Fatal("invalid configuration")
	}

	// Step 3: Ensure the database directory exists.
	if err := config.EnsureDataDir(cfg.Database.Path); err != nil {
		logrus.WithError(err).Fatal("failed to create data directory")
	}

	// Stub — remaining startup steps (DB init, bootstrap, server) will be
	// implemented in later task groups.
	fmt.Printf("af-hub: configuration loaded (port=%d, bind=%s)\n",
		cfg.Server.Port, cfg.Server.BindAddress)
}
