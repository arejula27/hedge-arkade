// Command oracle publishes signed prices on a fixed cadence.
//
// It is a separate binary because it is a separate thing: it knows about no
// contract, holds no funds, and could be run by someone else entirely. The
// service reaches it over HTTP like any other client.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/arejula27/hedge/service/internal/config"
	"github.com/arejula27/hedge/service/internal/oracle"
	"github.com/arejula27/hedge/service/internal/oracleserver"
	"github.com/arejula27/hedge/service/internal/postgres"
	"github.com/btcsuite/btcd/btcec/v2"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.LoadOracle()
	if err != nil {
		return err
	}

	seed, err := hex.DecodeString(cfg.Seed)
	if err != nil {
		return err
	}
	key, _ := btcec.PrivKeyFromBytes(seed)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := postgres.Migrate(ctx, db); err != nil {
		return err
	}

	store := postgres.NewOracleStore(db)
	publisher := oracle.NewPublisher(store, key, cfg.StartPrice)

	log.Printf("oracle %x publishing every %s", publisher.PublicKey(), cfg.Interval)

	go publisher.Run(ctx, cfg.Interval, func(err error) {
		log.Printf("publishing: %v", err)
	})

	srv := oracleserver.New(publisher, store, oracleserver.Options{
		Port:        cfg.Port,
		Interval:    cfg.Interval,
		AllowManual: cfg.AllowManual,
	})

	errs := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
