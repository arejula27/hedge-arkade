package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
)

// ArbitrationRepo stores the split the service proposes after a unilateral
// exit, and the signatures it collects for it.
type ArbitrationRepo struct {
	db *DB
}

func NewArbitrationRepo(db *DB) *ArbitrationRepo { return &ArbitrationRepo{db: db} }

func (r *ArbitrationRepo) Put(ctx context.Context, a *domain.Arbitration) error {
	signatures, err := json.Marshal(a.Signatures)
	if err != nil {
		return fmt.Errorf("encoding the signatures: %w", err)
	}

	_, err = r.db.pool.ExecContext(ctx,
		`INSERT INTO arbitrations
		 (id, contract_id, short_sats, long_sats, price, message, signature,
		  raw_tx, available, signatures, txid)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 ON CONFLICT (contract_id) DO UPDATE SET
			signatures = EXCLUDED.signatures, txid = EXCLUDED.txid`,
		a.ID, a.ContractID, a.ShortSats, a.LongSats, a.Price, a.Message, a.Signature,
		a.RawTx, a.Available, string(signatures), nullableString(a.Txid))
	if err != nil {
		return fmt.Errorf("writing the arbitration for %s: %w", a.ContractID, err)
	}
	return nil
}

func (r *ArbitrationRepo) ForContract(ctx context.Context, contract uuid.UUID) (*domain.Arbitration, error) {
	var (
		a          domain.Arbitration
		signatures string
		txid       sql.NullString
	)
	a.ContractID = contract

	err := r.db.pool.QueryRowContext(ctx,
		`SELECT id, short_sats, long_sats, price, message, signature,
		        raw_tx, available, signatures, txid
		 FROM arbitrations WHERE contract_id = $1`, contract,
	).Scan(&a.ID, &a.ShortSats, &a.LongSats, &a.Price, &a.Message, &a.Signature,
		&a.RawTx, &a.Available, &signatures, &txid)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading the arbitration: %w", err)
	}

	a.Txid = txid.String
	if err := json.Unmarshal([]byte(signatures), &a.Signatures); err != nil {
		return nil, fmt.Errorf("reading the signatures: %w", err)
	}
	if a.Signatures == nil {
		a.Signatures = map[string]string{}
	}
	return &a, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
