package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Routes returns the whole HTTP surface as a plain http.Handler, which is what
// keeps echo out of this package's exported API.
func (s *Server) Routes(allowOrigin string) http.Handler {
	if allowOrigin == "" {
		allowOrigin = "http://localhost:5173"
	}

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = s.errorHandler

	e.Use(middleware.Recover())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{allowOrigin},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders: []string{"Accept", "Authorization", "Content-Type", userHeader},
		MaxAge:       300,
	}))

	e.GET("/health", s.health)

	api := e.Group("/api")

	api.POST("/users", s.createUser)
	api.GET("/users", s.listUsers)
	api.GET("/me", s.me, s.identified)

	api.GET("/wallet", s.wallet, s.identified)
	api.POST("/wallet/fund", s.fundWallet, s.identified)
	api.POST("/wallet/recover", s.recoverWallet, s.identified)

	api.POST("/contracts", s.propose, s.identified)
	api.GET("/contracts", s.listContracts)
	api.GET("/contracts/:id", s.showContract)
	api.POST("/contracts/:id/accept", s.accept, s.identified)
	api.POST("/contracts/:id/cancel", s.cancel, s.identified)
	api.POST("/contracts/:id/fund", s.fundContract, s.identified)

	// Settling needs no identity. The settlement leaf carries no party key,
	// which is the point: a contract that has liquidated must not need the
	// losing side to cooperate.
	api.POST("/contracts/:id/settle", s.settle)

	// Closing early is leaf 2: no oracle, no covenant, and the two signatures
	// are the whole of the authority.
	api.GET("/contracts/:id/redemption", s.showRedemption)
	api.POST("/contracts/:id/redemption", s.proposeRedemption, s.identified)
	api.POST("/contracts/:id/redemption/sign", s.signRedemption, s.identified)
	api.POST("/contracts/:id/redemption/reject", s.rejectRedemption, s.identified)

	// The oracle is proxied so the frontend has one origin, and so the UI never
	// has to know it is a separate process.
	api.GET("/oracle", s.oracle)
	api.GET("/oracle/history", s.oracleHistory)
	api.POST("/oracle/price", s.setPrice)

	api.GET("/demo/stack", s.demoStack)
	api.GET("/events", s.events)

	return e
}
