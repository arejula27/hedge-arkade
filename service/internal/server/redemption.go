package server

import (
	"encoding/hex"
	"net/http"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/labstack/echo/v4"
)

type redemptionResponse struct {
	ID         string `json:"id"`
	ProposedBy string `json:"proposed_by"`

	ShortSats int64 `json:"short_sats"`
	LongSats  int64 `json:"long_sats"`

	// The evidence, when the split came from an oracle price. It travels so the
	// other party can check the numbers against the same bytes rather than
	// against a promise — and so the close can be audited afterwards.
	Price     int64  `json:"price,omitempty"`
	Message   string `json:"message,omitempty"`
	Signature string `json:"signature,omitempty"`

	ShortSigned bool `json:"short_signed"`
	LongSigned  bool `json:"long_signed"`
}

func asRedemption(r *domain.Redemption) redemptionResponse {
	out := redemptionResponse{
		ID:          r.ID.String(),
		ProposedBy:  r.ProposedBy.String(),
		ShortSats:   r.ShortSats,
		LongSats:    r.LongSats,
		Price:       r.Price,
		ShortSigned: r.ShortSigned,
		LongSigned:  r.LongSigned,
	}
	if r.FromOracle() {
		out.Message = hex.EncodeToString(r.Message)
		out.Signature = hex.EncodeToString(r.Signature)
	}
	return out
}

type proposeRedemptionRequest struct {
	// Leave both at zero to close at the oracle's price, which is the case with
	// something to check.
	ShortSats int64 `json:"short_sats"`
	LongSats  int64 `json:"long_sats"`
}

func (s *Server) proposeRedemption(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	var request proposeRedemptionRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "that is not a split")
	}

	proposal, err := s.app.ProposeRedemption(c.Request().Context(), id, caller(c),
		app.Split{ShortSats: request.ShortSats, LongSats: request.LongSats})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, asRedemption(proposal))
}

func (s *Server) signRedemption(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	proposal, err := s.app.SignRedemption(c.Request().Context(), id, caller(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, asRedemption(proposal))
}

func (s *Server) rejectRedemption(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	contract, err := s.app.RejectRedemption(ctx, id, caller(c))
	if err != nil {
		return err
	}
	names, err := s.names(ctx)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s.view(contract, names))
}

func (s *Server) showRedemption(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	proposal, err := s.app.Redemption(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, asRedemption(proposal))
}
