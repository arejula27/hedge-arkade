// Package server is the transport layer. echo lives here and in
// internal/oracleserver, and nowhere else: the handlers translate JSON into
// domain values, call a use case, and translate back. Nothing below this
// package imports echo, so the framework can be replaced without touching a
// line of anything else.
package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/config"
	"github.com/arejula27/hedge/service/internal/events"
	"github.com/btcsuite/btcd/chaincfg"
)

// Server holds what the handlers are allowed to reach. The use cases arrive as
// one App: turning a request into a call, and an error into a status code, is
// the whole of this layer's job.
type Server struct {
	app    *app.App
	db     prober
	broker *events.Broker

	// params is the network a contract address is rendered on.
	params *chaincfg.Params
}

type Options struct {
	App    *app.App
	DB     prober
	Broker *events.Broker
	Params *chaincfg.Params

	// AllowOrigin is where the frontend is served from.
	AllowOrigin string
}

// New assembles the HTTP server. It takes its collaborators rather than
// constructing them, so a test can hand it fakes.
func New(cfg config.Config, o Options) *http.Server {
	return &http.Server{
		Addr:        fmt.Sprintf(":%d", cfg.Port),
		Handler:     NewServer(o).Routes(o.AllowOrigin),
		IdleTimeout: time.Minute,
		ReadTimeout: 10 * time.Second,

		// No write timeout. GET /api/events is a stream that stays open for as
		// long as a browser tab is, and a timeout here would kill it mid-demo
		// with nothing in the log to say why. Handlers bound their own work
		// with the request context instead.
		WriteTimeout: 0,
	}
}

func NewServer(o Options) *Server {
	params := o.Params
	if params == nil {
		params = &chaincfg.RegressionNetParams
	}
	return &Server{app: o.App, db: o.DB, broker: o.Broker, params: params}
}
