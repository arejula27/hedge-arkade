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
	"fmt"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/arejula27/hedge/service/internal/wallets"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
)

type Server struct {
	wallets *wallets.Registry
}

func New(w *wallets.Registry) *Server { return &Server{wallets: w} }

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
