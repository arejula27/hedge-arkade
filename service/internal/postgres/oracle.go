package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arejula27/hedge/service/internal/oracle"
)

// oracleLock is the number every writer of oracle_publications agrees on.
// pg_advisory_xact_lock takes a number, not a table, so the number is the
// agreement.
const oracleLock = 8_100_071

// OracleStore is oracle.Store over postgres.
type OracleStore struct {
	db *DB
}

func NewOracleStore(db *DB) *OracleStore { return &OracleStore{db: db} }

// Append allocates the next sequence, signs with it, and writes the row, all in
// one transaction under an advisory lock.
//
// The lock is what makes the sequence dense. Reading MAX(sequence) without it
// lets two concurrent publishers pick the same number, and a BIGSERIAL instead
// would leave a hole every time a transaction rolled back — a hole no later
// publication can fill, and one that makes every settlement needing that number
// as a predecessor impossible.
func (s *OracleStore) Append(
	ctx context.Context, timestamp, price int64, sign oracle.Sign,
) (oracle.Publication, error) {
	tx, err := s.db.pool.BeginTx(ctx, nil)
	if err != nil {
		return oracle.Publication{}, fmt.Errorf("beginning the publication: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, oracleLock); err != nil {
		return oracle.Publication{}, fmt.Errorf("taking the publication lock: %w", err)
	}

	var sequence uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM oracle_publications`,
	).Scan(&sequence); err != nil {
		return oracle.Publication{}, fmt.Errorf("reading the last sequence: %w", err)
	}

	message, signature, err := sign(sequence, timestamp, price)
	if err != nil {
		return oracle.Publication{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO oracle_publications (sequence, ts, price, message, signature)
		 VALUES ($1, $2, $3, $4, $5)`,
		sequence, timestamp, price, message, signature,
	); err != nil {
		return oracle.Publication{}, fmt.Errorf("writing publication %d: %w", sequence, err)
	}

	if err := tx.Commit(); err != nil {
		return oracle.Publication{}, fmt.Errorf("committing publication %d: %w", sequence, err)
	}

	return oracle.Publication{
		Sequence:  sequence,
		Timestamp: timestamp,
		Price:     price,
		Message:   message,
		Signature: signature,
	}, nil
}

func (s *OracleStore) Latest(ctx context.Context) (oracle.Publication, error) {
	return s.one(ctx,
		`SELECT sequence, ts, price, message, signature
		 FROM oracle_publications ORDER BY sequence DESC LIMIT 1`)
}

func (s *OracleStore) At(ctx context.Context, sequence uint64) (oracle.Publication, error) {
	return s.one(ctx,
		`SELECT sequence, ts, price, message, signature
		 FROM oracle_publications WHERE sequence = $1`, sequence)
}

func (s *OracleStore) History(ctx context.Context, limit int) ([]oracle.Publication, error) {
	rows, err := s.db.pool.QueryContext(ctx,
		`SELECT sequence, ts, price, message, signature
		 FROM oracle_publications ORDER BY sequence DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("reading the history: %w", err)
	}
	defer rows.Close()

	var history []oracle.Publication
	for rows.Next() {
		var p oracle.Publication
		if err := rows.Scan(&p.Sequence, &p.Timestamp, &p.Price, &p.Message, &p.Signature); err != nil {
			return nil, fmt.Errorf("reading a publication: %w", err)
		}
		history = append(history, p)
	}
	return history, rows.Err()
}

func (s *OracleStore) one(ctx context.Context, query string, args ...any) (oracle.Publication, error) {
	var p oracle.Publication
	err := s.db.pool.QueryRowContext(ctx, query, args...).
		Scan(&p.Sequence, &p.Timestamp, &p.Price, &p.Message, &p.Signature)
	if errors.Is(err, sql.ErrNoRows) {
		return oracle.Publication{}, oracle.ErrNoPublications
	}
	if err != nil {
		return oracle.Publication{}, fmt.Errorf("reading a publication: %w", err)
	}
	return p, nil
}
