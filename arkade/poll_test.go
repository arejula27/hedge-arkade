package arkade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPollDoesNotWaitWhenTheFirstAttemptSucceeds(t *testing.T) {
	calls := 0
	err := Poll(t.Context(), time.Hour, "nothing", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if calls != 1 {
		t.Errorf("attempted %d times, want 1", calls)
	}
}

func TestPollRetriesUntilItSucceeds(t *testing.T) {
	calls := 0
	err := Poll(t.Context(), time.Millisecond, "the third attempt", func() error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if calls != 3 {
		t.Errorf("attempted %d times, want 3", calls)
	}
}

// Reporting only that time ran out hides why. arkd's view of the chain lagging
// and arkd being down look identical from the outside otherwise.
func TestPollReportsTheLastError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err := Poll(ctx, time.Millisecond, "a batch to close", func() error {
		return errors.New("arkd said no")
	})
	if err == nil {
		t.Fatal("Poll gave up without an error")
	}
	if !strings.Contains(err.Error(), "arkd said no") {
		t.Errorf("the last error is missing from %q", err)
	}
	if !strings.Contains(err.Error(), "a batch to close") {
		t.Errorf("what was awaited is missing from %q", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the cause is not the deadline: %v", err)
	}
}

func TestPollStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := Poll(ctx, time.Hour, "anything", func() error {
		return errors.New("still failing")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Poll returned %v, want a cancellation", err)
	}
}
