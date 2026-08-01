package arkade

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/arkade-os/go-sdk/explorer"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
)

// RawHex serializes a transaction for broadcasting.
func RawHex(tx *wire.MsgTx) (string, error) {
	var raw bytes.Buffer
	if err := tx.Serialize(&raw); err != nil {
		return "", fmt.Errorf("serialising the transaction: %w", err)
	}
	return hex.EncodeToString(raw.Bytes()), nil
}

// TaprootAddress renders a P2TR scriptPubKey as an address on the given network.
//
// The explorer indexes by address, so anything that has to be found on chain
// has to be asked for by one.
func TaprootAddress(pkScript []byte, params *chaincfg.Params) (string, error) {
	// P2TR is OP_1 followed by a 32-byte push.
	if len(pkScript) != 34 {
		return "", fmt.Errorf("that is not a P2TR script: %d bytes", len(pkScript))
	}

	key, err := schnorr.ParsePubKey(pkScript[2:])
	if err != nil {
		return "", fmt.Errorf("parsing the output key: %w", err)
	}

	address, err := btcutil.NewAddressTaproot(schnorr.SerializePubKey(key), params)
	if err != nil {
		return "", fmt.Errorf("rendering the address: %w", err)
	}
	return address.EncodeAddress(), nil
}

// Broadcast puts a signed transaction on the chain, retrying until the network
// takes it or ctx expires.
//
// Retrying is the answer to a transaction whose parent is still in the mempool,
// and to a relative timelock that has not quite matured.
func Broadcast(ctx context.Context, w *Wallet, tx *wire.MsgTx) (string, error) {
	raw, err := RawHex(tx)
	if err != nil {
		return "", err
	}

	var txid string
	err = Poll(ctx, 2*time.Second, "the chain to accept the transaction", func() error {
		txid, err = w.Explorer().Broadcast(raw)
		return err
	})
	return txid, err
}

// WaitForOutput waits for an address to hold an output of exactly sats, and
// returns it.
func WaitForOutput(
	ctx context.Context, w *Wallet, address string, sats int64,
) (explorer.Utxo, error) {
	var found explorer.Utxo

	err := Poll(ctx, 2*time.Second, "the output to appear on chain", func() error {
		utxos, err := w.Explorer().GetUtxos(address)
		if err != nil {
			return err
		}
		for _, u := range utxos {
			if int64(u.Amount) == sats {
				found = u
				return nil
			}
		}
		return fmt.Errorf("no output of %d sats at %s yet", sats, address)
	})
	return found, err
}
