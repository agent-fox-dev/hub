package main

import (
	"context"
	"flag"
	"log"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/health"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

func main() {
	adminEmail := flag.String("admin-email", "", "admin email for first-boot bootstrap")
	resetToken := flag.Bool("reset-admin-token", false, "rotate the admin token")
	flag.Parse()

	cfg, err := apikit.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	database, err := apikit.OpenDatabase(cfg.Database.Path)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	if err := apikit.Bootstrap(context.Background(), database, apikit.BootstrapOptions{
		AdminEmail: *adminEmail,
		ResetToken: *resetToken,
	}); err != nil {
		log.Fatal(err)
	}

	server := apikit.NewServer(cfg, health.NewDBChecker(database))

	// Mount all built-in handlers (OAuth, users, orgs, keys, PATs) and
	// workspace handlers with workspace permission scopes registered.
	if err := workspace.MountWorkspaceHandlers(server, database); err != nil {
		log.Fatal(err)
	}

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
}
