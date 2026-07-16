// Package main implements the af-hub server binary.
//
// CLI flags:
//
//	--config <path>        Path to config.toml (default: ./config.toml)
//	--reset-admin-token    Bypass AF_HUB_ADMIN_TOKEN validation and generate
//	                       a fresh admin token, replacing the existing one.
//
// Both flags are independent and can be combined freely.
//
// Startup sequence (REQ 01-REQ-2.1):
//
//  1. Parse CLI flags
//  2. Load config.toml
//  3. Initialize structured logging
//  4. Open/initialize SQLite
//  5. Run admin bootstrap or token validation
//  6. Register HTTP routes and middleware, custom error handler
//  7. Log startup info
//  8. Start HTTP listener
//  9. Arm SIGTERM/SIGINT handler
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/labstack/echo/v4"
	echoMw "github.com/labstack/echo/v4/middleware"
	log "github.com/sirupsen/logrus"

	"github.com/agent-fox-dev/hub/internal/admin"
	"github.com/agent-fox-dev/hub/internal/auth"
	"github.com/agent-fox-dev/hub/internal/db"
	"github.com/agent-fox-dev/hub/internal/handler"
	"github.com/agent-fox-dev/hub/internal/middleware"
	"github.com/agent-fox-dev/hub/internal/server"
	"github.com/agent-fox-dev/hub/internal/serverconfig"
)

// version is injected at build time via -ldflags.
var version = "0.1.0"

func main() {
	// Step 1: Parse CLI flags.
	configPath := flag.String("config", "", "path to config.toml (default: $XDG_CONFIG_HOME/af-hub/config.toml)")
	resetAdminToken := flag.Bool("reset-admin-token", false, "bypass AF_HUB_ADMIN_TOKEN validation and generate a fresh admin token")
	flag.Parse()

	// Step 2: Load config.toml.
	// When --config is not provided, use XDG Base Directory paths for
	// config and data. When explicitly provided, use CWD-relative defaults
	// for backward compatibility.
	useXDG := *configPath == ""
	resolvedConfigPath := *configPath
	if useXDG {
		resolvedConfigPath = serverconfig.DefaultConfigPath()
	}
	result, err := serverconfig.LoadConfig(resolvedConfigPath, useXDG)
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

	// Step 4: Open/initialize SQLite database.
	database, err := db.InitDatabase(result.Config.Database.Path)
	if err != nil {
		log.WithError(err).Fatal("failed to initialize database")
	}
	defer database.Close()

	// Step 5: Run admin bootstrap or token validation.
	bootstrapResult, bootstrapErr := admin.Bootstrap(database, result.ConfigDir, *resetAdminToken)
	if bootstrapErr != nil {
		log.WithError(bootstrapErr).Fatal("admin bootstrap failed")
	}
	if bootstrapResult.IsFirstBoot {
		log.WithField("path", bootstrapResult.TokenFilePath).
			Info("admin token generated; save it and set AF_HUB_ADMIN_TOKEN before restarting")
		database.Close()
		os.Exit(0)
	}

	// Step 6: Register HTTP routes and middleware, custom error handler.
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = handler.CustomErrorHandler

	// Global middleware stack: Recover → request logger → body-size limit.
	// RequestLogger runs before BodySizeLimit so X-Request-ID is set even on 413 responses.
	e.Use(echoMw.Recover())
	e.Use(middleware.RequestLoggerMiddleware())
	e.Use(middleware.BodySizeLimitMiddleware("1M"))

	// Health probes on root Echo instance (no group, no auth middleware).
	e.GET("/healthz", handler.HealthzHandler())
	e.HEAD("/healthz", handler.HealthzHandler())
	e.GET("/readyz", handler.ReadyzHandler(database))
	e.HEAD("/readyz", handler.ReadyzHandler(database))

	// Auth group at /api/v1/auth — no auth middleware.
	// Any("/*", ...) catch-all ensures unregistered auth paths stay within
	// this group and don't fall through to the protected group's auth middleware.
	authGroup := e.Group("/api/v1/auth")
	authGroup.Any("/*", func(c echo.Context) error {
		return echo.ErrNotFound
	})

	// Protected group at /api/v1 — with spec 01 auth middleware.
	e.Group("/api/v1", middleware.AuthMiddleware(database))

	// Register spec 02/04 routes (OAuth, users, keys, workspaces).
	registry := auth.NewRegistry()
	for _, p := range result.Config.OAuth.Providers {
		cfg := auth.ProviderConfig{
			Name:         p.Name,
			ClientID:     p.ClientID,
			ClientSecret: p.ClientSecret,
			AuthorizeURL: p.AuthorizeURL,
			TokenURL:     p.TokenURL,
			UserInfoURL:  p.UserinfoURL,
			Scopes:       "",
		}
		switch p.Name {
		case "github":
			registry.Register(p.Name, auth.NewGitHubProvider(cfg), cfg)
		case "google":
			registry.Register(p.Name, auth.NewGoogleProvider(cfg), cfg)
		default:
			log.WithField("provider", p.Name).Warn("unknown OAuth provider; skipping")
		}
	}
	devMode := result.Config.Server.ExternalURL == ""
	allowlist := auth.NewAllowlist(result.Config.Server.ExternalURL, devMode)
	server.RegisterRoutes(e, database, registry, allowlist)

	// Step 7: Log startup info.
	log.WithFields(log.Fields{
		"bind":      result.Config.Server.Bind,
		"port":      result.Config.Server.Port,
		"db_path":   result.Config.Database.Path,
		"log_level": result.Config.Log.Level,
	}).Info("server starting")

	// Step 8: Start HTTP listener in a goroutine.
	addr := fmt.Sprintf("%s:%d", result.Config.Server.Bind, result.Config.Server.Port)
	go func() {
		if err := e.Start(addr); err != nil {
			// echo.Start returns http.ErrServerClosed on graceful shutdown,
			// which is expected. Other errors are fatal.
			log.WithError(err).Info("http server stopped")
		}
	}()

	// Step 9: Arm SIGTERM/SIGINT handler and block until signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	// Graceful shutdown with 15-second drain timeout.
	shutdownErr := middleware.GracefulShutdown(e, middleware.ShutdownTimeout)
	if shutdownErr != nil {
		if shutdownErr == context.DeadlineExceeded {
			log.Warn("graceful shutdown timed out; some connections may have been dropped")
		} else {
			log.WithError(shutdownErr).Warn("error during shutdown")
		}
	} else {
		log.Info("server shutdown complete")
	}
}
