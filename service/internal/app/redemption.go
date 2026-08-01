package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
)

// Split is what one party proposes the other agree to. Leave it empty to close
// at the oracle's price, which is the case with something to check.
type Split struct {
	ShortSats int64
	LongSats  int64
}

// ProposeRedemption offers an early close through leaf 2.
//
// The split is whatever the two of them say it is — that is the point of the
// leaf, and the reason it needs both signatures. A split derived from the
// oracle carries the signed message with it, so the other party can check the
// numbers against the same bytes rather than against a promise.
func (a *App) ProposeRedemption(
	ctx context.Context, id, who uuid.UUID, split Split,
) (*domain.Redemption, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	side, party := c.SideOf(who)
	if !party {
		return nil, fmt.Errorf("%w: you are not a party to this contract", ErrNotAllowed)
	}
	if c.State != domain.Active {
		return nil, fmt.Errorf("%w: this contract is %s", domain.ErrTransition, c.State)
	}

	proposal := &domain.Redemption{
		ID:         uuid.New(),
		ContractID: c.ID,
		ProposedBy: who,
		ShortSats:  split.ShortSats,
		LongSats:   split.LongSats,
	}

	// An empty split means "at whatever the oracle says", which is the case
	// worth having evidence for.
	if split.ShortSats == 0 && split.LongSats == 0 {
		pair, err := a.feed.Pair(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the price: %w", err)
		}

		covenant, err := c.Covenant()
		if err != nil {
			return nil, err
		}

		// Read back out of the signed bytes rather than taken from the feed's
		// own number, so the split is derived from the evidence that travels
		// with it.
		price, err := covenant.PriceFrom(pair.Settlement.Message, pair.Settlement.Signature)
		if err != nil {
			return nil, fmt.Errorf("verifying the price: %w", err)
		}
		if proposal.ShortSats, proposal.LongSats, err = c.Split(price); err != nil {
			return nil, err
		}

		proposal.Price = price
		proposal.Message = pair.Settlement.Message
		proposal.Signature = pair.Settlement.Signature
	}

	if err := a.checkSplit(c, proposal); err != nil {
		return nil, err
	}

	if proposal.ArkTx, proposal.Checkpoints, err = a.stack.BuildRedemption(
		ctx, c, proposal.ShortSats, proposal.LongSats); err != nil {
		return nil, fmt.Errorf("building the early close: %w", err)
	}

	// The proposer signs their own half on the way out. Nobody proposes a
	// close they are not willing to sign.
	if err := a.signRedemption(ctx, who, side, proposal); err != nil {
		return nil, err
	}

	if err := a.redemptions.Put(ctx, proposal); err != nil {
		return nil, err
	}

	detail := fmt.Sprintf("%s proposes %d/%d", who, proposal.ShortSats, proposal.LongSats)
	if proposal.FromOracle() {
		detail += fmt.Sprintf(" at %d", proposal.Price)
	}
	if err := a.contracts.Advance(ctx, c, domain.RedemptionProposed, detail); err != nil {
		return nil, err
	}
	return proposal, nil
}

// checkSplit is what the covenant's own redemption builder checks, run here so
// a bad proposal is refused with a sentence rather than by the operator.
func (a *App) checkSplit(c *domain.Contract, r *domain.Redemption) error {
	if r.ShortSats+r.LongSats != c.Terms.PayoutSats {
		return invalid("the split %d/%d does not add up to the contract's %d",
			r.ShortSats, r.LongSats, c.Terms.PayoutSats)
	}
	dust := int64(a.stack.Stack().Dust)
	if r.ShortSats < dust || r.LongSats < dust {
		return invalid("both sides have to clear the operator's dust of %d, got %d/%d",
			dust, r.ShortSats, r.LongSats)
	}
	return nil
}

// SignRedemption is the other party agreeing.
//
// A party that signs without checking is trusting whoever proposed, which is
// the thing the design refuses to require — so the numbers are re-derived from
// the proposal's own evidence before the signature is added.
func (a *App) SignRedemption(ctx context.Context, id, who uuid.UUID) (*domain.Redemption, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	side, party := c.SideOf(who)
	if !party {
		return nil, fmt.Errorf("%w: you are not a party to this contract", ErrNotAllowed)
	}
	if c.State != domain.RedemptionProposed {
		return nil, fmt.Errorf("%w: this contract is %s", domain.ErrTransition, c.State)
	}

	proposal, err := a.redemptions.ForContract(ctx, id)
	if err != nil {
		return nil, err
	}
	if proposal.HasSigned(side) {
		return proposal, nil
	}

	if err := a.verifyRedemption(c, proposal); err != nil {
		return nil, err
	}
	if err := a.signRedemption(ctx, who, side, proposal); err != nil {
		return nil, err
	}
	if err := a.redemptions.Put(ctx, proposal); err != nil {
		return nil, err
	}

	if !proposal.Signed() {
		return proposal, nil
	}

	// Both signatures are in. Submitting is slow against a live operator, so
	// the contract moves and the worker finishes it.
	if err := a.contracts.Advance(ctx, c, domain.Redeeming,
		fmt.Sprintf("%s signs; closing at %d/%d", who, proposal.ShortSats, proposal.LongSats)); err != nil {
		return nil, err
	}
	return proposal, nil
}

