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

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/boot"
	"github.com/arejula27/hedge/service/internal/config"
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

	service, err := boot.Wire(ctx, cfg, boot.Options{})
	if err != nil {
		return err
	}
	defer service.Close()

	log.Printf("operator %x, emulator %x, exit delay %+v, dust %d",
		service.Stack.ArkdSigner.SerializeCompressed(),
		service.Stack.EmulatorSigner.SerializeCompressed(),
		service.Stack.ExitDelay, service.Stack.Dust)

	// Funding and settling outlive the requests that start them, so a worker
	// carries them the rest of the way — from the row alone, restart or no
	// restart.
	go app.NewWorker(service.App, app.WorkerOptions{Log: log.Printf}).Run(ctx)

	srv := server.New(cfg, server.Options{
		App:    service.App,
		DB:     service.DB,
		Broker: service.Broker,
		Params: service.Params,
	})

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
