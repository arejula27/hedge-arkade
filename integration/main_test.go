//go:build integration

package integration

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdkclient "github.com/arkade-os/go-sdk/client/grpc"
	"github.com/btcsuite/btcd/btcec/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// stack holds what the live services told us about themselves. Nothing here is
// a constant we chose: the point of these tests is that the contract is built
// from what the operator actually runs.
type liveStack struct {
	arkdSigner          *btcec.PublicKey
	emulatorSigner      *btcec.PublicKey
	exitDelay           arklib.RelativeLocktime
	dust                uint64
	checkpointTapscript string

	// Where a forfeited VTXO goes, and the key the batch output is swept with.
	// Both are needed to join a batch swap, which is how a contract is renewed.
	forfeitAddress string
	forfeitPubKey  *btcec.PublicKey

	emulator emulatorclient.TransportClient
	conn     *grpc.ClientConn
}

var stack liveStack

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := WaitForStack(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "integration stack unavailable: %v\n", err)
		fmt.Fprintln(os.Stderr, "start it with `just regtest-up`")
		os.Exit(1)
	}

	if err := stack.connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "failed to read the stack's configuration: %v\n", err)
		os.Exit(1)
	}
	defer stack.conn.Close()

	os.Exit(m.Run())
}

func (s *liveStack) connect(ctx context.Context) error {
	arkd, err := arksdkclient.NewClient(ArkdURL)
	if err != nil {
		return fmt.Errorf("arkd client: %w", err)
	}

	info, err := arkd.GetInfo(ctx)
	if err != nil {
		return fmt.Errorf("arkd GetInfo: %w", err)
	}

	s.arkdSigner, err = parseKey(info.SignerPubKey)
	if err != nil {
		return fmt.Errorf("arkd signer key: %w", err)
	}
	s.exitDelay = locktime(info.UnilateralExitDelay)
	s.dust = info.Dust
	s.checkpointTapscript = info.CheckpointTapscript
	s.forfeitAddress = info.ForfeitAddress

	s.forfeitPubKey, err = parseKey(info.ForfeitPubKey)
	if err != nil {
		return fmt.Errorf("arkd forfeit key: %w", err)
	}

	s.conn, err = grpc.NewClient(EmulatorURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("emulator connection: %w", err)
	}
	s.emulator = emulatorclient.NewGRPCClient(s.conn)

	emulatorInfo, err := s.emulator.GetInfo(ctx)
	if err != nil {
		return fmt.Errorf("emulator GetInfo: %w", err)
	}
	s.emulatorSigner, err = parseKey(emulatorInfo.SignerPublicKey)
	if err != nil {
		return fmt.Errorf("emulator signer key: %w", err)
	}

	return nil
}

// allowsBlockTimelocks reads the operator's policy off its own exit delay. A
// production operator configures seconds; the regtest stacks configure blocks so
// timelocks fire on mining instead of on the wall clock.
func (s *liveStack) allowsBlockTimelocks() bool {
	return s.exitDelay.Type == arklib.LocktimeTypeBlock
}

// locktime interprets arkd's exit delay the way arkd itself does: a value at or
// above 512 is seconds, anything below is blocks.
func locktime(value int64) arklib.RelativeLocktime {
	if value >= 512 {
		return arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: uint32(value)}
	}
	return arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: uint32(value)}
}

func parseKey(hexKey string) (*btcec.PublicKey, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, err
	}
	return btcec.ParsePubKey(raw)
}

// A sanity check on the fixture, not on our code: if the stack reports keys we
// cannot parse or an exit delay of zero, every test below is meaningless.
func TestStackIsUsable(t *testing.T) {
	if stack.arkdSigner == nil || stack.emulatorSigner == nil {
		t.Fatal("the stack did not report both signer keys")
	}
	if stack.exitDelay.Value == 0 {
		t.Fatal("arkd reports a zero unilateral exit delay")
	}
	t.Logf("arkd signer %x", stack.arkdSigner.SerializeCompressed())
	t.Logf("emulator signer %x", stack.emulatorSigner.SerializeCompressed())
	t.Logf("exit delay %+v, dust %d", stack.exitDelay, stack.dust)
}
