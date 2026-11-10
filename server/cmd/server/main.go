package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	stdhttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/linknestx/linknestx/server/internal/audit"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/groups"
	httpapi "github.com/linknestx/linknestx/server/internal/http"
	"github.com/linknestx/linknestx/server/internal/p2p"
	"github.com/linknestx/linknestx/server/internal/retention"
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
	store, err := files.NewChunkStore(cfg.StorageRoot)
	if err != nil {
		return fmt.Errorf("open chunk storage: %w", err)
	}
	defer store.Close()
	fileService := files.NewFileService(conn, service, store, cfg.JWTSigningKey, chatService)
	peerService := p2p.NewService(conn, service, fileService, cfg)
	peerHub := p2p.NewHub(peerService)
	defer peerHub.Close()
	jobs := retention.Jobs{Files: fileService, Chat: chatService, Audit: audit.NewService(conn, service), Interval: cfg.CleanupInterval}

	server := &stdhttp.Server{
		Addr:              cfg.BindAddress,
		Handler:           httpapi.NewRouterWithPeers(cfg, service, groupService, chatService, hub, fileService, peerService, peerHub),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.BindAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	jobsDone := make(chan struct{})
	go func() { defer close(jobsDone); jobs.Run(shutdownContext) }()
	defer func() { stop(); <-jobsDone }()
	peersDone := make(chan struct{})
	go func() { defer close(peersDone); peerService.Run(shutdownContext) }()
	defer func() { stop(); <-peersDone }()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-shutdownContext.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hub.Close()
		peerHub.Close()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("shutdown HTTP server", "error", err)
			_ = server.Close()
		}
	}()
	slog.Info("server listening", "address", cfg.BindAddress)
	err = server.Serve(listener)
	stop()
	<-shutdownDone
	if err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
		return err
	}
	return nil
}
