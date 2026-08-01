package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/arejula27/hedge/contract"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
)

// Exit is a party giving up on the operator.
//
// Everything up to here has assumed the operator answers. This is the path that
// does not: the contract's whole chain of transactions goes onto Bitcoin, and
// then the exit both parties signed at funding — before either of them needed
// it — sweeps the money into a 2-of-3 the operator has no key to.
func (a *App) Exit(ctx context.Context, id, who uuid.UUID) (*domain.Contract, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, party := c.SideOf(who); !party {
		return nil, fmt.Errorf("%w: you are not a party to this contract", ErrNotAllowed)
	}
	if c.State != domain.Active {
		return nil, fmt.Errorf("%w: this contract is %s", domain.ErrTransition, c.State)
	}

	pkg, err := a.exits.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !pkg.Complete() {
		return nil, notYet("the exit is not signed by both parties yet")
	}

	if err := a.contracts.Advance(ctx, c, domain.Exiting, "leaving Arkade, at "+who.String()+"'s request"); err != nil {
		return nil, err
	}
	return c, nil
}

// finishExiting unrolls and broadcasts. It is minutes of wall clock — a chain
// to put on the chain one transaction per block, then a relative timelock to
// wait out — so it is the worker's, and it has to be able to start again.
func (a *App) finishExiting(ctx context.Context, c *domain.Contract) error {
	pkg, err := a.exits.Get(ctx, c.ID)
	if err != nil {
		return err
	}

	if !pkg.OnChain() {
		outpoint, sats, err := a.stack.LeaveArkade(ctx, c, pkg)
		if err != nil {
			return fmt.Errorf("exiting %s: %w", c.ID, err)
		}

		// Written before anything else can fail. The money is on the chain from
		// here, and everything after it has to be able to start from the row.
		pkg.Swept, pkg.SweptSats = &outpoint, sats
		if err := a.exits.Put(ctx, pkg); err != nil {
			return fmt.Errorf("recording where the exit landed: %w", err)
		}
	}

	return a.contracts.Advance(ctx, c, domain.Exited,
		fmt.Sprintf("out of Arkade: %s holds %d sats", pkg.Swept, pkg.SweptSats))
}

// Arbitrate is the service working out the split once the covenant is gone.
//
// It has no discretion in it. Without a valid oracle signature Arbitrate
// refuses to produce a proposal at all, and the message travels with the result
// so the number can be checked before anyone signs and audited afterwards.
//
// It also cannot move the money: two of the three keys are needed and the
// service holds one.
func (a *App) Arbitrate(ctx context.Context, id uuid.UUID) (*domain.Arbitration, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.State != domain.Exited {
		return nil, fmt.Errorf("%w: this contract is %s", domain.ErrTransition, c.State)
	}

	pkg, err := a.exits.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !pkg.OnChain() {
		return nil, notYet("the exit has not landed on the chain yet")
	}

	pair, err := a.feed.Pair(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the price: %w", err)
	}

	covenant, sweep, err := a.sweepOf(c)
	if err != nil {
		return nil, err
	}

	outpoint, err := outpointOf(*pkg.Swept)
	if err != nil {
		return nil, err
	}

	proposal, err := covenant.Arbitrate(sweep, outpoint, pkg.SweptSats, a.arbitrationFeeSats,
		pair.Settlement.Message, pair.Settlement.Signature)
	if err != nil {
		return nil, fmt.Errorf("arbitrating: %w", err)
	}

	raw, err := rawHex(proposal.Tx)
	if err != nil {
		return nil, err
	}

	arbitration := &domain.Arbitration{
		ID:         uuid.New(),
		ContractID: c.ID,
		ShortSats:  proposal.ShortSats,
		LongSats:   proposal.LongSats,
		Price:      proposal.Price,
		Message:    proposal.Message,
		Signature:  proposal.Signature,
		RawTx:      raw,
		Available:  pkg.SweptSats,
		Signatures: map[string]string{},
	}

	// The service signs its own half at once. It proposed the number; refusing
	// to stand behind it would be a strange thing to do, and one signature
	// short is one nobody can use.
	signature, err := a.signer.SignSweepAsService(ctx, c, arbitration)
	if err != nil {
		return nil, err
	}
	arbitration.Signatures[contract.XOnlyHex(a.serviceKey)] = hexOf(signature)

	if err := a.arbitrations.Put(ctx, arbitration); err != nil {
		return nil, err
	}
	if err := a.contracts.Advance(ctx, c, domain.Arbitrating,
		fmt.Sprintf("proposed %d/%d at %d",
			proposal.ShortSats, proposal.LongSats, proposal.Price)); err != nil {
		return nil, err
	}
	return arbitration, nil
}

