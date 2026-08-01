// Package signer signs on a party's behalf with a key this process holds.
//
// This is the demo's whole custody story, and the one package that has no place
// in the service that ships: the coordinator holds the oracle's key and its own
// third of the 2-of-3, and never a party's.
//
// Nothing above it changes when that day comes. Every signature already goes
// through app.Signer, which takes a user and returns bytes — so an external
// wallet implements the same three methods and no use case notices.
package signer

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/contract"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/arejula27/hedge/service/internal/wallets"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
)

type Server struct {
	wallets *wallets.Registry

	// serviceKey is the service's own third of the 2-of-3 a unilateral exit
	// sweeps into. It is one of only two keys the coordinator ever holds.
	serviceKey *btcec.PrivateKey
}

func New(w *wallets.Registry, serviceKey *btcec.PrivateKey) *Server {
	return &Server{wallets: w, serviceKey: serviceKey}
}

func (s *Server) PublicKey(ctx context.Context, user uuid.UUID) (*btcec.PublicKey, error) {
	key, err := s.wallets.Key(ctx, user)
	if err != nil {
		return nil, err
	}
	return key.PubKey(), nil
}

// SignPacket signs the inputs whose leaf carries the user's key and leaves the
// rest alone, so a party cannot sign for their counterparty.
func (s *Server) SignPacket(ctx context.Context, user uuid.UUID, packetB64 string) (string, error) {
	w, err := s.wallets.Wallet(ctx, user)
	if err != nil {
		return "", err
	}
	return w.SignPacket(ctx, packetB64)
}

// SignLeaf signs the leaves of a packet that name the user's own contract key.
//
// This is a different key from the one SignPacket uses, doing a different job.
// Leaf 2 carries the contract's own keys and no wallet holds them, so the
// wallet cannot sign it and this has to reach for the raw key instead — which
// is exactly the reach that goes away when the key lives on the user's device.
func (s *Server) SignLeaf(ctx context.Context, user uuid.UUID, packetB64 string) (string, error) {
	key, err := s.wallets.Key(ctx, user)
	if err != nil {
		return "", err
	}

	packet, err := arkade.Decode(packetB64)
	if err != nil {
		return "", err
	}

	if err := contract.SignTapscript(key, packet); err != nil {
		return "", fmt.Errorf("signing the revealed leaves: %w", err)
	}

	signed, err := packet.B64Encode()
	if err != nil {
		return "", fmt.Errorf("encoding the signed packet: %w", err)
	}
	return signed, nil
}

// SignExit signs one side of the unilateral exit.
//
// It signs the transaction it is given rather than one it builds, because both
// parties have to sign the same bytes — and it is a pure function of the
// contract and the outpoint, so each of them can derive it and check that the
// bytes they are being asked to sign are the ones they expected.
func (s *Server) SignExit(
	ctx context.Context, user uuid.UUID, c *domain.Contract, exit *domain.Exit,
) ([]byte, error) {
	key, err := s.wallets.Key(ctx, user)
	if err != nil {
		return nil, err
	}

	covenant, err := c.Covenant()
	if err != nil {
		return nil, err
	}

	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(exit.RawTx)); err != nil {
		return nil, fmt.Errorf("reading the exit transaction: %w", err)
	}

	return covenant.SignExit(key, &tx, exit.Amount)
}

// SignSweep signs an arbitration spending the 2-of-3 the exit landed in.
//
// The service holds the third key and signs its own half elsewhere. Two of
// three is the whole point: the service decides the number and cannot move the
// money, and a party moves the money and does not decide the number.
func (s *Server) SignSweep(
	ctx context.Context, user uuid.UUID, c *domain.Contract, a *domain.Arbitration,
) ([]byte, error) {
	key, err := s.wallets.Key(ctx, user)
	if err != nil {
		return nil, err
	}

	covenant, sweep, err := s.sweepOf(c)
	if err != nil {
		return nil, err
	}

	tx, err := parseTx(a.RawTx)
	if err != nil {
		return nil, err
	}

	return covenant.SignArbitration(key, &contract.Arbitration{Tx: tx}, sweep, a.Available)
}

// SignSweepAsService is the service putting its own key to an arbitration.
//
// This one is legitimately here forever: the coordinator holds the oracle's key
// and its own third of the 2-of-3, and never a party's.
func (s *Server) SignSweepAsService(
	_ context.Context, c *domain.Contract, a *domain.Arbitration,
) ([]byte, error) {
	covenant, sweep, err := s.sweepOf(c)
	if err != nil {
		return nil, err
	}

	tx, err := parseTx(a.RawTx)
	if err != nil {
		return nil, err
	}

	return covenant.SignArbitration(s.serviceKey, &contract.Arbitration{Tx: tx}, sweep, a.Available)
}

func (s *Server) sweepOf(c *domain.Contract) (contract.Contract, *contract.Sweep, error) {
	covenant, err := c.Covenant()
	if err != nil {
		return contract.Contract{}, nil, err
	}

	sweep, err := contract.NewSweep(covenant.Keys.Short, covenant.Keys.Long, s.serviceKey.PubKey())
	if err != nil {
		return contract.Contract{}, nil, fmt.Errorf("rebuilding the sweep: %w", err)
	}
	return covenant, sweep, nil
}

func parseTx(rawHex string) (*wire.MsgTx, error) {
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return nil, fmt.Errorf("the transaction is not hex: %w", err)
	}

	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("reading the transaction: %w", err)
	}
	return &tx, nil
}
