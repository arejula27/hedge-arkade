// Package boot wires the service together.
//
// Composition normally belongs in the one binary that does it, and it did until
// three things needed the same wiring: the API, the seeder that fills a fresh
// demo with people who have money, and the test that drives the whole flow
// against a live stack. Three copies of it would drift, and the one that
// drifted would be the test.
package boot

import (
	"context"
	"encoding/hex"
	"fmt"

	arkadeclient "github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/arkade/regtest"
	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/arkadeadapter"
	"github.com/arejula27/hedge/service/internal/config"
	"github.com/arejula27/hedge/service/internal/events"
	"github.com/arejula27/hedge/service/internal/oracleclient"
	"github.com/arejula27/hedge/service/internal/postgres"
	"github.com/arejula27/hedge/service/internal/signer"
	"github.com/arejula27/hedge/service/internal/wallets"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg"
)

// Service is everything a binary might need, already connected.
type Service struct {
	DB     *postgres.DB
	Stack  *arkadeclient.Stack
	App    *app.App
	Broker *events.Broker

	// Params is the chain a contract address is rendered on.
	Params *chaincfg.Params

	closers []func()
}

func (s *Service) Close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
}

// Options lets a caller replace a piece. A test points Feed at an oracle it
// started itself; everything else takes the real thing.
type Options struct {
	Feed app.Feed
}

// Wire opens the database, reads the live stack, and builds the use cases.
//
// Connecting reads both services' GetInfo, which is the readiness signal that
// matters: arkd's port opens well before it can answer. Nothing about a
// contract is a constant we chose.
func Wire(ctx context.Context, cfg config.Config, o Options) (*Service, error) {
	s := &Service{Params: Params(arkadeclient.DefaultConfig().Network)}

	seed, err := hex.DecodeString(cfg.ServiceSeed)
	if err != nil {
		return nil, fmt.Errorf("the service key: %w", err)
	}
	serviceKey, _ := btcec.PrivKeyFromBytes(seed)

	if s.DB, err = postgres.Open(ctx, cfg.Database); err != nil {
		return nil, err
	}
	s.closers = append(s.closers, func() { s.DB.Close() })

	arkadeConfig := arkadeclient.DefaultConfig()
	if s.Stack, err = arkadeclient.Connect(ctx, arkadeConfig); err != nil {
		s.Close()
		return nil, err
	}
	s.closers = append(s.closers, func() { s.Stack.Close() })
	s.Params = Params(arkadeConfig.Network)

	users := postgres.NewUserRepo(s.DB)
	registry := wallets.New(s.Stack, users)

	// The faucet only exists on regtest. Leaving the script unset is how a real
	// deployment says so.
	var chain arkadeclient.Chain
	if cfg.RegtestScript != "" {
		chain = regtest.New(cfg.RegtestScript)
	}

	feed := o.Feed
	if feed == nil {
		feed = oracleclient.New(cfg.Oracle)
	}

	s.Broker = events.NewBroker()

	s.App = app.New(app.Options{
		Users: users,
		// Announcing wraps the store rather than each use case: Advance is the
		// only way a contract moves, so it is the only place this belongs.
		Contracts: events.Announce(postgres.NewContractRepo(s.DB), s.Broker),
		Exits:     postgres.NewExitRepo(s.DB),
		Signer:    signer.New(registry),
		Stack:     arkadeadapter.New(s.Stack, registry, chain),
		Feed:      feed,

		ServiceKey: serviceKey.PubKey(),
	})

	return s, nil
}

// Params is the chain a contract address is rendered on.
func Params(network arklib.Network) *chaincfg.Params {
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
