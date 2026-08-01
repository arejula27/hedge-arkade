package server

import (
	"encoding/hex"
	"net/http"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/labstack/echo/v4"
)

type arbitrationResponse struct {
	ID string `json:"id"`

	ShortSats int64 `json:"short_sats"`
	LongSats  int64 `json:"long_sats"`

	// The evidence. Without a valid oracle signature the service cannot
	// produce a proposal at all, which is what keeps it from having any
	// discretion here — and the message travels so the number can be checked
	// before anyone signs and audited afterwards.
	Price     int64  `json:"price"`
	Message   string `json:"message"`
	Signature string `json:"signature"`

	Available  int64  `json:"available"`
	Signatures int    `json:"signatures"`
	Signed     bool   `json:"signed"`
	Txid       string `json:"txid,omitempty"`
}

func asArbitration(a *domain.Arbitration) arbitrationResponse {
	return arbitrationResponse{
		ID:         a.ID.String(),
		ShortSats:  a.ShortSats,
		LongSats:   a.LongSats,
		Price:      a.Price,
		Message:    hex.EncodeToString(a.Message),
		Signature:  hex.EncodeToString(a.Signature),
		Available:  a.Available,
		Signatures: len(a.Signatures),
		Signed:     a.Signed(),
		Txid:       a.Txid,
	}
}

// exit is a party giving up on the operator. It takes minutes — a chain to
// unroll one transaction per block, then a relative timelock to wait out — so
// it answers with the contract and the worker does the rest.
func (s *Server) exit(c echo.Context) error {
	return s.act(c, s.app.Exit)
}

// arbitrate needs no identity: the service proposes the number and cannot move
// the money, so who asked it to is not a question worth answering.
func (s *Server) arbitrate(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	proposal, err := s.app.Arbitrate(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, asArbitration(proposal))
}

func (s *Server) signArbitration(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	proposal, err := s.app.SignArbitration(c.Request().Context(), id, caller(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, asArbitration(proposal))
}

func (s *Server) showArbitration(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	proposal, err := s.app.Arbitration(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, asArbitration(proposal))
}
