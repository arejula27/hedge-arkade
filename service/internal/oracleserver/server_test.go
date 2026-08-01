package oracleserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arejula27/hedge/service/internal/oracle"
	"github.com/btcsuite/btcd/btcec/v2"
)

// stubStore keeps publications in a slice and allocates sequences the way the
// real one does.
type stubStore struct {
	published []oracle.Publication
	failWith  error
}

func (s *stubStore) Append(_ context.Context, timestamp, price int64, sign oracle.Sign) (oracle.Publication, error) {
	if s.failWith != nil {
		return oracle.Publication{}, s.failWith
	}
	sequence := uint64(len(s.published)) + 1
	message, signature, err := sign(sequence, timestamp, price)
	if err != nil {
		return oracle.Publication{}, err
	}
	p := oracle.Publication{
		Sequence: sequence, Timestamp: timestamp, Price: price,
		Message: message, Signature: signature,
	}
	s.published = append(s.published, p)
	return p, nil
}

func (s *stubStore) Latest(context.Context) (oracle.Publication, error) {
	if len(s.published) == 0 {
		return oracle.Publication{}, oracle.ErrNoPublications
	}
	return s.published[len(s.published)-1], nil
}

func (s *stubStore) At(_ context.Context, sequence uint64) (oracle.Publication, error) {
	if sequence == 0 || sequence > uint64(len(s.published)) {
		return oracle.Publication{}, oracle.ErrNoPublications
	}
	return s.published[sequence-1], nil
}

func (s *stubStore) History(_ context.Context, limit int) ([]oracle.Publication, error) {
	if s.failWith != nil {
		return nil, s.failWith
	}
	if limit > len(s.published) {
		limit = len(s.published)
	}
	out := make([]oracle.Publication, 0, limit)
	for i := len(s.published) - 1; i >= len(s.published)-limit; i-- {
		out = append(out, s.published[i])
	}
	return out, nil
}

var stubKey, _ = btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x11}, 32))

// serve builds the server with a store, publishing `filled` prices into it
// first. It drives requests through the real router rather than calling the
// handler directly, so the route table is covered too.
func serve(t *testing.T, allowManual bool, filled int) (*Server, *stubStore) {
	t.Helper()

	store := &stubStore{}
	publisher := oracle.NewPublisher(store, stubKey, 10_000_000)
	for range filled {
		if _, err := publisher.Publish(t.Context()); err != nil {
			t.Fatalf("filling the store: %v", err)
		}
	}

	return &Server{
		publisher:   publisher,
		store:       store,
		interval:    5 * time.Second,
		allowManual: allowManual,
	}, store
}

func do(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set(echoContentType, "application/json")
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

const echoContentType = "Content-Type"

func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()

	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
}

