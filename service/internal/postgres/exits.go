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

// Put writes the package. Funding is retried after a restart, so writing the
// same package twice has to be a no-op rather than a failure.
func (r *ExitRepo) Put(ctx context.Context, e domain.ExitPackage) error {
	_, err := r.db.pool.ExecContext(ctx,
		`INSERT INTO exit_packages
		 (contract_id, raw_tx, amount, sweep_pkscript, sweep_leaf, sweep_control, short_sig, long_sig)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (contract_id) DO NOTHING`,
		e.ContractID, e.RawTx, e.Amount,
		e.Sweep.PkScript, e.Sweep.Leaf, e.Sweep.ControlBlock,
		e.ShortSig, e.LongSig)
	if err != nil {
		return fmt.Errorf("writing the exit package for %s: %w", e.ContractID, err)
	}
	return nil
}

func (r *ExitRepo) Get(ctx context.Context, contract uuid.UUID) (domain.ExitPackage, error) {
	var e domain.ExitPackage
	e.ContractID = contract

	err := r.db.pool.QueryRowContext(ctx,
		`SELECT raw_tx, amount, sweep_pkscript, sweep_leaf, sweep_control, short_sig, long_sig
		 FROM exit_packages WHERE contract_id = $1`, contract,
	).Scan(&e.RawTx, &e.Amount,
		&e.Sweep.PkScript, &e.Sweep.Leaf, &e.Sweep.ControlBlock,
		&e.ShortSig, &e.LongSig)

	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExitPackage{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ExitPackage{}, fmt.Errorf("reading the exit package: %w", err)
	}
	return e, nil
}
