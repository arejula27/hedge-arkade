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

// RedemptionRepo stores the one open early close a contract may have.
type RedemptionRepo struct {
	db *DB
}

func NewRedemptionRepo(db *DB) *RedemptionRepo { return &RedemptionRepo{db: db} }

// Put writes the proposal, or updates the one already there.
//
// A signature arriving is an update to the same row: the packets come back with
// one more signature on them than they went out with.
func (r *RedemptionRepo) Put(ctx context.Context, red *domain.Redemption) error {
	checkpoints, err := json.Marshal(red.Checkpoints)
	if err != nil {
		return fmt.Errorf("encoding the checkpoints: %w", err)
	}

	_, err = r.db.pool.ExecContext(ctx,
		`INSERT INTO redemptions
		 (id, contract_id, proposed_by, short_sats, long_sats,
		  price, message, signature, ark_tx, checkpoints, short_signed, long_signed)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT (contract_id) DO UPDATE SET
			id = EXCLUDED.id, proposed_by = EXCLUDED.proposed_by,
			short_sats = EXCLUDED.short_sats, long_sats = EXCLUDED.long_sats,
			price = EXCLUDED.price, message = EXCLUDED.message,
			signature = EXCLUDED.signature,
			ark_tx = EXCLUDED.ark_tx, checkpoints = EXCLUDED.checkpoints,
			short_signed = EXCLUDED.short_signed, long_signed = EXCLUDED.long_signed`,
		red.ID, red.ContractID, red.ProposedBy, red.ShortSats, red.LongSats,
		nullable(red.Price, red.FromOracle()), nullableBytes(red.Message),
		nullableBytes(red.Signature),
		red.ArkTx, string(checkpoints), red.ShortSigned, red.LongSigned)
	if err != nil {
		return fmt.Errorf("writing the early close for %s: %w", red.ContractID, err)
	}
	return nil
}

func (r *RedemptionRepo) ForContract(ctx context.Context, contract uuid.UUID) (*domain.Redemption, error) {
	var (
		red         domain.Redemption
		price       sql.NullInt64
		checkpoints string
	)
	red.ContractID = contract

	err := r.db.pool.QueryRowContext(ctx,
		`SELECT id, proposed_by, short_sats, long_sats, price, message, signature,
		        ark_tx, checkpoints, short_signed, long_signed
		 FROM redemptions WHERE contract_id = $1`, contract,
	).Scan(&red.ID, &red.ProposedBy, &red.ShortSats, &red.LongSats,
		&price, &red.Message, &red.Signature,
		&red.ArkTx, &checkpoints, &red.ShortSigned, &red.LongSigned)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading the early close: %w", err)
	}
	red.Price = price.Int64

	if err := json.Unmarshal([]byte(checkpoints), &red.Checkpoints); err != nil {
		return nil, fmt.Errorf("reading the checkpoints: %w", err)
	}
	return &red, nil
}

// Drop removes a proposal that was rejected or has been submitted, so the
// contract can have another.
func (r *RedemptionRepo) Drop(ctx context.Context, contract uuid.UUID) error {
	_, err := r.db.pool.ExecContext(ctx,
		`DELETE FROM redemptions WHERE contract_id = $1`, contract)
	if err != nil {
		return fmt.Errorf("dropping the early close: %w", err)
	}
	return nil
}

func nullable(v int64, present bool) any {
	if !present {
		return nil
	}
	return v
}

func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
