// Package arkade talks to a live Arkade stack: arkd, the emulator, the explorer
// and a wallet built on the go-sdk.
//
// It is a separate Go module for the same reason `contract` is one. `contract`
// is pure computation over inputs and is what the client verifier is pinned to;
// everything that needs a network connection to arkd or the emulator lives here
// instead, and both the service and the integration tests depend on it.
//
// Nothing here decides a payout. The formula lives in `contract` and nowhere
// else.
package arkade

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"time"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdkclient "github.com/arkade-os/go-sdk/client/grpc"
	"github.com/btcsuite/btcd/btcec/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Config points at the stack. arkade-regtest publishes these on localhost;
// override any of them for a stack that does not.
//
// ExplorerURL has to include /api. Port 3000 serves the mempool web UI at the
// root and the Esplora REST API underneath, so pointing at the root gets HTML
// and the SDK fails with "invalid character '<'".
type Config struct {
	ArkdURL     string
	EmulatorURL string
	ExplorerURL string
	Network     arklib.Network
}

// DefaultConfig reads the HEDGE_* environment variables, falling back to what
// arkade-regtest exposes.
func DefaultConfig() Config {
	return Config{
		ArkdURL:     env("HEDGE_ARKD_URL", "localhost:7070"),
		EmulatorURL: env("HEDGE_EMULATOR_URL", "localhost:7073"),
		ExplorerURL: env("HEDGE_EXPLORER_URL", "http://localhost:3000/api"),
		Network:     arklib.BitcoinRegTest,
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// Stack is what the live services told us about themselves. Nothing in it is a
// constant we chose: a contract is built from what the operator actually runs.
type Stack struct {
	ArkdSigner     *btcec.PublicKey
	EmulatorSigner *btcec.PublicKey
	ExitDelay      arklib.RelativeLocktime
	Dust           uint64

	// CheckpointTapscript is decoded here so no caller has to hold the hex.
	CheckpointTapscript []byte

	// Where a forfeited VTXO goes, and the key the batch output is swept with.
	// Both are needed to join a batch swap, which is how a contract is renewed.
	ForfeitAddress string
	ForfeitPubKey  *btcec.PublicKey

	Emulator emulatorclient.TransportClient

	config Config
	conn   *grpc.ClientConn
}

// Connect reads both services' GetInfo. It is the readiness signal that matters:
// arkd's port opens well before it can answer.
func Connect(ctx context.Context, cfg Config) (*Stack, error) {
	arkd, err := arksdkclient.NewClient(cfg.ArkdURL)
	if err != nil {
		return nil, fmt.Errorf("arkd client: %w", err)
	}

	info, err := arkd.GetInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("arkd GetInfo: %w", err)
	}

	s := &Stack{
		ExitDelay:      Locktime(info.UnilateralExitDelay),
		Dust:           info.Dust,
		ForfeitAddress: info.ForfeitAddress,
		config:         cfg,
	}

	if s.ArkdSigner, err = ParseKey(info.SignerPubKey); err != nil {
		return nil, fmt.Errorf("arkd signer key: %w", err)
	}
	if s.ForfeitPubKey, err = ParseKey(info.ForfeitPubKey); err != nil {
		return nil, fmt.Errorf("arkd forfeit key: %w", err)
	}
	if info.CheckpointTapscript != "" {
		if s.CheckpointTapscript, err = hex.DecodeString(info.CheckpointTapscript); err != nil {
			return nil, fmt.Errorf("decoding the checkpoint tapscript: %w", err)
		}
	}

	s.conn, err = grpc.NewClient(cfg.EmulatorURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("emulator connection: %w", err)
	}
	s.Emulator = emulatorclient.NewGRPCClient(s.conn)

	emulatorInfo, err := s.Emulator.GetInfo(ctx)
	if err != nil {
		s.conn.Close()
		return nil, fmt.Errorf("emulator GetInfo: %w", err)
	}
	if s.EmulatorSigner, err = ParseKey(emulatorInfo.SignerPublicKey); err != nil {
		s.conn.Close()
		return nil, fmt.Errorf("emulator signer key: %w", err)
	}

	return s, nil
}

func (s *Stack) Close() error {
	if s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func (s *Stack) Config() Config { return s.config }

// AllowsBlockTimelocks reads the operator's policy off its own exit delay. A
// production operator configures seconds; the regtest stacks configure blocks so
// timelocks fire on mining instead of on the wall clock.
func (s *Stack) AllowsBlockTimelocks() bool {
	return s.ExitDelay.Type == arklib.LocktimeTypeBlock
}

// WaitFor blocks until every service accepts a TCP connection, or the context
// expires. Starting the stack and using it are separate steps, and arkd is not
// ready the moment its port opens on the way up.
func WaitFor(ctx context.Context, cfg Config) error {
	for _, target := range []struct{ name, addr string }{
		{"arkd", cfg.ArkdURL},
		{"emulator", cfg.EmulatorURL},
	} {
		if err := waitForPort(ctx, target.name, target.addr); err != nil {
			return err
		}
	}
	return nil
}

func waitForPort(ctx context.Context, name, addr string) error {
	var last error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s at %s never came up: %w (last dial: %v)",
				name, addr, ctx.Err(), last)
		default:
		}

		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		last = err
		time.Sleep(time.Second)
	}
}

// Locktime interprets arkd's exit delay the way arkd itself does: a value at or
// above 512 is seconds, anything below is blocks.
func Locktime(value int64) arklib.RelativeLocktime {
	if value >= 512 {
		return arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: uint32(value)}
	}
	return arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: uint32(value)}
}

func ParseKey(hexKey string) (*btcec.PublicKey, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, err
	}
	return btcec.ParsePubKey(raw)
}