// verifyRedemption re-derives the split from the proposal's own oracle message.
//
// This is the check a party must run before signing, and the reason the message
// travels with the proposal at all. Without it, "the service says this is the
// right number" is the whole of the guarantee.
func (a *App) verifyRedemption(c *domain.Contract, r *domain.Redemption) error {
	if err := a.checkSplit(c, r); err != nil {
		return err
	}
	if !r.FromOracle() {
		return nil
	}

	covenant, err := c.Covenant()
	if err != nil {
		return err
	}

	price, err := covenant.PriceFrom(r.Message, r.Signature)
	if err != nil {
		return fmt.Errorf("the proposal's evidence does not verify: %w", err)
	}
	if price != r.Price {
		return invalid("the proposal says %d and its message says %d", r.Price, price)
	}

	short, long, err := c.Split(price)
	if err != nil {
		return err
	}
	if short != r.ShortSats || long != r.LongSats {
		return invalid("at %d the formula gives %d/%d, and the proposal says %d/%d",
			price, short, long, r.ShortSats, r.LongSats)
	}
	return nil
}

func (a *App) signRedemption(
	ctx context.Context, who uuid.UUID, side domain.Side, r *domain.Redemption,
) error {
	signed, err := a.signer.SignLeaf(ctx, who, r.ArkTx)
	if err != nil {
		return fmt.Errorf("signing the early close: %w", err)
	}
	r.ArkTx = signed

	for i, checkpoint := range r.Checkpoints {
		if r.Checkpoints[i], err = a.signer.SignLeaf(ctx, who, checkpoint); err != nil {
			return fmt.Errorf("signing a checkpoint: %w", err)
		}
	}

	r.SignedBy(side)
	return nil
}

// RejectRedemption withdraws a proposal and leaves the contract as it was.
func (a *App) RejectRedemption(ctx context.Context, id, who uuid.UUID) (*domain.Contract, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, party := c.SideOf(who); !party {
		return nil, fmt.Errorf("%w: you are not a party to this contract", ErrNotAllowed)
	}

	if err := a.redemptions.Drop(ctx, id); err != nil {
		return nil, err
	}
	if err := a.contracts.Advance(ctx, c, domain.Active, "the early close was rejected by "+who.String()); err != nil {
		return nil, err
	}
	return c, nil
}

// Redemption is the open proposal, if there is one.
func (a *App) Redemption(ctx context.Context, id uuid.UUID) (*domain.Redemption, error) {
	return a.redemptions.ForContract(ctx, id)
}

// finishRedeeming submits the signed close. It is the worker's job, and the one
// the worker retries.
func (a *App) finishRedeeming(ctx context.Context, c *domain.Contract) error {
	proposal, err := a.redemptions.ForContract(ctx, c.ID)
	if err != nil {
		return err
	}
	if !proposal.Signed() {
		return fmt.Errorf("contract %s is redeeming with only one signature", c.ID)
	}

	// The operator re-verifies every key in the revealed leaf on the
	// checkpoints it hands back, and no wallet holds those keys — so the same
	// two parties have to sign that round too.
	sign := []arkade.Signer{
		a.leafSigner(*c.ShortUser),
		a.leafSigner(*c.LongUser),
	}

	if err := a.stack.SubmitRedemption(ctx, c, proposal, sign); err != nil {
		return fmt.Errorf("closing %s early: %w", c.ID, err)
	}

	if err := a.redemptions.Drop(ctx, c.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return a.contracts.Advance(ctx, c, domain.Redeemed,
		fmt.Sprintf("closed early: %d to the short, %d to the long",
			proposal.ShortSats, proposal.LongSats))
}

func (a *App) leafSigner(user uuid.UUID) arkade.Signer {
	return func(ctx context.Context, packetB64 string) (string, error) {
		return a.signer.SignLeaf(ctx, user, packetB64)
	}
}
