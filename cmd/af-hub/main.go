package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
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

	server := apikit.NewServer(cfg, func() error {
		return database.Ping(context.Background())
	})

	// Mount all built-in handlers (OAuth, users, orgs, keys, PATs).
	if err := server.MountHandlers(database); err != nil {
		log.Fatal(err)
	}

	// Add your own routes on the same API group.
	api := server.APIGroup()
	api.GET("/hello", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"message": "hello from your custom server"})
	})

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
}
