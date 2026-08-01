package app

import (
	"context"
	"time"

	"github.com/arejula27/hedge/service/internal/domain"
)

// Worker finishes what a request started.
//
// Funding and settling take tens of seconds against a live stack — a faucet
// payment to confirm, a batch to close, an emulator to run a script — which is
// longer than a request should hold, and longer than a process is guaranteed to
// live. So the request moves the contract into a transient state and stops, and
// this loop carries it the rest of the way, from the row alone, restart or no
// restart.
type Worker struct {
	app   *App
	every time.Duration

	// giveUpAfter is how long a contract may sit mid-step before it is written
	// off. Without it a contract whose funding can never succeed — a party
	// spent their VTXO elsewhere between accepting and funding — is retried
	// forever and never reports anything.
	giveUpAfter time.Duration

	log func(format string, args ...any)
}

type WorkerOptions struct {
	Every       time.Duration
	GiveUpAfter time.Duration
	Log         func(format string, args ...any)
}

func NewWorker(a *App, o WorkerOptions) *Worker {
	w := &Worker{app: a, every: o.Every, giveUpAfter: o.GiveUpAfter, log: o.Log}
	if w.every <= 0 {
		w.every = 2 * time.Second
	}
	if w.giveUpAfter <= 0 {
		w.giveUpAfter = 10 * time.Minute
	}
	if w.log == nil {
		w.log = func(string, ...any) {}
	}
	return w
}

// transient is every state that means there is work outstanding, with where a
// contract goes when it has been stuck in one for too long.
//
// Every one of them gives up back where it came from rather than to a state of
// its own: a settlement the emulator refuses leaves the contract exactly as it
// was, still funded and still settleable. Funding is the exception, because a
// contract that was never funded is not one anybody can do anything with.
var transient = map[domain.State]domain.State{
	domain.Funding:   domain.Failed,
	domain.Settling:  domain.Active,
	domain.Redeeming: domain.Active,

	// Exiting and arbitrating give up back where they came from too. An exit
	// that could not be broadcast changed nothing, and an arbitration nobody
	// signed can be proposed again.
	domain.Exiting:     domain.Active,
	domain.Arbitrating: domain.Exited,
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.every)
	defer ticker.Stop()

	for {
		w.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick does one pass. It is exported so a test can drive it without waiting for
// a timer.
func (w *Worker) Tick(ctx context.Context) {
	states := make([]domain.State, 0, len(transient))
	for state := range transient {
		states = append(states, state)
	}

	outstanding, err := w.app.contracts.InState(ctx, states...)
	if err != nil {
		w.log("looking for outstanding work: %v", err)
		return
	}

	for _, c := range outstanding {
		if ctx.Err() != nil {
			return
		}
		w.advance(ctx, c)
	}
}

func (w *Worker) advance(ctx context.Context, c *domain.Contract) {
	if w.app.now().Sub(c.UpdatedAt) > w.giveUpAfter {
		w.giveUp(ctx, c)
		return
	}

	var err error
	switch c.State {
	case domain.Funding:
		err = w.app.finishFunding(ctx, c)
	case domain.Settling:
		err = w.app.finishSettling(ctx, c)
	case domain.Redeeming:
		err = w.app.finishRedeeming(ctx, c)
	case domain.Exiting:
		err = w.app.finishExiting(ctx, c)
	case domain.Arbitrating:
		err = w.app.finishArbitrating(ctx, c)
	default:
		return
	}

	// Losing a race is another worker having done the job, which is not worth
	// reporting.
	if err != nil && !domain.Lost(err) {
		w.log("contract %s stuck in %s: %v", c.ID, c.State, err)
	}
}

func (w *Worker) giveUp(ctx context.Context, c *domain.Contract) {
	to, ok := transient[c.State]
	if !ok {
		return
	}

	from := c.State
	detail := "gave up after " + w.giveUpAfter.String()
	if err := w.app.contracts.Advance(ctx, c, to, detail); err != nil && !domain.Lost(err) {
		w.log("writing off contract %s: %v", c.ID, err)
		return
	}
	w.log("contract %s was stuck in %s for over %s; it is now %s", c.ID, from, w.giveUpAfter, to)
}