func TestInfoReportsTheKeyAContractIsBuiltWith(t *testing.T) {
	s, _ := serve(t, true, 3)

	rec := do(t, s, http.MethodGet, "/oracle/info", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	var body infoResponse
	decode(t, rec, &body)

	if want := hex.EncodeToString(s.publisher.PublicKey()); body.PubKey != want {
		t.Errorf("pubkey %q, want %q", body.PubKey, want)
	}
	if len(body.PubKey) != 64 {
		t.Errorf("pubkey is %d hex chars, want 64 for an x-only key", len(body.PubKey))
	}
	if body.CadenceSeconds != 5 {
		t.Errorf("cadence %d, want 5", body.CadenceSeconds)
	}
	if body.LatestSequence != 3 {
		t.Errorf("latest sequence %d, want 3", body.LatestSequence)
	}
	if !body.AllowsManual {
		t.Error("allows_manual is false on an oracle that takes prices")
	}
}

// An oracle that has published nothing is still a usable oracle: a contract can
// be built against its key before it has said anything.
func TestInfoAnswersBeforeAnythingIsPublished(t *testing.T) {
	s, _ := serve(t, false, 0)

	rec := do(t, s, http.MethodGet, "/oracle/info", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}

	var body infoResponse
	decode(t, rec, &body)
	if body.LatestSequence != 0 {
		t.Errorf("latest sequence %d, want 0", body.LatestSequence)
	}
}

func TestLatestServesTheMostRecentPublication(t *testing.T) {
	s, _ := serve(t, false, 3)

	rec := do(t, s, http.MethodGet, "/oracle/latest", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	var body publication
	decode(t, rec, &body)

	if body.Sequence != 3 {
		t.Errorf("sequence %d, want 3", body.Sequence)
	}
	// 24 bytes of message, 64 of signature, both hex.
	if len(body.Message) != 48 {
		t.Errorf("message is %d hex chars, want 48", len(body.Message))
	}
	if len(body.Signature) != 128 {
		t.Errorf("signature is %d hex chars, want 128", len(body.Signature))
	}
	if _, err := hex.DecodeString(body.Message); err != nil {
		t.Errorf("message is not hex: %v", err)
	}
}

func TestLatestIs404OnAnEmptyOracle(t *testing.T) {
	s, _ := serve(t, false, 0)

	if rec := do(t, s, http.MethodGet, "/oracle/latest", ""); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// The covenant needs a message and its immediate predecessor. Serving the pair
// is what keeps that rule in the one place that can guarantee it.
func TestPairIsAdjacent(t *testing.T) {
	s, _ := serve(t, false, 4)

	for _, tc := range []struct {
		name             string
		path             string
		settle, previous uint64
	}{
		{"the latest", "/oracle/pair", 4, 3},
		{"at a sequence", "/oracle/pair/2", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, s, http.MethodGet, tc.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}

			var body pairResponse
			decode(t, rec, &body)

			if body.Settlement.Sequence != tc.settle {
				t.Errorf("settlement %d, want %d", body.Settlement.Sequence, tc.settle)
			}
			if body.Previous.Sequence != tc.previous {
				t.Errorf("previous %d, want %d", body.Previous.Sequence, tc.previous)
			}
		})
	}
}

func TestPairRefusesWhatCannotBeAPair(t *testing.T) {
	s, _ := serve(t, false, 3)

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{"the first publication has no predecessor", "/oracle/pair/1", http.StatusNotFound},
		{"a sequence that does not exist", "/oracle/pair/99", http.StatusNotFound},
		{"a sequence that is not a number", "/oracle/pair/soon", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(t, s, http.MethodGet, tc.path, ""); rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestPairIs404WithFewerThanTwoPublications(t *testing.T) {
	for _, filled := range []int{0, 1} {
		s, _ := serve(t, false, filled)
		if rec := do(t, s, http.MethodGet, "/oracle/pair", ""); rec.Code != http.StatusNotFound {
			t.Errorf("with %d publications: status %d, want 404", filled, rec.Code)
		}
	}
}

func TestHistoryIsNewestFirst(t *testing.T) {
	s, _ := serve(t, false, 5)

	rec := do(t, s, http.MethodGet, "/oracle/history?limit=3", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	var body []publication
	decode(t, rec, &body)

	if len(body) != 3 {
		t.Fatalf("got %d publications, want 3", len(body))
	}
	for i, want := range []uint64{5, 4, 3} {
		if body[i].Sequence != want {
			t.Errorf("publication %d has sequence %d, want %d", i, body[i].Sequence, want)
		}
	}
}

func TestHistoryOfAnEmptyOracleIsAnEmptyList(t *testing.T) {
	s, _ := serve(t, false, 0)

	rec := do(t, s, http.MethodGet, "/oracle/history", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body %q, want []", got)
	}
}

func TestHistoryRefusesALimitThatIsNotOne(t *testing.T) {
	s, _ := serve(t, false, 2)

	for _, limit := range []string{"0", "-1", "soon"} {
		rec := do(t, s, http.MethodGet, "/oracle/history?limit="+limit, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%s: status %d, want 400", limit, rec.Code)
		}
	}
}

// Taking a price over HTTP is a demo control. A real feed reads a market, and
// an oracle that can be told what to say is one nobody should build a contract
// against.
func TestSetPriceIsRefusedUnlessManualIsOn(t *testing.T) {
	s, store := serve(t, false, 1)

	rec := do(t, s, http.MethodPost, "/oracle/price", `{"price":5000000}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}
	if len(store.published) != 1 {
		t.Error("a refused price was published anyway")
	}
	if s.publisher.Price() != 10_000_000 {
		t.Errorf("a refused price changed the current one to %d", s.publisher.Price())
	}
}

// Setting a price publishes it there and then, so a caller that has just moved
// the price can settle on it without waiting for the next tick.
func TestSetPricePublishesImmediately(t *testing.T) {
	s, store := serve(t, true, 1)

	rec := do(t, s, http.MethodPost, "/oracle/price", `{"price":4999999}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var body publication
	decode(t, rec, &body)

	if body.Price != 4_999_999 {
		t.Errorf("published %d, want 4999999", body.Price)
	}
	if body.Sequence != 2 {
		t.Errorf("sequence %d, want 2", body.Sequence)
	}
	if len(store.published) != 2 {
		t.Errorf("the store has %d publications, want 2", len(store.published))
	}
	if s.publisher.Price() != 4_999_999 {
		t.Errorf("the current price is %d", s.publisher.Price())
	}
}

func TestSetPriceRefusesWhatCannotBeAPrice(t *testing.T) {
	s, _ := serve(t, true, 1)

	for _, tc := range []struct{ name, body string }{
		{"zero", `{"price":0}`},
		{"negative", `{"price":-1}`},
		{"missing", `{}`},
		{"not json", `nonsense`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(t, s, http.MethodPost, "/oracle/price", tc.body); rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400", rec.Code)
			}
		})
	}
}

func TestAStoreFailureIsNotA404(t *testing.T) {
	s, store := serve(t, false, 0)
	store.failWith = errors.New("the database is down")

	if rec := do(t, s, http.MethodGet, "/oracle/history", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
}

func TestUnknownRoutesAre404(t *testing.T) {
	s, _ := serve(t, false, 1)

	if rec := do(t, s, http.MethodGet, "/oracle/nowhere", ""); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}
