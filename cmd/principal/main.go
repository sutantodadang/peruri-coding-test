package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"peruri/internal/apps/principal"
	"peruri/internal/config"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("demo Principal stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	if err := config.LoadEnv(".env"); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	callbackURL, cbToken, token := os.Getenv("CALLBACK_URL"), os.Getenv("CALLBACK_TOKEN"), os.Getenv("PRINCIPAL_TOKEN")
	if callbackURL == "" || cbToken == "" || token == "" {
		return errors.New("CALLBACK_URL, CALLBACK_TOKEN and PRINCIPAL_TOKEN required")
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	h, join := principal.NewSimulator(ctx, callbackURL, cbToken, token, client)
	address := os.Getenv("HTTP_ADDR_PRINCIPAL")
	if address == "" {
		address = "127.0.0.1:9000"
	}
	s := &http.Server{Addr: address, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { slog.Info("demo Principal listening", "address", address); done <- s.ListenAndServe() }()
	var e error
	select {
	case e = <-done:
		stop()
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := s.Shutdown(shutdown)
	if shutdownErr != nil {
		_ = s.Close()
	}
	stop()
	join()
	if e != nil && !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	return shutdownErr
}
