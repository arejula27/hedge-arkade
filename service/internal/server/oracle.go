package server

import (
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/labstack/echo/v4"
)

type priceResponse struct {
	Sequence  uint64 `json:"sequence"`
	Timestamp int64  `json:"timestamp"`
	// Price is in cents per BTC: 10000000 is $100,000.
	Price int64 `json:"price"`
}

func asPrice(p domain.Price) priceResponse {
	return priceResponse{Sequence: p.Sequence, Timestamp: p.Timestamp, Price: p.Price}
}

func (s *Server) oracle(c echo.Context) error {
	price, err := s.app.Price(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, asPrice(price))
}

func (s *Server) oracleHistory(c echo.Context) error {
	limit := 100
	if raw := c.QueryParam("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, "limit must be a positive number")
		}
		limit = parsed
	}

	history, err := s.app.PriceHistory(c.Request().Context(), limit)
	if err != nil {
		return err
	}

	out := make([]priceResponse, 0, len(history))
	for _, p := range history {
		out = append(out, asPrice(p))
	}
	return c.JSON(http.StatusOK, out)
}

type setPriceRequest struct {
	Price int64 `json:"price"`
}

// setPrice is the demo's price lever. The oracle refuses it unless it was
// started with manual publication on, which is where the real check lives.
func (s *Server) setPrice(c echo.Context) error {
	var request setPriceRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, `expected {"price": <number>}`)
	}

	if err := s.app.SetPrice(c.Request().Context(), request.Price); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

type stackResponse struct {
	ArkdSigner     string `json:"arkd_signer"`
	EmulatorSigner string `json:"emulator_signer"`
	ExitDelay      int64  `json:"exit_delay"`
	InBlocks       bool   `json:"exit_delay_in_blocks"`
	Dust           uint64 `json:"dust"`
}

// demoStack is what the operator and the emulator said about themselves. It is
// on screen so the demo can show it is talking to a real stack rather than to a
// mock: none of these numbers is one we chose.
func (s *Server) demoStack(c echo.Context) error {
	stack := s.app.Stack()

	return c.JSON(http.StatusOK, stackResponse{
		ArkdSigner:     hex.EncodeToString(stack.ArkdSigner.SerializeCompressed()),
		EmulatorSigner: hex.EncodeToString(stack.EmulatorSigner.SerializeCompressed()),
		ExitDelay:      int64(stack.ExitDelay.Value),
		InBlocks:       stack.AllowsBlockTimelocks,
		Dust:           stack.Dust,
	})
}
