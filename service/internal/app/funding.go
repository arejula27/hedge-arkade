package app

import (
	"bytes"
	"context"
	"fmt"

	"github.com/arejula27/hedge/contract"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
)

// Fund puts both parties' collateral into the contract.
//
// Everything that can be checked is checked before the contract moves, so
// anything that fails after it has moved is transient and the worker can retry
// it. The move itself is what stops two people funding the same contract twice.
func (a *App) Fund(ctx context.Context, id, who uuid.UUID) (*domain.Contract, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, party := c.SideOf(who); !party {
		return nil, fmt.Errorf("%w: you are not a party to this contract", ErrNotAllowed)
	}
	if c.State != domain.Accepted {
		return nil, fmt.Errorf("%w: this contract is %s", domain.ErrTransition, c.State)
	}

	if err := a.affordable(ctx, c); err != nil {
		return nil, err
	}

	if err := a.contracts.Advance(ctx, c, domain.Funding, "funding started by "+who.String()); err != nil {
		return nil, err
	}

	// The rest takes tens of seconds against a live stack, so the caller gets
	// the contract back now and the worker finishes it.
	return c, nil
}

// affordable checks both sides can pay before anything moves.
//
// Each party's VTXO has to cover their stake and still leave change above the
// operator's dust, because the funding transaction pays that change back to
// them and an output below dust is one arkd refuses.
func (a *App) affordable(ctx context.Context, c *domain.Contract) error {
	for _, side := range []struct {
		name  domain.Side
		user  *uuid.UUID
		stake int64
	}{
		{domain.Short, c.ShortUser, c.ShortStake},
		{domain.Long, c.LongUser, c.LongStake},
	} {
		if side.user == nil {
			return fmt.Errorf("the %s side is empty", side.name)
		}

		balance, recoverable, err := a.stack.Balance(ctx, *side.user)
		if err != nil {
			return fmt.Errorf("the %s's balance: %w", side.name, err)
		}

		needed := side.stake + int64(a.stack.Stack().Dust) + 1
		if balance < needed {
			if recoverable > 0 {
				return notYet(
					"the %s has %d spendable sats and needs %d, with %d more waiting on a batch to recover",
					side.name, balance, needed, recoverable)
			}
			return notYet(
				"the %s has %d sats and needs %d: their stake of %d plus change above dust",
				side.name, balance, needed, side.stake)
		}
	}
	return nil
}

// finishFunding is the slow half: one Arkade transaction with an input from
// each party, then the exit both of them sign before the contract is live.
//
// It runs in the worker, and it runs again after a restart, so it has to be
// safe to repeat. Submitting the funding transaction twice is: the second
// attempt spends VTXOs the first one already spent, and the operator refuses.
func (a *App) finishFunding(ctx context.Context, c *domain.Contract) error {
	if c.Funding == nil {
		outpoint, err := a.stack.Fund(ctx, c)
		if err != nil {
			return fmt.Errorf("funding %s: %w", c.ID, err)
		}
		c.Funding = &outpoint

		// Written before anything else can fail. The collateral is on the
		// operator's books from here, and a retry starting from a row that
		// never heard about it would spend both parties' VTXOs twice.
		if err := a.contracts.Save(ctx, c); err != nil {
			return fmt.Errorf("recording the funding of %s: %w", c.ID, err)
		}
	}

	if err := a.presignExit(ctx, c); err != nil {
		return fmt.Errorf("pre-signing the exit for %s: %w", c.ID, err)
	}

	return a.contracts.Advance(ctx, c, domain.Active,
		fmt.Sprintf("funded at %s, exit pre-signed", c.Funding))
}

// presignExit builds the transaction that takes the contract out of Arkade and
// has both parties sign it, before either of them needs it.
//
// contract.PreSignExit does this in one call and wants both private keys, which
// is exactly what a service that never custodies a key cannot have. It
// decomposes: ExitTx is a pure function of the contract and the outpoint, so
// each wallet derives the same bytes independently and neither has to trust
// this copy of them, and the two signatures are separate calls that could
// arrive minutes apart.
//
// What that loses is the check PreSignExit does for free — that the key signing
// is the contract's — so each signature is verified here instead. A wrong one
// stored now would look complete and fail only when the operator is already
// gone.
func (a *App) presignExit(ctx context.Context, c *domain.Contract) error {
	if _, err := a.exits.Get(ctx, c.ID); err == nil {
		return nil
	}

	covenant, err := c.Covenant()
	if err != nil {
		return err
	}

	sweep, err := contract.NewSweep(covenant.Keys.Short, covenant.Keys.Long, a.serviceKey)
	if err != nil {
		return fmt.Errorf("building the sweep: %w", err)
	}

	outpoint, err := outpointOf(*c.Funding)
	if err != nil {
		return err
	}

	tx, err := covenant.ExitTx(outpoint, c.Terms.PayoutSats, a.exitFeeSats, sweep.PkScript)
	if err != nil {
		return fmt.Errorf("building the exit: %w", err)
	}

	var raw bytes.Buffer
	if err := tx.Serialize(&raw); err != nil {
		return fmt.Errorf("serialising the exit: %w", err)
	}

	exit := domain.Exit{
		ContractID: c.ID,
		RawTx:      raw.Bytes(),
		Amount:     c.Terms.PayoutSats,
		Sweep: domain.Sweep{
			PkScript:     sweep.PkScript,
			Leaf:         sweep.Leaf,
			ControlBlock: sweep.ControlBlock,
		},
	}

	pkg := domain.ExitPackage{Exit: exit}
	for _, side := range []struct {
		user *uuid.UUID
		key  *btcec.PublicKey
		into *[]byte
	}{
		{c.ShortUser, covenant.Keys.Short, &pkg.ShortSig},
		{c.LongUser, covenant.Keys.Long, &pkg.LongSig},
	} {
		sig, err := a.signer.SignExit(ctx, *side.user, c, &exit)
		if err != nil {
			return err
		}
		if err := covenant.VerifyExitSignature(side.key, sig, tx, exit.Amount); err != nil {
			return fmt.Errorf("the signature from %s does not cover this exit: %w", side.user, err)
		}
		*side.into = sig
	}

	if !pkg.Complete() {
		return fmt.Errorf("the exit package is not signed by both parties")
	}
	return a.exits.Put(ctx, pkg)
}

// ExitPackage is the pre-signed exit, so a party can see it exists and hold
// their own copy of it.
func (a *App) ExitPackage(ctx context.Context, id uuid.UUID) (domain.ExitPackage, error) {
	return a.exits.Get(ctx, id)
}

func outpointOf(o domain.Outpoint) (wire.OutPoint, error) {
	hash, err := chainHash(o.Txid)
	if err != nil {
		return wire.OutPoint{}, fmt.Errorf("the funding txid %q: %w", o.Txid, err)
	}
	return wire.OutPoint{Hash: *hash, Index: o.Vout}, nil
}
