package oracleserver

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/arejula27/hedge/service/internal/oracle"
	"github.com/labstack/echo/v4"
)

// publication is the wire shape. The bytes go out as hex rather than the
// base64 encoding/json would give a []byte, because hex is what every other
// tool in this protocol speaks.
type publication struct {
	Sequence  uint64 `json:"sequence"`
	Timestamp int64  `json:"timestamp"`
	Price     int64  `json:"price"`
	Message   string `json:"message"`
	Signature string `json:"signature"`
}

func wire(p oracle.Publication) publication {
	return publication{
		Sequence:  p.Sequence,
		Timestamp: p.Timestamp,
		Price:     p.Price,
		Message:   hex.EncodeToString(p.Message),
		Signature: hex.EncodeToString(p.Signature),
	}
}

type infoResponse struct {
	// PubKey is the 32-byte x-only key, which is what goes into a contract's
	// Terms.OraclePubKey.
	PubKey         string `json:"pubkey"`
	CadenceSeconds int    `json:"cadence_seconds"`
	LatestSequence uint64 `json:"latest_sequence"`
	AllowsManual   bool   `json:"allows_manual"`
}

func (s *Server) info(c echo.Context) error {
	response := infoResponse{
		PubKey:         hex.EncodeToString(s.publisher.PublicKey()),
		CadenceSeconds: int(s.interval.Seconds()),
		AllowsManual:   s.allowManual,
	}

	// An oracle that has published nothing is still a usable oracle; it just
	// has no history yet.
	latest, err := s.store.Latest(c.Request().Context())
	if err == nil {
		response.LatestSequence = latest.Sequence
	} else if !errors.Is(err, oracle.ErrNoPublications) {
		return err
	}

	return c.JSON(http.StatusOK, response)
}

func (s *Server) latest(c echo.Context) error {
	p, err := s.store.Latest(c.Request().Context())
	if err != nil {
		return notFoundIfEmpty(err, "the oracle has published nothing yet")
	}
	return c.JSON(http.StatusOK, wire(p))
}

type pairResponse struct {
	Settlement publication `json:"settlement"`
	Previous   publication `json:"previous"`
}

// A pair is served rather than assembled by the caller from two requests. The
// covenant needs a message and its *immediate* predecessor, and the oracle is
// the only thing that can promise the two are adjacent.
func (s *Server) latestPair(c echo.Context) error {
	pair, err := oracle.LatestPair(c.Request().Context(), s.store)
	if err != nil {
		return notFoundIfEmpty(err, "the oracle has fewer than two publications")
	}
	return c.JSON(http.StatusOK, pairResponse{wire(pair.Settlement), wire(pair.Previous)})
}

func (s *Server) pairAt(c echo.Context) error {
	sequence, err := strconv.ParseUint(c.Param("sequence"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "sequence must be a number")
	}

	pair, err := oracle.PairAt(c.Request().Context(), s.store, sequence)
	if err != nil {
		return notFoundIfEmpty(err, "no publication at that sequence, or none before it")
	}
	return c.JSON(http.StatusOK, pairResponse{wire(pair.Settlement), wire(pair.Previous)})
}

const (
	defaultHistory = 100
	maxHistory     = 1000
)

func (s *Server) history(c echo.Context) error {
	limit := defaultHistory
	if raw := c.QueryParam("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, "limit must be a positive number")
		}
		limit = min(parsed, maxHistory)
	}

	history, err := s.store.History(c.Request().Context(), limit)
	if err != nil {
		return err
	}

	out := make([]publication, 0, len(history))
	for _, p := range history {
		out = append(out, wire(p))
	}
	return c.JSON(http.StatusOK, out)
}

type priceRequest struct {
	Price int64 `json:"price"`
}

// setPrice moves the price and publishes it immediately, so a caller that has
// just set a price can settle on it without waiting for the next tick.
func (s *Server) setPrice(c echo.Context) error {
	if !s.allowManual {
		return echo.NewHTTPError(http.StatusForbidden, "this oracle does not take prices over HTTP")
	}

	var request priceRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "expected {\"price\": <number>}")
	}
	if err := s.publisher.SetPrice(request.Price); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	p, err := s.publisher.Publish(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, wire(p))
}

func notFoundIfEmpty(err error, message string) error {
	if errors.Is(err, oracle.ErrNoPublications) {
		return echo.NewHTTPError(http.StatusNotFound, message)
	}
	return err
}
