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
	"github.com/SpritexAI/SpritexDock/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration failed", "error", err)
		os.Exit(1)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	api.RegisterRoutes(app)

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
