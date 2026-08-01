package app

import (
	"context"
	"fmt"
	"time"

	"github.com/arejula27/hedge/contract"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
)

// Proposal is what someone fills in to open a position.
//
// Prices are in cents per BTC, the same unit the oracle publishes and the
// covenant compares against. The hedge value is in cents too; scaling it to
// what Terms wants is domain.NominalUnits's job and nobody else's.
type Proposal struct {
	Creator uuid.UUID
	Side    domain.Side

	HedgeValueCents      int64
	PayoutSats           int64
	LowLiquidationCents  int64
	HighLiquidationCents int64

	MaturityIn             time.Duration
	EnableMutualRedemption bool
}

func (p Proposal) validate() error {
	switch {
	case !p.Side.Valid():
		return invalid("side must be short or long, got %q", p.Side)
	case p.HedgeValueCents <= 0:
		return invalid("the hedge value must be positive")
	case p.PayoutSats < 2*contract.Dust:
		return invalid("the payout must be at least %d sats, so both sides clear dust", 2*contract.Dust)
	case p.LowLiquidationCents <= 0:
		return invalid("the low liquidation price must be positive")
	case p.HighLiquidationCents <= p.LowLiquidationCents:
		return invalid("the high liquidation price must be above the low one")
	case p.MaturityIn <= 0:
		return invalid("maturity must be in the future")
	}
	return nil
}

// Propose opens a position and leaves it looking for a counterparty.
//
// Only the creator's payout script is known here. The other side's — and so the
// address, which is a function of both — arrives with whoever accepts.
func (a *App) Propose(ctx context.Context, p Proposal) (*domain.Contract, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}

	oraclePubKey, err := a.feed.PubKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the oracle's key: %w", err)
	}
	price, err := a.feed.Latest(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the price: %w", err)
	}

	// A position that is already past a boundary liquidates the instant it is
	// funded, which is a way to lose money to a typo rather than to the market.
	if price.Price <= p.LowLiquidationCents || price.Price >= p.HighLiquidationCents {
		return nil, notYet(
			"the price is %d, already outside %d–%d: the contract would liquidate on funding",
			price.Price, p.LowLiquidationCents, p.HighLiquidationCents)
	}

	creatorKey, err := a.signer.PublicKey(ctx, p.Creator)
	if err != nil {
		return nil, err
	}
	creatorScript, err := a.stack.VtxoPkScript(ctx, p.Creator)
	if err != nil {
		return nil, fmt.Errorf("the creator's payout script: %w", err)
	}

	now := a.now()
	stack := a.stack.Stack()

	c := &domain.Contract{
		ID:      uuid.New(),
		State:   domain.Proposed,
		Creator: p.Side,
		Terms: contract.Terms{
			NominalUnitsXSatsPerBtc:              domain.NominalUnits(p.HedgeValueCents),
			SatsForNominalUnitsAtHighLiquidation: 0,
			PayoutSats:                           p.PayoutSats,
			LowLiquidationPrice:                  p.LowLiquidationCents,
			HighLiquidationPrice:                 p.HighLiquidationCents,
			OraclePubKey:                         oraclePubKey,
			StartTimestamp:                       now.Unix(),
			MaturityTimestamp:                    now.Add(p.MaturityIn).Unix(),
		},
		ArkdSigner:             stack.ArkdSigner.SerializeCompressed(),
		EmulatorSigner:         stack.EmulatorSigner.SerializeCompressed(),
		ExitDelay:              stack.ExitDelay,
		EnableMutualRedemption: p.EnableMutualRedemption,
	}

	// The stakes are not known until the other side is, but the row has to add
	// up from the moment it is written, so the creator holds all of it for now.
	c.ShortStake, c.LongStake = p.PayoutSats, 0

	if p.Side == domain.Short {
		c.ShortUser, c.ShortKey = &p.Creator, creatorKey.SerializeCompressed()
		c.Terms.ShortLockScript = creatorScript
	} else {
		c.LongUser, c.LongKey = &p.Creator, creatorKey.SerializeCompressed()
		c.Terms.LongLockScript = creatorScript
	}

	if err := a.contracts.Create(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Accept takes the other side.
//
// This is where the contract becomes real: both payout scripts are known, so
// the address is, and so is what each side has to put in — which is exactly
// what the covenant would pay them back at today's price, so a contract that
// settled the moment it was funded would move nothing.
func (a *App) Accept(ctx context.Context, id, acceptor uuid.UUID) (*domain.Contract, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.State != domain.Proposed {
		return nil, fmt.Errorf("%w: this contract is %s", domain.ErrTransition, c.State)
	}
	if _, already := c.SideOf(acceptor); already {
		return nil, fmt.Errorf("%w: you cannot take both sides of a contract", ErrNotAllowed)
	}

	price, err := a.feed.Latest(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the price: %w", err)
	}

	key, err := a.signer.PublicKey(ctx, acceptor)
	if err != nil {
		return nil, err
	}
	script, err := a.stack.VtxoPkScript(ctx, acceptor)
	if err != nil {
		return nil, fmt.Errorf("the acceptor's payout script: %w", err)
	}

	side := c.Creator.Other()
	if side == domain.Short {
		c.ShortUser, c.ShortKey = &acceptor, key.SerializeCompressed()
		c.Terms.ShortLockScript = script
	} else {
		c.LongUser, c.LongKey = &acceptor, key.SerializeCompressed()
		c.Terms.LongLockScript = script
	}

	// Straight from the covenant's own formula. The service never decides a
	// split; it only ever executes this.
	short, long, err := c.Split(price.Price)
	if err != nil {
		return nil, fmt.Errorf("the opening split: %w", err)
	}
	c.ShortStake, c.LongStake = short, long

	covenant, err := c.Covenant()
	if err != nil {
		return nil, err
	}

	// The operator's own acceptance rules, run here rather than discovered when
	// the funding transaction is refused.
	stack := a.stack.Stack()
	if err := covenant.Validate(stack.ExitDelay, stack.AllowsBlockTimelocks); err != nil {
		return nil, invalid("the operator would refuse this contract: %v", err)
	}

	if c.PkScript, err = covenant.PkScript(); err != nil {
		return nil, err
	}

	detail := fmt.Sprintf("%s takes the %s at %d; stakes %d/%d",
		acceptor, side, price.Price, short, long)
	if err := a.contracts.Advance(ctx, c, domain.Accepted, detail); err != nil {
		return nil, err
	}
	return c, nil
}

// Cancel withdraws a contract before any money has moved.
func (a *App) Cancel(ctx context.Context, id, who uuid.UUID) (*domain.Contract, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, party := c.SideOf(who); !party {
		return nil, fmt.Errorf("%w: you are not a party to this contract", ErrNotAllowed)
	}
	if err := a.contracts.Advance(ctx, c, domain.Cancelled, "cancelled by "+who.String()); err != nil {
		return nil, err
	}
	return c, nil
}

func (a *App) Contract(ctx context.Context, id uuid.UUID) (*domain.Contract, error) {
	return a.contracts.Get(ctx, id)
}

func (a *App) Contracts(ctx context.Context, f domain.ContractFilter) ([]*domain.Contract, error) {
	return a.contracts.List(ctx, f)
}

func (a *App) History(ctx context.Context, id uuid.UUID) ([]domain.Event, error) {
	return a.contracts.Events(ctx, id)
}
