package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Routes returns the whole HTTP surface as a plain http.Handler, which is what
// keeps echo out of this package's exported API.
func (s *Server) Routes() http.Handler {
	e := echo.New()
	e.HideBanner = true

	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"http://localhost:5173"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders: []string{"Accept", "Authorization", "Content-Type"},
		MaxAge:       300,
	}))

	e.GET("/", s.HelloWorldHandler)
	e.GET("/health", s.health)

	return e
}
