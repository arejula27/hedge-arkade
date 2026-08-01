package arkade

import (
	"context"
	"fmt"
	"time"
)

// Poll retries attempt until it succeeds or ctx expires, reporting the last
// error rather than only that time ran out. arkd's view of the chain lags the
// faucet, and settling is a batch that has to close.
func Poll(ctx context.Context, every time.Duration, what string, attempt func() error) error {
	last := attempt()
	if last == nil {
		return nil
	}

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w (last error: %v)", what, ctx.Err(), last)
		case <-ticker.C:
			if last = attempt(); last == nil {
				return nil
			}
		}
	}
}
