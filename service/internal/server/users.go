package server

import (
	"encoding/hex"
	"net/http"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/labstack/echo/v4"
)

type userResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	PubKey string `json:"pubkey"`
}

func asUser(u domain.User) userResponse {
	return userResponse{
		ID:     u.ID.String(),
		Name:   u.Name,
		PubKey: hex.EncodeToString(u.PublicKey),
	}
}

type createUserRequest struct {
	Name string `json:"name"`
}

func (s *Server) createUser(c echo.Context) error {
	var request createUserRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, `expected {"name": "..."}`)
	}

	u, err := s.app.CreateUser(c.Request().Context(), request.Name)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, asUser(u))
}

func (s *Server) listUsers(c echo.Context) error {
	users, err := s.app.Users(c.Request().Context())
	if err != nil {
		return err
	}

	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		out = append(out, asUser(u))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) me(c echo.Context) error {
	u, err := s.app.User(c.Request().Context(), caller(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, asUser(u))
}

type walletResponse struct {
	OffchainAddress string `json:"offchain_address"`
	BoardingAddress string `json:"boarding_address"`
	SpendableSats   int64  `json:"spendable_sats"`
	// RecoverableSats is money that is there and cannot be spent offchain
	// until it has been back through a batch.
	RecoverableSats int64 `json:"recoverable_sats"`
}

func (s *Server) wallet(c echo.Context) error {
	w, err := s.app.Wallet(c.Request().Context(), caller(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, walletResponse{
		OffchainAddress: w.OffchainAddress,
		BoardingAddress: w.BoardingAddress,
		SpendableSats:   w.SpendableSats,
		RecoverableSats: w.RecoverableSats,
	})
}

// recoverWallet puts swept VTXOs back into spendable ones. It takes a batch, so
// it is as slow as boarding.
func (s *Server) recoverWallet(c echo.Context) error {
	if err := s.app.Recover(c.Request().Context(), caller(c)); err != nil {
		return err
	}
	return c.NoContent(http.StatusAccepted)
}

type fundWalletRequest struct {
	Sats int64 `json:"sats"`
}

// fundWallet boards sats from the regtest faucet.
//
// It takes tens of seconds — a faucet payment to confirm, then a batch to close
// — so it answers 202 and the caller watches their balance.
func (s *Server) fundWallet(c echo.Context) error {
	var request fundWalletRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, `expected {"sats": <number>}`)
	}

	if err := s.app.TopUp(c.Request().Context(), caller(c), request.Sats); err != nil {
		return err
	}
	return c.NoContent(http.StatusAccepted)
}
