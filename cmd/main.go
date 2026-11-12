package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"peruri/db"
	"peruri/internal/config"
	"peruri/internal/container"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if e := run(); e != nil {
		slog.Error("server stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	if err := config.LoadEnv(".env"); err != nil {
		return err
	}
	migrate := flag.Bool("migrate", true, "apply versioned schema on startup")
	flag.Parse()
	databaseURL := os.Getenv("DATABASE_URL")
	config := container.Config{PrincipalURL: os.Getenv("PRINCIPAL_URL"), PrincipalToken: os.Getenv("PRINCIPAL_TOKEN"), APIToken: os.Getenv("API_TOKEN"), CallbackToken: os.Getenv("CALLBACK_TOKEN")}
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required; copy .env.example to .env or set DATABASE_URL in the process environment")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(root, 20*time.Second)
	defer cancel()
	pool, e := db.Open(startup, databaseURL)
	if e != nil {
		return errors.New("database connection failed; check DATABASE_URL and database availability")
	}
	defer pool.Close()
	if *migrate {
		if e = db.Migrate(startup, pool); e != nil {
			return fmt.Errorf("migrate schema: %w", e)
		}
	}
	app := http.NewServeMux()
	appContainer, e := container.SetupContainer(app, pool, config)
	if e != nil {
		return fmt.Errorf("setup container: %w", e)
	}
	defer appContainer.Close()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			ctx, cancel := context.WithTimeout(root, 5*time.Second)
			n, e := appContainer.DocProcService.RecoverStaleDocuments(ctx)
			cancel()
			if e != nil && root.Err() == nil {
				slog.Error("lease recovery failed", "error", e)
			}
			if n > 0 {
				slog.Info("leases recovered", "documents", n)
			}
			select {
			case <-root.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	server := &http.Server{Addr: address, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 65 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	stopped := make(chan error, 1)
	go func() { slog.Info("API listening", "address", address); stopped <- server.ListenAndServe() }()
	select {
	case e = <-stopped:
		stop()
	case <-root.Done():
	}
	ctx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	shutdownErr := server.Shutdown(ctx)
	if shutdownErr != nil {
		_ = server.Close()
	}
	stop()
	<-workerDone
	if e != nil && !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	return shutdownErr
}
