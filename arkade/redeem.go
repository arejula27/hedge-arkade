package arkade

import (
	"context"
	"fmt"
	"strings"

	arkclient "github.com/arkade-os/go-sdk/client"
	"github.com/btcsuite/btcd/btcutil/psbt"
)

// Decode reads a base64 packet back.
func Decode(b64 string) (*psbt.Packet, error) {
	packet, err := psbt.NewFromRawBytes(strings.NewReader(b64), true)
	if err != nil {
		return nil, fmt.Errorf("decoding a packet: %w", err)
	}
	return packet, nil
}

func DecodeAll(b64s []string) ([]*psbt.Packet, error) {
	packets := make([]*psbt.Packet, 0, len(b64s))
	for _, b64 := range b64s {
		packet, err := Decode(b64)
		if err != nil {
			return nil, err
		}
		packets = append(packets, packet)
	}
	return packets, nil
}

// SubmitSigned hands a transaction to the operator whose packets already carry
// the signatures that matter.
//
// A mutual redemption is signed before either party's wallet sees it: leaf 2
// carries the contract's own keys, which no wallet holds. What the wallet is
// here is transport — hence `transport`, applied on the way in — and what
// `finalizers` are is those same contract keys again, because the operator
// hands the checkpoints back with its own signature on them and re-verifies
// every key in the revealed leaf when it takes them (`service.go:1236`).
func SubmitSigned(
	ctx context.Context, arkd arkclient.TransportClient,
	arkTx string, checkpoints []string, transport, finalizers []Signer,
) (string, error) {
	signedArkTx, signedCheckpoints := arkTx, checkpoints

	var err error
	for _, sign := range transport {
		if signedArkTx, err = sign(ctx, signedArkTx); err != nil {
			return "", fmt.Errorf("signing the transaction: %w", err)
		}
	}
	if signedCheckpoints, err = signEach(ctx, signedCheckpoints, transport); err != nil {
		return "", err
	}

	txid, _, returned, err := arkd.SubmitTx(ctx, signedArkTx, signedCheckpoints)
	if err != nil {
		return "", err
	}

	final, err := signEach(ctx, returned, finalizers)
	if err != nil {
		return "", err
	}

	if err := arkd.FinalizeTx(ctx, txid, final); err != nil {
		return "", err
	}
	return txid, nil
}