// SignArbitration is a party checking the proposal and putting their key to it.
//
// The check is the whole point: the service decided the number, and a party who
// signs without recomputing it from the oracle's own bytes is trusting that it
// decided honestly.
func (a *App) SignArbitration(ctx context.Context, id, who uuid.UUID) (*domain.Arbitration, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, party := c.SideOf(who); !party {
		return nil, fmt.Errorf("%w: you are not a party to this contract", ErrNotAllowed)
	}
	if c.State != domain.Arbitrating {
		return nil, fmt.Errorf("%w: this contract is %s", domain.ErrTransition, c.State)
	}

	proposal, err := a.arbitrations.ForContract(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := a.verifyArbitration(c, proposal); err != nil {
		return nil, err
	}

	signature, err := a.signer.SignSweep(ctx, who, c, proposal)
	if err != nil {
		return nil, err
	}

	key, err := a.signer.PublicKey(ctx, who)
	if err != nil {
		return nil, err
	}
	proposal.Signatures[contract.XOnlyHex(key)] = hexOf(signature)

	if err := a.arbitrations.Put(ctx, proposal); err != nil {
		return nil, err
	}
	return proposal, nil
}

// verifyArbitration rebuilds the proposal from its own evidence and demands an
// exact match.
//
// This is what a party runs before signing, and it is why the message and
// signature travel with the proposal at all: without them, "the service says
// this is the right number" is the whole of the guarantee.
func (a *App) verifyArbitration(c *domain.Contract, proposal *domain.Arbitration) error {
	covenant, sweep, err := a.sweepOf(c)
	if err != nil {
		return err
	}

	tx, err := parseTx(proposal.RawTx)
	if err != nil {
		return err
	}

	rebuilt := &contract.Arbitration{
		Tx:        tx,
		ShortSats: proposal.ShortSats,
		LongSats:  proposal.LongSats,
		Price:     proposal.Price,
		Message:   proposal.Message,
		Signature: proposal.Signature,
	}
	if err := covenant.VerifyArbitration(rebuilt, sweep, proposal.Available, a.arbitrationFeeSats); err != nil {
		return fmt.Errorf("the proposal does not check out: %w", err)
	}

	// VerifyArbitration checks the transaction, which is what actually pays.
	// The numbers stored beside it are what a party reads on a screen, and a
	// proposal whose two halves disagree is one that shows one thing and pays
	// another.
	if len(tx.TxOut) != 2 {
		return invalid("the arbitration pays %d outputs, want 2", len(tx.TxOut))
	}
	if tx.TxOut[0].Value != proposal.ShortSats || tx.TxOut[1].Value != proposal.LongSats {
		return invalid("the proposal says %d/%d and its transaction pays %d/%d",
			proposal.ShortSats, proposal.LongSats, tx.TxOut[0].Value, tx.TxOut[1].Value)
	}
	return nil
}

// finishArbitrating broadcasts once there are two signatures. It is the
// worker's, and it is what pays both sides.
func (a *App) finishArbitrating(ctx context.Context, c *domain.Contract) error {
	proposal, err := a.arbitrations.ForContract(ctx, c.ID)
	if err != nil {
		return err
	}
	if !proposal.Signed() {
		// Waiting for a party, which is not a failure and not something to
		// report over and over.
		return nil
	}

	txid, err := a.stack.PayOut(ctx, c, proposal)
	if err != nil {
		return fmt.Errorf("paying out %s: %w", c.ID, err)
	}

	proposal.Txid = txid
	if err := a.arbitrations.Put(ctx, proposal); err != nil {
		return err
	}

	return a.contracts.Advance(ctx, c, domain.Arbitrated,
		fmt.Sprintf("paid on chain: %d to the short, %d to the long",
			proposal.ShortSats, proposal.LongSats))
}

// Arbitration is the open proposal, if there is one.
func (a *App) Arbitration(ctx context.Context, id uuid.UUID) (*domain.Arbitration, error) {
	return a.arbitrations.ForContract(ctx, id)
}

// sweepOf rebuilds the 2-of-3 the exit swept into.
//
// It is rebuilt rather than read back, because NewSweep is deterministic and a
// stored copy that had drifted from the keys would be a destination nobody
// could spend.
func (a *App) sweepOf(c *domain.Contract) (contract.Contract, *contract.Sweep, error) {
	covenant, err := c.Covenant()
	if err != nil {
		return contract.Contract{}, nil, err
	}

	sweep, err := contract.NewSweep(covenant.Keys.Short, covenant.Keys.Long, a.serviceKey)
	if err != nil {
		return contract.Contract{}, nil, fmt.Errorf("rebuilding the sweep: %w", err)
	}
	return covenant, sweep, nil
}

func rawHex(tx *wire.MsgTx) (string, error) {
	var raw bytes.Buffer
	if err := tx.Serialize(&raw); err != nil {
		return "", fmt.Errorf("serialising the transaction: %w", err)
	}
	return hexOf(raw.Bytes()), nil
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

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
