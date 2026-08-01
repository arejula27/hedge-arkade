package arkade

import (
	"context"
	"fmt"
	"time"

	arkclient "github.com/arkade-os/go-sdk/client"
	"github.com/btcsuite/btcd/btcutil/psbt"
)

// Signer takes one base64 packet and returns it signed. It is the shape a
// wallet already has, and the shape an external wallet will have: nothing here
// ever sees a private key.
type Signer func(ctx context.Context, packetB64 string) (string, error)

// SignEveryone walks the transaction round the signers in turn. Each one signs
// the inputs whose leaf carries its key and leaves the rest alone, so the order
// does not matter and a party cannot sign for its counterparty.
func SignEveryone(
	ctx context.Context, arkTx *psbt.Packet, checkpoints []*psbt.Packet, signers []Signer,
) (string, []string, error) {
	signedArkTx, err := arkTx.B64Encode()
	if err != nil {
		return "", nil, fmt.Errorf("encoding the transaction: %w", err)
	}

	signedCheckpoints, err := Encode(checkpoints)
	if err != nil {
		return "", nil, err
	}

	for _, sign := range signers {
		if signedArkTx, err = sign(ctx, signedArkTx); err != nil {
			return "", nil, fmt.Errorf("signing the transaction: %w", err)
		}
	}

	if signedCheckpoints, err = signEach(ctx, signedCheckpoints, signers); err != nil {
		return "", nil, err
	}

	return signedArkTx, signedCheckpoints, nil
}

func signEach(ctx context.Context, packets []string, signers []Signer) ([]string, error) {
	signed := make([]string, len(packets))
	copy(signed, packets)

	for _, sign := range signers {
		for i, packet := range signed {
			s, err := sign(ctx, packet)
			if err != nil {
				return nil, fmt.Errorf("signing a checkpoint: %w", err)
			}
			signed[i] = s
		}
	}
	return signed, nil
}

// SubmitToArkd is the path for a transaction with no covenant on its input:
// straight to the operator, sign the checkpoints it returns, finalise.
func SubmitToArkd(
	ctx context.Context, arkd arkclient.TransportClient,
	arkTx *psbt.Packet, checkpoints []*psbt.Packet, signers []Signer,
) (string, error) {
	signedArkTx, signedCheckpoints, err := SignEveryone(ctx, arkTx, checkpoints, signers)
	if err != nil {
		return "", err
	}

	txid, _, returned, err := arkd.SubmitTx(ctx, signedArkTx, signedCheckpoints)
	if err != nil {
		return "", err
	}

	final, err := signEach(ctx, returned, signers)
	if err != nil {
		return "", err
	}

	if err := arkd.FinalizeTx(ctx, txid, final); err != nil {
		return "", err
	}

	return txid, nil
}

// SubmitToEmulator is the path for a covenant input. The emulator runs the
// script, signs, and forwards to arkd itself when it holds the last signature
// (`internal/application/tx.go:146`), so one call covers both services.
func SubmitToEmulator(
	ctx context.Context, s *Stack,
	arkTx *psbt.Packet, checkpoints []*psbt.Packet, signers []Signer,
) error {
	signedArkTx, signedCheckpoints, err := SignEveryone(ctx, arkTx, checkpoints, signers)
	if err != nil {
		return err
	}

	_, _, err = s.Emulator.SubmitTx(ctx, signedArkTx, signedCheckpoints)
	return err
}

// WaitForVtxo blocks until arkd reports the transaction's outputs as spendable.
// Nothing can spend them before it has registered them.
func WaitForVtxo(ctx context.Context, w *Wallet, txid string) error {
	waiting, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return Poll(waiting, 2*time.Second, "the new VTXOs to be registered", func() error {
		spendable, err := w.ListVtxos(ctx)
		if err != nil {
			return err
		}
		for _, v := range spendable {
			if v.Txid == txid {
				return nil
			}
		}
		return fmt.Errorf("tx %s not among %d spendable VTXOs", txid, len(spendable))
	})
}
