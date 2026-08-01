//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/arejula27/hedge/service/internal/oracle"
)

// migrated opens a connection, brings the schema up, and empties the oracle's
// table so each test starts from sequence 1.
func migrated(t *testing.T) (*DB, *OracleStore) {
	t.Helper()

	db, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.pool.ExecContext(t.Context(), `TRUNCATE oracle_publications`); err != nil {
		t.Fatalf("emptying the table: %v", err)
	}

	return db, NewOracleStore(db)
}

// counting is a Sign that records what it was asked to sign, so a test can see
// the sequence the store allocated without reading the row back.
func counting(t *testing.T) oracle.Sign {
	t.Helper()
	return func(sequence uint64, timestamp, price int64) ([]byte, []byte, error) {
		return fmt.Appendf(nil, "message-%d", sequence),
			fmt.Appendf(nil, "signature-%d", sequence), nil
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db, _ := migrated(t)

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrating twice: %v", err)
	}
}

func TestAppendStartsAtOneAndCountsUp(t *testing.T) {
	_, store := migrated(t)

	for want := uint64(1); want <= 3; want++ {
		got, err := store.Append(t.Context(), 1_800_000_000, 10_000_000, counting(t))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if got.Sequence != want {
			t.Fatalf("sequence %d, want %d", got.Sequence, want)
		}
		if string(got.Message) != fmt.Sprintf("message-%d", want) {
			t.Errorf("the message was signed for %q", got.Message)
		}
	}
}

// This is the test the advisory lock exists for.
//
// Fifty publishers racing must produce 1..50 with no gaps and no duplicates. A
// BIGSERIAL would pass the "no duplicates" half and fail this one the moment a
// transaction rolled back, and every gap makes a settlement impossible forever:
// the covenant demands the predecessor be exactly one less, and a number that
// was never written can never be published later.
func TestAppendIsDenseUnderConcurrency(t *testing.T) {
	_, store := migrated(t)

	const publishers = 50

	var wg sync.WaitGroup
	sequences := make([]uint64, publishers)
	failures := make([]error, publishers)

	for i := range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := store.Append(context.Background(), 1_800_000_000, 10_000_000, counting(t))
			sequences[i], failures[i] = p.Sequence, err
		}()
	}
	wg.Wait()

	seen := make(map[uint64]bool, publishers)
	for i, err := range failures {
		if err != nil {
			t.Fatalf("publisher %d: %v", i, err)
		}
		if seen[sequences[i]] {
			t.Fatalf("sequence %d was handed out twice", sequences[i])
		}
		seen[sequences[i]] = true
	}

	for want := uint64(1); want <= publishers; want++ {
		if !seen[want] {
			t.Fatalf("sequence %d is missing: the sequence has a hole in it", want)
		}
	}
}

// A publication whose signing fails must leave no trace. The number it would
// have used has to stay available, or the hole is permanent.
func TestAFailedSigningBurnsNoSequence(t *testing.T) {
	_, store := migrated(t)

	if _, err := store.Append(t.Context(), 1_800_000_000, 10_000_000, counting(t)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	refusing := func(uint64, int64, int64) ([]byte, []byte, error) {
		return nil, nil, errors.New("the signing key is gone")
	}
	if _, err := store.Append(t.Context(), 1_800_000_000, 10_000_000, refusing); err == nil {
		t.Fatal("Append succeeded with a signer that refused")
	}

	got, err := store.Append(t.Context(), 1_800_000_000, 10_000_000, counting(t))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got.Sequence != 2 {
		t.Errorf("sequence %d after a failed signing, want 2 — the number was burnt", got.Sequence)
	}
}

func TestLatestAndAtReadBackWhatWasWritten(t *testing.T) {
	_, store := migrated(t)

	for i := range 3 {
		if _, err := store.Append(t.Context(), int64(1_800_000_000+i), int64(10_000_000+i), counting(t)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	latest, err := store.Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Sequence != 3 || latest.Price != 10_000_002 || latest.Timestamp != 1_800_000_002 {
		t.Errorf("Latest = %+v", latest)
	}

	second, err := store.At(t.Context(), 2)
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	if second.Sequence != 2 || second.Price != 10_000_001 {
		t.Errorf("At(2) = %+v", second)
	}
	if string(second.Message) != "message-2" || string(second.Signature) != "signature-2" {
		t.Errorf("the bytes came back as %q / %q", second.Message, second.Signature)
	}
}

func TestAnEmptyOracleSaysSoRatherThanReturningNothing(t *testing.T) {
	_, store := migrated(t)

	if _, err := store.Latest(t.Context()); !errors.Is(err, oracle.ErrNoPublications) {
		t.Errorf("Latest on an empty table gave %v", err)
	}
	if _, err := store.At(t.Context(), 7); !errors.Is(err, oracle.ErrNoPublications) {
		t.Errorf("At(7) gave %v", err)
	}
}

func TestHistoryIsNewestFirstAndRespectsTheLimit(t *testing.T) {
	_, store := migrated(t)

	for range 5 {
		if _, err := store.Append(t.Context(), 1_800_000_000, 10_000_000, counting(t)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	history, err := store.History(t.Context(), 3)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("got %d publications, want 3", len(history))
	}
	for i, want := range []uint64{5, 4, 3} {
		if history[i].Sequence != want {
			t.Errorf("publication %d has sequence %d, want %d", i, history[i].Sequence, want)
		}
	}
}

// The price column is checked in the database as well as in the publisher: a
// zero or negative price would make the covenant's division meaningless, and
// the schema is the last place that can refuse it.
func TestTheSchemaRefusesAPriceThatIsNotPositive(t *testing.T) {
	_, store := migrated(t)

	for _, price := range []int64{0, -1} {
		if _, err := store.Append(t.Context(), 1_800_000_000, price, counting(t)); err == nil {
			t.Errorf("the database accepted a price of %d", price)
		}
	}
}
