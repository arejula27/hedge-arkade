package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/arejula27/hedge/service/internal/domain"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/google/uuid"
)

// ErrConflict is a contract that moved on between being read and being
// written. It is a 409, not a 500: two workers racing for the same contract is
// expected, and one of them losing is the mechanism working.
var ErrConflict = errors.New("the contract is no longer in the state it was read in")

// Lost says whether an error means someone else got there first.
//
// There are two ways to lose the same race: the row moved between the read and
// the write (ErrConflict), or it had already moved before the read, so the
// transition was refused by the domain. Both are a 409 and neither is worth
// retrying without re-reading — and the caller should not have to tell them
// apart to know that.
func Lost(err error) bool {
	return errors.Is(err, ErrConflict) || errors.Is(err, domain.ErrTransition)
}

type ContractRepo struct {
	db *DB
}

func NewContractRepo(db *DB) *ContractRepo { return &ContractRepo{db: db} }

const contractColumns = `
	id, state, creator, short_user_id, long_user_id,
	nominal_units, leverage_sats, payout_sats, low_liquidation, high_liquidation,
	short_lock_script, long_lock_script, oracle_pubkey, start_ts, maturity_ts,
	short_key, long_key, arkd_signer, emulator_signer,
	exit_delay_value, exit_delay_blocks, enable_mutual_redemption,
	pk_script, short_stake, long_stake, funding_txid, funding_vout`

