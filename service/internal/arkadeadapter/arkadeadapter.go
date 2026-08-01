// Package arkadeadapter puts the live Arkade stack behind the use cases' port.
//
// Everything here is a thin call into the `arkade` module. What it adds is the
// mapping from a user id to a wallet, which is the demo's own concern and not
// the protocol's.
package arkadeadapter

import (
	"context"
	"fmt"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/arejula27/hedge/service/internal/wallets"
	"github.com/google/uuid"
)

type Adapter struct {
	stack   *arkade.Stack
	wallets *wallets.Registry
	chain   arkade.Chain
}

func New(stack *arkade.Stack, w *wallets.Registry, chain arkade.Chain) *Adapter {
	return &Adapter{stack: stack, wallets: w, chain: chain}
}

func (a *Adapter) Stack() app.Stack {
	return app.Stack{
		ArkdSigner:           a.stack.ArkdSigner,
		EmulatorSigner:       a.stack.EmulatorSigner,
		ExitDelay:            a.stack.ExitDelay,
		Dust:                 a.stack.Dust,
		AllowsBlockTimelocks: a.stack.AllowsBlockTimelocks(),
	}
}

func (a *Adapter) Addresses(ctx context.Context, user uuid.UUID) (string, string, error) {
	w, err := a.wallets.Wallet(ctx, user)
	if err != nil {
		return "", "", err
	}
	return w.Addresses(ctx)
}

func (a *Adapter) Balance(ctx context.Context, user uuid.UUID) (int64, int64, error) {
	w, err := a.wallets.Wallet(ctx, user)
	if err != nil {
		return 0, 0, err
	}
	return w.Balance(ctx)
}

func (a *Adapter) Recover(ctx context.Context, user uuid.UUID) error {
	w, err := a.wallets.Wallet(ctx, user)
	if err != nil {
		return err
	}
	_, err = w.Settle(ctx)
	return err
}

// VtxoPkScript is where a user is paid inside Arkade.
//
// A contract's payout has to name this and not a bare P2TR of the user's key:
// both are valid outputs, but only this one is a script an Arkade wallet
// indexes. Getting it wrong settles the contract correctly into sats nobody can
// spend.
func (a *Adapter) VtxoPkScript(ctx context.Context, user uuid.UUID) ([]byte, error) {
	w, err := a.wallets.Wallet(ctx, user)
	if err != nil {
		return nil, err
	}
	return w.VtxoPkScript()
}

func (a *Adapter) TopUp(ctx context.Context, user uuid.UUID, sats int64) error {
	if a.chain == nil {
		return fmt.Errorf("there is no faucet on this network")
	}
	w, err := a.wallets.Wallet(ctx, user)
	if err != nil {
		return err
	}
	return w.Fund(ctx, a.chain, sats)
}

// Fund puts both parties' collateral into the contract in one Arkade
// transaction, each side signing only its own inputs.
func (a *Adapter) Fund(ctx context.Context, c *domain.Contract) (domain.Outpoint, error) {
	short, long, err := a.parties(ctx, c)
	if err != nil {
		return domain.Outpoint{}, err
	}

	covenant, err := c.Covenant()
	if err != nil {
		return domain.Outpoint{}, err
	}

	outpoint, err := arkade.FundBilaterally(ctx, a.stack, covenant,
		arkade.Stake{Wallet: short, Sats: c.ShortStake},
		arkade.Stake{Wallet: long, Sats: c.LongStake},
	)
	if err != nil {
		return domain.Outpoint{}, err
	}
	return domain.Outpoint{Txid: outpoint.Hash.String(), Vout: outpoint.Index}, nil
}

// Settle spends the contract through the settlement leaf.
//
// The wallet here is transport, not authority: the leaf carries no party key,
// so the signature it adds is to nothing, and the emulator is what actually
// authorises the spend after running the script.
func (a *Adapter) Settle(
	ctx context.Context, c *domain.Contract, short, long int64, pair app.Pair,
) error {
	shortWallet, _, err := a.parties(ctx, c)
	if err != nil {
		return err
	}

	covenant, err := c.Covenant()
	if err != nil {
		return err
	}

	outpoint, err := outpointOf(c)
	if err != nil {
		return err
	}

	arkTx, checkpoints, err := arkade.BuildSettlement(a.stack, covenant, outpoint, short, long,
		arkade.SettlementWitness(pair.Settlement, pair.Previous))
	if err != nil {
		return err
	}

	return arkade.SubmitToEmulator(ctx, a.stack, arkTx, checkpoints,
		[]arkade.Signer{shortWallet.Signer()})
}

// BuildRedemption is the early close through leaf 2, unsigned.
//
// It goes straight to the operator when it is submitted: the leaf carries no
// tweaked emulator key, so the emulator has nothing to execute.
func (a *Adapter) BuildRedemption(
	ctx context.Context, c *domain.Contract, short, long int64,
) (string, []string, error) {
	covenant, err := c.Covenant()
	if err != nil {
		return "", nil, err
	}

	outpoint, err := outpointOf(c)
	if err != nil {
		return "", nil, err
	}

	proposal, err := covenant.ProposeRedemption(outpoint, short, long, a.stack.CheckpointTapscript)
	if err != nil {
		return "", nil, err
	}

	arkTx, err := proposal.ArkTx.B64Encode()
	if err != nil {
		return "", nil, fmt.Errorf("encoding the early close: %w", err)
	}
	checkpoints, err := arkade.Encode(proposal.Checkpoints)
	if err != nil {
		return "", nil, err
	}
	return arkTx, checkpoints, nil
}

// SubmitRedemption hands the signed close to the operator.
//
// Nothing signs on the way in: leaf 2 is a 3-of-3 of the two parties and the
// operator, the parties' keys are already on the packets, and the operator adds
// its own. `sign` is those same party keys again, for the checkpoints it hands
// back — it re-verifies every key in the revealed leaf when it takes them
// (`service.go:1236`).
//
// A party's wallet must *not* sign here, even though it is the transport. In
// this demo the key a contract leaf carries is the same key the wallet holds,
// so a wallet signature would be a second signature from a key that has already
// signed — and a PSBT with the same key twice is one the operator refuses to
// parse at all. When wallets move to the users' own devices the two keys come
// apart and this stops being a hazard, but the wallet still has no business
// signing a leaf it is not named in.
func (a *Adapter) SubmitRedemption(
	ctx context.Context, c *domain.Contract, r *domain.Redemption, sign []arkade.Signer,
) error {
	short, _, err := a.parties(ctx, c)
	if err != nil {
		return err
	}

	txid, err := arkade.SubmitSigned(ctx, short.Arkd(), r.ArkTx, r.Checkpoints, nil, sign)
	if err != nil {
		return err
	}
	return arkade.WaitForVtxo(ctx, short, txid)
}

func (a *Adapter) parties(ctx context.Context, c *domain.Contract) (*arkade.Wallet, *arkade.Wallet, error) {
	if c.ShortUser == nil || c.LongUser == nil {
		return nil, nil, fmt.Errorf("contract %s does not have both sides yet", c.ID)
	}
	short, err := a.wallets.Wallet(ctx, *c.ShortUser)
	if err != nil {
		return nil, nil, err
	}
	long, err := a.wallets.Wallet(ctx, *c.LongUser)
	if err != nil {
		return nil, nil, err
	}
	return short, long, nil
}
