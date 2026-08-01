package arkade

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

// tagging is a Signer that records that it ran. Signing for real needs a wallet
// and a live explorer; what this package is responsible for is that every
// signer sees the transaction and every checkpoint, in order.
func tagging(tag string) Signer {
	return func(_ context.Context, packetB64 string) (string, error) {
		return packetB64 + "|" + tag, nil
	}
}

func TestSignEveryoneWalksTheTransactionRoundThePartiesInTurn(t *testing.T) {
	arkTx := packet(t, &wire.TxOut{Value: 10, PkScript: script(0x51)})
	checkpoints := []*psbt.Packet{
		packet(t, &wire.TxOut{Value: 4, PkScript: script(0x52)}),
		packet(t, &wire.TxOut{Value: 6, PkScript: script(0x53)}),
	}

	signedArkTx, signedCheckpoints, err := SignEveryone(
		t.Context(), arkTx, checkpoints, []Signer{tagging("short"), tagging("long")})
	if err != nil {
		t.Fatalf("SignEveryone: %v", err)
	}

	if !strings.HasSuffix(signedArkTx, "|short|long") {
		t.Errorf("the transaction did not go round both parties in order: %q", tail(signedArkTx))
	}
	if len(signedCheckpoints) != 2 {
		t.Fatalf("got %d checkpoints, want 2", len(signedCheckpoints))
	}
	for i, checkpoint := range signedCheckpoints {
		if !strings.HasSuffix(checkpoint, "|short|long") {
			t.Errorf("checkpoint %d did not go round both parties in order: %q", i, tail(checkpoint))
		}
	}
}

func TestSignEveryoneWithNoSignersChangesNothing(t *testing.T) {
	arkTx := packet(t, &wire.TxOut{Value: 10, PkScript: script(0x51)})
	want, err := arkTx.B64Encode()
	if err != nil {
		t.Fatalf("B64Encode: %v", err)
	}

	got, checkpoints, err := SignEveryone(t.Context(), arkTx, nil, nil)
	if err != nil {
		t.Fatalf("SignEveryone: %v", err)
	}
	if got != want {
		t.Error("the transaction changed with no signers")
	}
	if len(checkpoints) != 0 {
		t.Errorf("got %d checkpoints, want none", len(checkpoints))
	}
}

func TestSignEveryoneReportsWhichSideRefused(t *testing.T) {
	refusing := func(_ context.Context, _ string) (string, error) {
		return "", errors.New("the wallet is locked")
	}

	arkTx := packet(t, &wire.TxOut{Value: 10, PkScript: script(0x51)})
	checkpoints := []*psbt.Packet{packet(t, &wire.TxOut{Value: 10, PkScript: script(0x52)})}

	for _, tc := range []struct {
		name    string
		signers []Signer
		want    string
	}{
		{"on the transaction", []Signer{refusing}, "signing the transaction"},
		{"on a checkpoint", []Signer{checkpointRefuser()}, "signing a checkpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := SignEveryone(t.Context(), arkTx, checkpoints, tc.signers)
			if err == nil {
				t.Fatal("SignEveryone accepted a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
		})
	}
}

// checkpointRefuser signs the transaction and then refuses the checkpoints, so
// the two failure points can be told apart.
func checkpointRefuser() Signer {
	first := true
	return func(_ context.Context, packetB64 string) (string, error) {
		if first {
			first = false
			return packetB64, nil
		}
		return "", errors.New("the wallet is locked")
	}
}

func tail(s string) string {
	if len(s) <= 24 {
		return s
	}
	return "…" + s[len(s)-24:]
}
