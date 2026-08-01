package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// HelloWorldHandler is the demo endpoint the generated frontend calls from its
// "Fetch from Server" button. It is what proves the two halves are wired
// together, and it goes when the first real endpoint arrives.
func (s *Server) HelloWorldHandler(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"message": "Hello World"})
}
