package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type UserRepo struct {
	db *DB
}

func NewUserRepo(db *DB) *UserRepo { return &UserRepo{db: db} }

// Create writes the user and their wallet seed in one transaction. A user with
// no wallet is a user who can do nothing, so the two are never apart.
func (r *UserRepo) Create(ctx context.Context, u domain.User, seed []byte) error {
	tx, err := r.db.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning the user: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, name, public_key) VALUES ($1, $2, $3)`,
		u.ID, u.Name, u.PublicKey,
	); err != nil {
		if uniqueViolation(err) {
			return domain.ErrNameTaken
		}
		return fmt.Errorf("writing the user: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallets (user_id, seed) VALUES ($1, $2)`, u.ID, seed,
	); err != nil {
		return fmt.Errorf("writing the wallet: %w", err)
	}

	return tx.Commit()
}

func (r *UserRepo) Get(ctx context.Context, id uuid.UUID) (domain.User, error) {
	return r.one(ctx, `SELECT id, name, public_key FROM users WHERE id = $1`, id)
}

func (r *UserRepo) ByName(ctx context.Context, name string) (domain.User, error) {
	return r.one(ctx, `SELECT id, name, public_key FROM users WHERE name = $1`, name)
}

func (r *UserRepo) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.pool.QueryContext(ctx,
		`SELECT id, name, public_key FROM users ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	users := []domain.User{}
	for rows.Next() {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.Name, &u.PublicKey); err != nil {
			return nil, fmt.Errorf("reading a user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// Seed is the user's wallet key. It exists because the demo holds the wallets;
// it is the one call that goes away when they move to the user's own device.
func (r *UserRepo) Seed(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var seed []byte
	err := r.db.pool.QueryRowContext(ctx,
		`SELECT seed FROM wallets WHERE user_id = $1`, id).Scan(&seed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading a wallet: %w", err)
	}
	return seed, nil
}

func (r *UserRepo) one(ctx context.Context, query string, args ...any) (domain.User, error) {
	var u domain.User
	err := r.db.pool.QueryRowContext(ctx, query, args...).Scan(&u.ID, &u.Name, &u.PublicKey)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("reading a user: %w", err)
	}
	return u, nil
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
