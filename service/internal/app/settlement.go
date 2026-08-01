package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/google/uuid"
)

// Settle spends the contract through the covenant.
//
// Anyone can ask for it: the settlement leaf carries no party key, which is the
// point — a contract that has liquidated must not need the losing side to
// cooperate.
func (a *App) Settle(ctx context.Context, id uuid.UUID) (*domain.Contract, error) {
	c, err := a.contracts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.State != domain.Active {
		return nil, fmt.Errorf("%w: this contract is %s", domain.ErrTransition, c.State)
	}

	price, err := a.feed.Latest(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the price: %w", err)
	}

	// The covenant's own trigger, checked here so a refusal says why rather
	// than coming back as a script failure from the emulator.
	if !c.Settleable(price.Price, price.Timestamp) {
		return nil, notYet(
			"the price is %d, inside %d–%d, and maturity is %d: nothing to settle yet",
			price.Price, c.Terms.LowLiquidationPrice, c.Terms.HighLiquidationPrice,
			c.Terms.MaturityTimestamp)
	}

	if err := a.contracts.Advance(ctx, c, domain.Settling,
		fmt.Sprintf("settling at %d", price.Price)); err != nil {
		return nil, err
	}
	return c, nil
}

// finishSettling is the slow half, and the one the worker retries.
//
// The pair comes from the oracle rather than being assembled here: the covenant
// needs a message and its *immediate* predecessor, and only the oracle can
// promise two of them are adjacent.
func (a *App) finishSettling(ctx context.Context, c *domain.Contract) error {
	if c.Funding == nil {
		return fmt.Errorf("contract %s has no funding outpoint", c.ID)
	}

	pair, err := a.feed.Pair(ctx)
	if err != nil {
		return fmt.Errorf("reading the settlement pair: %w", err)
	}

	covenant, err := c.Covenant()
	if err != nil {
		return err
	}

	// The price is read back out of the signed bytes rather than taken from
	// the feed's own number, so the split is derived from the same evidence
	// the covenant will check.
	price, err := covenant.PriceFrom(pair.Settlement.Message, pair.Settlement.Signature)
	if err != nil {
		return fmt.Errorf("verifying the settlement price: %w", err)
	}

	short, long, err := c.Split(price)
	if err != nil {
		return fmt.Errorf("the settlement split: %w", err)
	}

	if err := a.stack.Settle(ctx, c, short, long, pair); err != nil {
		return fmt.Errorf("settling %s: %w", c.ID, err)
	}

	return a.contracts.Advance(ctx, c, domain.Settled,
		fmt.Sprintf("settled at %d: %d to the short, %d to the long", price, short, long))
}

// Projection is what a contract would pay right now. It is what the UI shows
// while a contract is alive, and it comes from the covenant's own formula.
type Projection struct {
	Price      int64
	ShortSats  int64
	LongSats   int64
	Liquidated bool
	Matured    bool
}

func (a *App) Project(ctx context.Context, c *domain.Contract) (Projection, error) {
	price, err := a.feed.Latest(ctx)
	if err != nil {
		return Projection{}, err
	}

	short, long, err := c.Split(price.Price)
	if err != nil {
		return Projection{}, err
	}

	return Projection{
		Price:      price.Price,
		ShortSats:  short,
		LongSats:   long,
		Liquidated: c.Liquidated(price.Price),
		Matured:    c.Matured(price.Timestamp),
	}, nil
}

// Price is the current publication, for the UI's ticker.
func (a *App) Price(ctx context.Context) (domain.Price, error) {
	return a.feed.Latest(ctx)
}

func (a *App) PriceHistory(ctx context.Context, limit int) ([]domain.Price, error) {
	return a.feed.History(ctx, limit)
}

// SetPrice moves the demo's price lever. The oracle refuses unless it was
// started with manual publication on.
func (a *App) SetPrice(ctx context.Context, price int64) error {
	if price <= 0 {
		return fmt.Errorf("a price must be positive, got %d", price)
	}
	return a.feed.SetPrice(ctx, price)
}

func chainHash(txid string) (*chainhash.Hash, error) {
	return chainhash.NewHashFromStr(strings.TrimSpace(txid))
}
