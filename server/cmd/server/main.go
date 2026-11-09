package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/groups"
	httpapi "github.com/linknestx/linknestx/server/internal/http"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// run owns the database for the lifetime of the HTTP server. Returning startup
// errors lets the deferred close run even when configuration or serving fails.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	conn, err := db.Open(cfg.SQLitePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer conn.Close()
	if err := db.ApplyMigrations(context.Background(), conn); err != nil {
		return err
	}
	service := auth.NewAuthService(auth.NewRepository(conn), cfg.JWTSigningKey, cfg.JWTRefreshKey)
	if cfg.BootstrapAdminUsername != "" {
		_, err := service.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: cfg.BootstrapAdminUsername, Password: cfg.BootstrapAdminPassword})
		if err != nil && !errors.Is(err, auth.ErrBootstrapComplete) {
			return fmt.Errorf("bootstrap administrator: %w", err)
		}
	}

	groupService := groups.NewGroupService(conn, service)
	chatService := chat.NewChatService(conn, service, groupService)
	hub := chat.NewHub(chatService)
	defer hub.Close()

	server := &stdhttp.Server{
		Addr:              cfg.BindAddress,
		Handler:           httpapi.NewRouterWithServices(cfg, service, groupService, chatService, hub),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-shutdownContext.Done()
		hub.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("shutdown HTTP server", "error", err)
			_ = server.Close()
		}
	}()
	slog.Info("server listening", "address", cfg.BindAddress)
	err = server.ListenAndServe()
	stop()
	<-shutdownDone
	if err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
		return err
	}
	return nil
}
