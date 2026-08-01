// Package server is the transport layer. echo lives here and nowhere else: the
// handlers translate JSON into domain values, call the domain, and translate
// back. Nothing below this package imports echo, so the framework can be
// replaced without touching a line of anything else.
package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/arejula27/hedge/service/internal/config"
)

// Server holds what the handlers are allowed to reach. Today that is only the
// database, for the readiness probe; the domain service joins it here when the
// first use case arrives, and no handler signature changes when it does.
type Server struct {
	db prober
}

// New assembles the HTTP server. It takes its collaborators rather than
// constructing them, so a test can hand it fakes.
func New(cfg config.Config, db prober) *http.Server {
	s := &Server{db: db}

	return &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      s.Routes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
}
