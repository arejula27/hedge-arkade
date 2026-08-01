package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/arejula27/hedge/service/internal/events"
	"github.com/google/uuid"
)

// The stream is what makes the demo readable: the tab that did not click
// watches the contract change state at the same moment as the one that did.
//
// It runs against a real server rather than httptest.ResponseRecorder, which
// buffers everything and so cannot show that anything arrived before the
// handler returned.
func TestTheStreamDeliversATransition(t *testing.T) {
	h := newHarness(t)

	live := httptest.NewServer(h.server.Routes(""))
	defer live.Close()

	body, stop := open(t, live.URL+"/api/events")
	defer stop()

	id := uuid.New()
	h.broker.Publish(events.Event{Contract: id, State: domain.Active, Detail: "funded"})

	got := next(t, body)
	if got.Contract != id.String() {
		t.Errorf("contract %q, want %q", got.Contract, id)
	}
	if got.State != string(domain.Active) {
		t.Errorf("state %q", got.State)
	}
	if got.Detail != "funded" {
		t.Errorf("detail %q", got.Detail)
	}
}

// A tab watching one contract must not be woken by every other contract in the
// system.
func TestTheStreamCanWatchOneContract(t *testing.T) {
	h := newHarness(t)

	live := httptest.NewServer(h.server.Routes(""))
	defer live.Close()

	watched, other := uuid.New(), uuid.New()

	body, stop := open(t, live.URL+"/api/events?contract="+watched.String())
	defer stop()

	h.broker.Publish(events.Event{Contract: other, State: domain.Settled})
	h.broker.Publish(events.Event{Contract: watched, State: domain.Active})

	got := next(t, body)
	if got.Contract != watched.String() {
		t.Errorf("the stream delivered %q, want only %q", got.Contract, watched)
	}
}

func TestTheStreamRefusesAContractIdThatIsNotOne(t *testing.T) {
	h := newHarness(t)

	rec := h.get(t, "/api/events?contract=soon", uuid.Nil)
	requireStatus(t, rec, http.StatusBadRequest)
}

// Closing the connection has to take the subscription with it, or every
// transition after a tab is closed goes on writing into a buffer nobody reads.
func TestClosingTheStreamUnsubscribes(t *testing.T) {
	h := newHarness(t)

	live := httptest.NewServer(h.server.Routes(""))
	defer live.Close()

	_, stop := open(t, live.URL+"/api/events")

	waitFor(t, "the subscription to appear", func() bool { return h.broker.Subscribers() == 1 })
	stop()
	waitFor(t, "the subscription to go", func() bool { return h.broker.Subscribers() == 0 })
}

// open starts a stream and returns its body, already past the headers.
func open(t *testing.T, url string) (*bufio.Reader, func()) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type %q", got)
	}

	return bufio.NewReader(resp.Body), func() { resp.Body.Close() }
}

// next reads until a data frame arrives, skipping the heartbeat comments.
func next(t *testing.T, body *bufio.Reader) streamEvent {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := body.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the stream: %v", err)
		}

		payload, found := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !found {
			continue
		}

		var got streamEvent
		if err := json.Unmarshal([]byte(payload), &got); err != nil {
			t.Fatalf("decoding %q: %v", payload, err)
		}
		return got
	}

	t.Fatal("nothing arrived on the stream")
	return streamEvent{}
}

func waitFor(t *testing.T, what string, ready func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waited for %s", what)
}