func (r *ContractRepo) Create(ctx context.Context, c *domain.Contract) error {
	_, err := r.db.pool.ExecContext(ctx,
		`INSERT INTO contracts (`+contractColumns+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
		         $16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`,
		writeArgs(c)...)
	if err != nil {
		return fmt.Errorf("writing the contract: %w", err)
	}
	return r.event(ctx, r.db.pool, c.ID, "", c.State, "created")
}

func (r *ContractRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Contract, error) {
	row := r.db.pool.QueryRowContext(ctx,
		`SELECT `+contractColumns+` FROM contracts WHERE id = $1`, id)

	c, err := scanContract(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// Filter is what the lists the UI asks for come down to: what is on offer, and
// what is mine.
type Filter struct {
	// State restricts to one state. Empty means any.
	State domain.State
	// User restricts to contracts that user is a party to. Nil means anyone's.
	User *uuid.UUID
	// Open restricts to contracts still looking for a counterparty.
	Open bool
}

func (r *ContractRepo) List(ctx context.Context, f Filter) ([]*domain.Contract, error) {
	var where []string
	var args []any

	if f.State != "" {
		args = append(args, f.State)
		where = append(where, fmt.Sprintf("state = $%d", len(args)))
	}
	if f.User != nil {
		args = append(args, *f.User)
		where = append(where, fmt.Sprintf(
			"(short_user_id = $%d OR long_user_id = $%d)", len(args), len(args)))
	}
	if f.Open {
		where = append(where, "state = 'proposed' AND (short_user_id IS NULL OR long_user_id IS NULL)")
	}

	query := `SELECT ` + contractColumns + ` FROM contracts`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC"

	return r.query(ctx, query, args...)
}

// InState is what the worker asks for: every contract stuck mid-step, so it can
// be picked up and finished after a restart.
func (r *ContractRepo) InState(ctx context.Context, states ...domain.State) ([]*domain.Contract, error) {
	names := make([]string, len(states))
	for i, s := range states {
		names[i] = string(s)
	}
	return r.query(ctx,
		`SELECT `+contractColumns+` FROM contracts WHERE state = ANY($1) ORDER BY updated_at`,
		names)
}

// Advance writes the contract and moves it to `to`, refusing if the row is no
// longer in the state it was read in.
//
// The compare-and-swap is what makes two workers picking up the same contract
// safe: they both read `funding`, both build a transaction, and only one of
// them gets to say it finished.
func (r *ContractRepo) Advance(
	ctx context.Context, c *domain.Contract, to domain.State, detail string,
) error {
	from := c.State
	if err := from.Transition(to); err != nil {
		return err
	}

	tx, err := r.db.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning the transition: %w", err)
	}
	defer tx.Rollback()

	c.State = to
	args := append(writeArgs(c), from)

	result, err := tx.ExecContext(ctx,
		`UPDATE contracts SET
			state = $2, creator = $3, short_user_id = $4, long_user_id = $5,
			nominal_units = $6, leverage_sats = $7, payout_sats = $8,
			low_liquidation = $9, high_liquidation = $10,
			short_lock_script = $11, long_lock_script = $12, oracle_pubkey = $13,
			start_ts = $14, maturity_ts = $15,
			short_key = $16, long_key = $17, arkd_signer = $18, emulator_signer = $19,
			exit_delay_value = $20, exit_delay_blocks = $21,
			enable_mutual_redemption = $22, pk_script = $23,
			short_stake = $24, long_stake = $25,
			funding_txid = $26, funding_vout = $27,
			updated_at = now()
		 WHERE id = $1 AND state = $28`, args...)
	if err != nil {
		c.State = from
		return fmt.Errorf("moving %s to %s: %w", c.ID, to, err)
	}

	changed, err := result.RowsAffected()
	if err != nil {
		c.State = from
		return err
	}
	if changed == 0 {
		c.State = from
		return ErrConflict
	}

	if err := r.event(ctx, tx, c.ID, from, to, detail); err != nil {
		c.State = from
		return err
	}
	if err := tx.Commit(); err != nil {
		c.State = from
		return fmt.Errorf("committing the transition: %w", err)
	}
	return nil
}

func (r *ContractRepo) Events(ctx context.Context, id uuid.UUID) ([]domain.Event, error) {
	rows, err := r.db.pool.QueryContext(ctx,
		`SELECT from_state, to_state, detail FROM contract_events
		 WHERE contract_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("reading the history: %w", err)
	}
	defer rows.Close()

	events := []domain.Event{}
	for rows.Next() {
		e := domain.Event{ContractID: id}
		if err := rows.Scan(&e.From, &e.To, &e.Detail); err != nil {
			return nil, fmt.Errorf("reading an event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// execer is whatever can run a statement: the pool, or a transaction when the
// event has to land with the transition it describes.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (r *ContractRepo) event(
	ctx context.Context, on execer, id uuid.UUID, from, to domain.State, detail string,
) error {
	_, err := on.ExecContext(ctx,
		`INSERT INTO contract_events (contract_id, from_state, to_state, detail)
		 VALUES ($1, $2, $3, $4)`, id, from, to, detail)
	if err != nil {
		return fmt.Errorf("recording %s -> %s: %w", from, to, err)
	}
	return nil
}

func (r *ContractRepo) query(ctx context.Context, query string, args ...any) ([]*domain.Contract, error) {
	rows, err := r.db.pool.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing contracts: %w", err)
	}
	defer rows.Close()

	contracts := []*domain.Contract{}
	for rows.Next() {
		c, err := scanContract(rows)
		if err != nil {
			return nil, err
		}
		contracts = append(contracts, c)
	}
	return contracts, rows.Err()
}

func writeArgs(c *domain.Contract) []any {
	var txid *string
	var vout *int32
	if c.Funding != nil {
		txid, vout = &c.Funding.Txid, new(int32)
		*vout = int32(c.Funding.Vout)
	}

	return []any{
		c.ID, c.State, c.Creator, c.ShortUser, c.LongUser,
		c.Terms.NominalUnitsXSatsPerBtc, c.Terms.SatsForNominalUnitsAtHighLiquidation,
		c.Terms.PayoutSats, c.Terms.LowLiquidationPrice, c.Terms.HighLiquidationPrice,
		c.Terms.ShortLockScript, c.Terms.LongLockScript, c.Terms.OraclePubKey,
		c.Terms.StartTimestamp, c.Terms.MaturityTimestamp,
		c.ShortKey, c.LongKey, c.ArkdSigner, c.EmulatorSigner,
		int32(c.ExitDelay.Value), c.ExitDelay.Type == arklib.LocktimeTypeBlock,
		c.EnableMutualRedemption, c.PkScript,
		c.ShortStake, c.LongStake, txid, vout,
	}
}

// scanner is a *sql.Row or a *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanContract(s scanner) (*domain.Contract, error) {
	var (
		c      domain.Contract
		blocks bool
		delay  int32
		txid   *string
		vout   *int32
	)

	if err := s.Scan(
		&c.ID, &c.State, &c.Creator, &c.ShortUser, &c.LongUser,
		&c.Terms.NominalUnitsXSatsPerBtc, &c.Terms.SatsForNominalUnitsAtHighLiquidation,
		&c.Terms.PayoutSats, &c.Terms.LowLiquidationPrice, &c.Terms.HighLiquidationPrice,
		&c.Terms.ShortLockScript, &c.Terms.LongLockScript, &c.Terms.OraclePubKey,
		&c.Terms.StartTimestamp, &c.Terms.MaturityTimestamp,
		&c.ShortKey, &c.LongKey, &c.ArkdSigner, &c.EmulatorSigner,
		&delay, &blocks, &c.EnableMutualRedemption, &c.PkScript,
		&c.ShortStake, &c.LongStake, &txid, &vout,
	); err != nil {
		return nil, err
	}

	c.ExitDelay = arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: uint32(delay)}
	if blocks {
		c.ExitDelay.Type = arklib.LocktimeTypeBlock
	}
	if txid != nil && vout != nil {
		c.Funding = &domain.Outpoint{Txid: *txid, Vout: uint32(*vout)}
	}

	return &c, nil
}
