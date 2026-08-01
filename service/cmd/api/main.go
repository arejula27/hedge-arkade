// Command api runs the hedge service.
//
// This file is the composition root: the only place that knows how the layers
// are wired together. Everything it constructs is handed its collaborators, so
// nothing below reaches for a global.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/arejula27/hedge/service/internal/config"
	"github.com/arejula27/hedge/service/internal/postgres"
	"github.com/arejula27/hedge/service/internal/server"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := server.New(cfg, db)

	errs := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	log.Println("shutting down, press Ctrl+C again to force")
	stop()

	// The server gets five seconds to finish what it is already serving.
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return srv.Shutdown(shutdown)
}
