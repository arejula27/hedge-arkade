package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arejula27/hedge/service/internal/postgres"
)

func TestHealthReportsOkWhenTheDatabaseAnswers(t *testing.T) {
	rec := get(t, stubProber{postgres.Health{Up: true, OpenConnections: 3}}, "/health")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body healthResponse
	decode(t, rec, &body)

	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if !body.Database.Up {
		t.Error("the database is reported down")
	}
	if body.Database.OpenConnections != 3 {
		t.Errorf("open connections = %d, want 3", body.Database.OpenConnections)
	}
}

// A degraded service must say so in the status code, because that is the only
// part a load balancer reads.
func TestHealthReports503WhenTheDatabaseIsDown(t *testing.T) {
	rec := get(t, stubProber{postgres.Health{Up: false, Error: "connection refused"}}, "/health")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body healthResponse
	decode(t, rec, &body)

	if body.Status != "degraded" {
		t.Errorf("status = %q, want degraded", body.Status)
	}
	if body.Database.Error != "connection refused" {
		t.Errorf("error = %q, want the pool's own message", body.Database.Error)
	}
}

func TestUnknownRoutesAre404(t *testing.T) {
	rec := get(t, stubProber{postgres.Health{Up: true}}, "/nope")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// get drives the request through the real router rather than calling the
// handler directly, so the route table is covered too.
func get(t *testing.T, db prober, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	(&Server{db: db}).Routes("").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}
