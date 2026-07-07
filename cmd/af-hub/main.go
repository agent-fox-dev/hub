// Package main is the entrypoint for the af-hub server.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/bootstrap"
	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/db"
	"github.com/agent-fox/af-hub/internal/logging"
	"github.com/agent-fox/af-hub/internal/server"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/sirupsen/logrus"
)

func main() {
	os.Exit(run())
}

func run() int {
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

	// Step 3: Configure structured JSON logging with the configured level.
	if err := logging.ConfigureLogging(cfg.Logging.Level); err != nil {
		logrus.WithError(err).Fatal("failed to configure logging")
	}

	// Step 4: Ensure the database directory exists.
	if err := config.EnsureDataDir(cfg.Database.Path); err != nil {
		logrus.WithError(err).Fatal("failed to create data directory")
	}

	// Step 5: Open the SQLite database and enable WAL mode.
	database, err := db.OpenDatabase(cfg.Database.Path)
	if err != nil {
		logrus.WithError(err).Fatal("failed to open database")
	}

	// Step 6: Initialize the database schema.
	if err := db.InitSchema(database); err != nil {
		logrus.WithError(err).Fatal("failed to initialize database schema")
	}

	// Step 7: Create the store layer.
	s := store.NewStore(database)

	// Step 8: Admin bootstrap, token validation, or token rotation.
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

	// Step 9: Initialize the OAuth provider registry from config.
	registry := auth.NewRegistry(&cfg.Auth)

	// Step 10: Create and start the HTTP server with full API surface.
	e := server.SetupServer(cfg, s, registry, database)
	addr := fmt.Sprintf("%s:%d", cfg.Server.BindAddress, cfg.Server.Port)
	logrus.WithField("address", addr).Info("starting HTTP server")

	// Start the Echo server in a goroutine so we can handle signals.
	serverErr := make(chan error, 1)
	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	// Step 11: Listen for SIGTERM/SIGINT for graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-quit:
		logrus.WithField("signal", sig.String()).Info("received shutdown signal")
	case err := <-serverErr:
		if err != nil {
			logrus.WithError(err).Error("server error")
			closeDBWithTimeout(database, 5*time.Second)
			return 1
		}
	}

	// Step 12: Initiate graceful shutdown with a 15-second drain timeout.
	const drainTimeout = 15 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()

	logrus.Info("shutting down server, waiting for in-flight requests to drain")
	shutdownErr := e.Shutdown(ctx)

	// Step 13: Close the database connection with a 5-second timeout.
	const dbCloseTimeout = 5 * time.Second
	closeDBWithTimeout(database, dbCloseTimeout)

	if shutdownErr != nil {
		logrus.WithError(shutdownErr).Warn("graceful shutdown timed out, forcing exit")
		return 1
	}

	logrus.Info("server shut down gracefully")
	return 0
}

// closeDBWithTimeout attempts to close the database connection within the given
// timeout. If db.Close() does not return within the timeout, it logs an error
// and returns without blocking indefinitely.
func closeDBWithTimeout(database *sql.DB, timeout time.Duration) {
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- database.Close()
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			logrus.WithError(err).Error("error closing database")
		}
	case <-time.After(timeout):
		logrus.Error("database close timed out")
	}
}
