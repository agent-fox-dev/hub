// Package main is the entrypoint for the af-hub server.
package main

import (
	"flag"
	"fmt"

	"github.com/agent-fox/af-hub/internal/bootstrap"
	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/db"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/sirupsen/logrus"
)

func main() {
	// Parse command-line flags.
	resetAdminToken := flag.Bool("reset-admin-token", false,
		"Generate a new admin token, update the database, and overwrite the admin_token file")
	flag.Parse()

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

	// Step 4: Open the SQLite database and enable WAL mode.
	database, err := db.OpenDatabase(cfg.Database.Path)
	if err != nil {
		logrus.WithError(err).Fatal("failed to open database")
	}
	defer database.Close()

	// Step 5: Initialize the database schema.
	if err := db.InitSchema(database); err != nil {
		logrus.WithError(err).Fatal("failed to initialize database schema")
	}

	// Step 6: Create the store layer.
	s := store.NewStore(database)

	// Step 7: Admin bootstrap, token validation, or token rotation.
	firstBoot, err := bootstrap.IsFirstBoot(s)
	if err != nil {
		logrus.WithError(err).Fatal("failed to check first boot status")
	}

	if firstBoot {
		// First boot: create admin user and generate token.
		if err := bootstrap.RunAdminBootstrap(s, "."); err != nil {
			logrus.WithError(err).Fatal("admin bootstrap failed")
		}
	} else if *resetAdminToken {
		// Token rotation: generate new token, skip env validation.
		if err := bootstrap.RotateAdminToken(s, "."); err != nil {
			logrus.WithError(err).Fatal("admin token rotation failed")
		}
	} else {
		// Subsequent boot: validate AF_HUB_ADMIN_TOKEN from environment.
		if err := bootstrap.ValidateAdminToken(s); err != nil {
			logrus.WithError(err).Fatal("admin token validation failed")
		}
	}

	// Stub — remaining startup steps (server bind, signal handling) will be
	// implemented in later task groups (10-12).
	fmt.Printf("af-hub: ready (port=%d, bind=%s)\n",
		cfg.Server.Port, cfg.Server.BindAddress)
}
