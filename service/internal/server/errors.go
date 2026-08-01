package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/labstack/echo/v4"
)

// errorResponse is what every failure looks like on the wire, so the frontend
// has one shape to render.
type errorResponse struct {
	Message string `json:"message"`
}

// errorHandler is the one place a domain error becomes a status code.
//
// Doing it here rather than in each handler is what stops the mapping drifting:
// there is exactly one answer to "what does a lost race look like", and every
// endpoint gives it.
func (s *Server) errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	code, message := statusOf(err)

	// A 500 tells the caller nothing, on purpose. So it has to tell us, or it
	// is a failure nobody can debug.
	if code >= http.StatusInternalServerError {
		log.Printf("%s %s: %v", c.Request().Method, c.Request().URL.Path, err)
	}

	if err := c.JSON(code, errorResponse{Message: message}); err != nil {
		log.Printf("writing the error response: %v", err)
	}
}

func statusOf(err error) (int, string) {
	var echoErr *echo.HTTPError
	if errors.As(err, &echoErr) {
		message, ok := echoErr.Message.(string)
		if !ok {
			message = http.StatusText(echoErr.Code)
		}
		return echoErr.Code, message
	}

	var notYet app.ErrNotYet
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, domain.ErrNameTaken):
		return http.StatusConflict, err.Error()
	case errors.Is(err, app.ErrNotAllowed):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, app.ErrInvalid):
		return http.StatusBadRequest, err.Error()

	// Both ways of losing a race read the same to a caller: come back with a
	// fresh copy. So do the steps a contract is not ready for.
	case domain.Lost(err):
		return http.StatusConflict, err.Error()
	case errors.As(err, &notYet):
		return http.StatusConflict, notYet.Reason

	// Anything else is ours, and the caller gets no detail about it.
	default:
		return http.StatusInternalServerError, "something went wrong"
	}
}
