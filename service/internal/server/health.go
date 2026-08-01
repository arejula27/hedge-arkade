package server

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/arejula27/hedge/service/internal/postgres"
)

// prober is what the readiness probe needs from the database. Taking an
// interface here rather than the concrete pool is what lets the handler be
// tested without a database running.
type prober interface {
	Check(ctx context.Context) postgres.Health
}

type healthResponse struct {
	Status   string          `json:"status"`
	Database postgres.Health `json:"database"`
}

// health reports 503 when a dependency is down, so a load balancer can act on
// the status code alone.
func (s *Server) health(c echo.Context) error {
	db := s.db.Check(c.Request().Context())

	status, code := "ok", http.StatusOK
	if !db.Up {
		status, code = "degraded", http.StatusServiceUnavailable
	}

	return c.JSON(code, healthResponse{Status: status, Database: db})
}
