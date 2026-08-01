package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/apptest"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/arejula27/hedge/service/internal/events"
	"github.com/arejula27/hedge/service/internal/postgres"
	"github.com/google/uuid"
)

// Requests go through the real router rather than to a handler directly, so the
// route table, the middleware and the error mapping are all covered by the same
// call. What sits behind it is a real App over the shared stubs, so the status
// codes are the ones a real outcome produces.

type stubProber struct{ health postgres.Health }

func (s stubProber) Check(context.Context) postgres.Health { return s.health }

type harness struct {
	*apptest.Fixture
	server *Server
	broker *events.Broker
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	fixture := apptest.New(t)
	broker := events.NewBroker()

	// The store is wrapped so every transition is announced, which is what the
	// composition root does.
	fixture.App = app.New(fixture.Options(events.Announce(fixture.ContractStore, broker)))

	return &harness{
		Fixture: fixture,
		server: NewServer(Options{
			App:    fixture.App,
			DB:     stubProber{health: postgres.Health{Up: true}},
			Broker: broker,
		}),
		broker: broker,
	}
}

func (h *harness) do(t *testing.T, method, path, body string, as uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if as != uuid.Nil {
		req.Header.Set(userHeader, as.String())
	}

	rec := httptest.NewRecorder()
	h.server.Routes("").ServeHTTP(rec, req)
	return rec
}

func (h *harness) get(t *testing.T, path string, as uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, http.MethodGet, path, "", as)
}

func (h *harness) post(t *testing.T, path, body string, as uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, http.MethodPost, path, body, as)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()

	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
}

func requireStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body.String())
	}
}

// standardProposal is the position the integration tests use, as the API takes
// it: a $10,000 hedge against 0.2 BTC, liquidating at $50,000 and $200,000.
const standardProposal = `{
	"side": "short",
	"hedge_value_cents": 1000000,
	"payout_sats": 20000000,
	"low_liquidation_cents": 5000000,
	"high_liquidation_cents": 20000000,
	"maturity_in_seconds": 86400,
	"enable_mutual_redemption": true
}`

func (h *harness) propose(t *testing.T) contractResponse {
	t.Helper()

	rec := h.post(t, "/api/contracts", standardProposal, h.Alice)
	requireStatus(t, rec, http.StatusCreated)

	var proposed contractResponse
	decode(t, rec, &proposed)
	return proposed
}

func (h *harness) accepted(t *testing.T) contractResponse {
	t.Helper()

	proposed := h.propose(t)

	rec := h.post(t, "/api/contracts/"+proposed.ID+"/accept", "", h.Bob)
	requireStatus(t, rec, http.StatusOK)

	var accepted contractResponse
	decode(t, rec, &accepted)
	return accepted
}

func (h *harness) funded(t *testing.T) contractResponse {
	t.Helper()

	c := h.accepted(t)

	rec := h.post(t, "/api/contracts/"+c.ID+"/fund", "", h.Alice)
	requireStatus(t, rec, http.StatusOK)

	app.NewWorker(h.App, app.WorkerOptions{}).Tick(t.Context())

	rec = h.get(t, "/api/contracts/"+c.ID, uuid.Nil)
	requireStatus(t, rec, http.StatusOK)

	var active contractResponse
	decode(t, rec, &active)
	if active.State != string(domain.Active) {
		t.Fatalf("the contract is %s, want active", active.State)
	}
	return active
}

// doRaw sends whatever the caller puts in the identity header, so a value that
// is not a user id can be tested.
func (h *harness) doRaw(t *testing.T, method, path, body, user string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(userHeader, user)

	rec := httptest.NewRecorder()
	h.server.Routes("").ServeHTTP(rec, req)
	return rec
}
