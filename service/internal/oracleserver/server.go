// Package oracleserver is the oracle's HTTP transport.
//
// echo lives here and in internal/server, and nowhere else. Nothing below
// either package imports it.
package oracleserver

import (
	"fmt"
	"net/http"
	"time"

	"github.com/arejula27/hedge/service/internal/oracle"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Server serves signed prices.
//
// The publisher is concrete rather than a port: it holds no I/O of its own, and
// what a test needs to control is the store underneath it.
type Server struct {
	publisher *oracle.Publisher
	store     oracle.Store
	interval  time.Duration

	// allowManual lets a client set the price. That is a demo control; a real
	// feed reads a market and exposes no such thing.
	allowManual bool
}

type Options struct {
	Port        int
	Interval    time.Duration
	AllowManual bool
}

func NewServer(p *oracle.Publisher, store oracle.Store, opts Options) *Server {
	return &Server{
		publisher:   p,
		store:       store,
		interval:    opts.Interval,
		allowManual: opts.AllowManual,
	}
}

func New(p *oracle.Publisher, store oracle.Store, opts Options) *http.Server {
	return &http.Server{
		Addr:         fmt.Sprintf(":%d", opts.Port),
		Handler:      NewServer(p, store, opts).Routes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
}

func (s *Server) Routes() http.Handler {
	e := echo.New()
	e.HideBanner = true

	// No CORS: no browser talks to the oracle. The service proxies it so the
	// frontend has a single origin.
	e.Use(middleware.Recover())

	e.GET("/oracle/info", s.info)
	e.GET("/oracle/latest", s.latest)
	e.GET("/oracle/pair", s.latestPair)
	e.GET("/oracle/pair/:sequence", s.pairAt)
	e.GET("/oracle/history", s.history)
	e.POST("/oracle/price", s.setPrice)

	return e
}
