package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
)

// ExitRepo stores the pre-signed unilateral exit: written once at funding, read
// only when a party gives up on the operator.
type ExitRepo struct {
	db *DB
}

func NewExitRepo(db *DB) *ExitRepo { return &ExitRepo{db: db} }

// Put writes the package.
//
// Funding is retried after a restart, so writing the same package twice has to
// be safe. What can change afterwards is where the exit landed, so that is the
// only thing an existing row takes.
func (r *ExitRepo) Put(ctx context.Context, e domain.ExitPackage) error {
	var txid *string
	var vout *int32
	if e.Swept != nil {
		txid, vout = &e.Swept.Txid, new(int32)
		*vout = int32(e.Swept.Vout)
	}

	_, err := r.db.pool.ExecContext(ctx,
		`INSERT INTO exit_packages
		 (contract_id, raw_tx, amount, sweep_pkscript, sweep_leaf, sweep_control,
		  short_sig, long_sig, sweep_txid, sweep_vout, sweep_sats)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (contract_id) DO UPDATE SET
			sweep_txid = EXCLUDED.sweep_txid,
			sweep_vout = EXCLUDED.sweep_vout,
			sweep_sats = EXCLUDED.sweep_sats`,
		e.ContractID, e.RawTx, e.Amount,
		e.Sweep.PkScript, e.Sweep.Leaf, e.Sweep.ControlBlock,
		e.ShortSig, e.LongSig, txid, vout, e.SweptSats)
	if err != nil {
		return fmt.Errorf("writing the exit package for %s: %w", e.ContractID, err)
	}
	return nil
}

func (r *ExitRepo) Get(ctx context.Context, contract uuid.UUID) (domain.ExitPackage, error) {
	var e domain.ExitPackage
	e.ContractID = contract

	var (
		txid *string
		vout *int32
		sats sql.NullInt64
	)

	err := r.db.pool.QueryRowContext(ctx,
		`SELECT raw_tx, amount, sweep_pkscript, sweep_leaf, sweep_control, short_sig, long_sig,
		        sweep_txid, sweep_vout, sweep_sats
		 FROM exit_packages WHERE contract_id = $1`, contract,
	).Scan(&e.RawTx, &e.Amount,
		&e.Sweep.PkScript, &e.Sweep.Leaf, &e.Sweep.ControlBlock,
		&e.ShortSig, &e.LongSig, &txid, &vout, &sats)

	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExitPackage{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ExitPackage{}, fmt.Errorf("reading the exit package: %w", err)
	}

	if txid != nil && vout != nil {
		e.Swept = &domain.Outpoint{Txid: *txid, Vout: uint32(*vout)}
		e.SweptSats = sats.Int64
	}
	return e, nil
}
