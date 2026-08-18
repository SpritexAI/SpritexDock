package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/SpritexAI/SpritexDock/internal/api"
	"github.com/SpritexAI/SpritexDock/internal/auth"
	"github.com/SpritexAI/SpritexDock/internal/config"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/deployment"
	"github.com/SpritexAI/SpritexDock/internal/docker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration failed", "error", err)
		os.Exit(1)
	}

	state, err := db.Open(cfg.DataDir)
	if err != nil {
		slog.Error("database initialization failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := state.Close(); err != nil {
			slog.Error("database close failed", "error", err)
		}
	}()

	seedCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := auth.SeedOwner(seedCtx, state, os.Getenv("SPRITEXDOCK_ADMIN_USER"), os.Getenv("SPRITEXDOCK_ADMIN_PASS")); err != nil {
		slog.Error("owner account initialization failed", "error", err)
		os.Exit(1)
	}
	if _, err := deployment.RecoverInterrupted(seedCtx, state); err != nil {
		slog.Error("deployment recovery failed", "error", err)
		os.Exit(1)
	}

	dockerClient, err := docker.NewClient(cfg.DockerEndpoint)
	if err != nil {
		slog.Error("docker client initialization failed", "error", err)
		os.Exit(1)
	}
	defer dockerClient.Close()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := docker.Ping(pingCtx, dockerClient); err != nil {
		pingCancel()
		slog.Error("docker engine unavailable", "error", err)
		os.Exit(1)
	}
	pingCancel()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	api.RegisterRoutes(app, state, cfg)

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- app.Listen(cfg.Listen)
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(shutdown)

	select {
	case err := <-serverErrors:
		if err != nil {
			slog.Error("server stopped", "error", err)
			os.Exit(1)
		}
	case sig := <-shutdown:
		slog.Info("shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.ShutdownWithContext(ctx); err != nil {
			slog.Error("graceful shutdown failed", "error", err)
			os.Exit(1)
		}
	}
}
