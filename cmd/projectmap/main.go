package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"projectmap/internal/access"
	"projectmap/internal/config"
	"projectmap/internal/database"
	"projectmap/internal/httpapi"
	"projectmap/internal/memory"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: projectmap serve|migrate|bootstrap|credentials")
	}
	command := os.Args[1]
	if command != "serve" && command != "migrate" && command != "bootstrap" && command != "credentials" {
		return errors.New("unknown command: use serve, migrate, bootstrap, or credentials")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	switch command {
	case "migrate", "bootstrap", "credentials":
		opctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if command == "migrate" {
			if err := store.Migrate(opctx); err != nil {
				return err
			}
			fmt.Println("Database migrations applied.")
			return nil
		}
		if err := store.Ready(opctx); err != nil {
			return err
		}
		identity, err := store.Bootstrap(opctx)
		if err != nil {
			return err
		}
		if command == "credentials" {
			if err := access.Provision(cfg.CredentialsFile, []string{identity.DashboardClientID, identity.AgentClientID}); err != nil {
				return err
			}
			fmt.Println("Local credentials created in the configured private credential file. Restart the backend to load them.")
			return nil
		}
		return json.NewEncoder(os.Stdout).Encode(identity)
	}
	credentials, err := access.LoadCredentials(cfg.CredentialsFile)
	if err != nil {
		return err
	}
	manager := access.New(store, credentials)
	projects := &memory.Service{Backend: database.NewMemoryStore(store, "http://"+cfg.Addr)}
	srv := &http.Server{Addr: cfg.Addr, Handler: httpapi.Handler(store, cfg.Addr, httpapi.Options{Access: manager, Memory: projects}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	result := make(chan error, 1)
	go func() { result <- srv.ListenAndServe() }()
	slog.Info("local backend starting", "address", cfg.Addr)
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP listener failed; check configured address and port")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return errors.New("HTTP shutdown timed out")
		}
		return nil
	}
}
