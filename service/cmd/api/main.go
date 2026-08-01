// Command api runs the hedge service.
//
// This file is the composition root: the only place that knows how the layers
// are wired together. Everything it constructs is handed its collaborators, so
// nothing below reaches for a global.
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

	arkadeclient "github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/arkade/regtest"
	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/arkadeadapter"
	"github.com/arejula27/hedge/service/internal/config"
	"github.com/arejula27/hedge/service/internal/events"
	"github.com/arejula27/hedge/service/internal/oracleclient"
	"github.com/arejula27/hedge/service/internal/postgres"
	"github.com/arejula27/hedge/service/internal/server"
	"github.com/arejula27/hedge/service/internal/signer"
	"github.com/arejula27/hedge/service/internal/wallets"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg"
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

	serviceSeed, err := hex.DecodeString(cfg.ServiceSeed)
	if err != nil {
		return err
	}
	serviceKey, _ := btcec.PrivKeyFromBytes(serviceSeed)

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

	// Connecting reads both services' GetInfo, which is the readiness signal
	// that matters: arkd's port opens well before it can answer. Nothing about
	// a contract is a constant we chose.
	arkadeConfig := arkadeclient.DefaultConfig()
	stack, err := arkadeclient.Connect(ctx, arkadeConfig)
	if err != nil {
		return err
	}
	defer stack.Close()

	log.Printf("operator %x, emulator %x, exit delay %+v, dust %d",
		stack.ArkdSigner.SerializeCompressed(),
		stack.EmulatorSigner.SerializeCompressed(),
		stack.ExitDelay, stack.Dust)

	users := postgres.NewUserRepo(db)
	registry := wallets.New(stack, users)

	// The faucet only exists on regtest. Leaving the script unset is how a real
	// deployment says so.
	var chain arkadeclient.Chain
	if cfg.RegtestScript != "" {
		chain = regtest.New(cfg.RegtestScript)
	}

	broker := events.NewBroker()

	service := app.New(app.Options{
		Users: users,
		// Announcing wraps the store rather than each use case: Advance is the
		// only way a contract moves, so it is the only place this belongs.
		Contracts: events.Announce(postgres.NewContractRepo(db), broker),
		Exits:     postgres.NewExitRepo(db),
		Signer:    signer.New(registry),
		Stack:     arkadeadapter.New(stack, registry, chain),
		Feed:      oracleclient.New(cfg.Oracle),

		ServiceKey: serviceKey.PubKey(),
	})

	// Funding and settling outlive the requests that start them, so a worker
	// carries them the rest of the way — from the row alone, restart or no
	// restart.
	go app.NewWorker(service, app.WorkerOptions{Log: log.Printf}).Run(ctx)

	srv := server.New(cfg, server.Options{
		App:    service,
		DB:     db,
		Broker: broker,
		Params: params(arkadeConfig.Network),
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

// params is the chain a contract address is rendered on.
func params(network arklib.Network) *chaincfg.Params {
	switch network.Name {
	case arklib.Bitcoin.Name:
		return &chaincfg.MainNetParams
	case arklib.BitcoinTestNet.Name:
		return &chaincfg.TestNet3Params
	case arklib.BitcoinSigNet.Name:
		return &chaincfg.SigNetParams
	default:
		return &chaincfg.RegressionNetParams
	}
}
