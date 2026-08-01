package server

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// userHeader carries who is asking.
//
// A header and not a cookie, on purpose: a cookie is shared by every tab of an
// origin, and the demo is two people in two tabs of the same browser. There is
// no password behind it — a demo where the two participants are two tabs has
// nothing to protect, and an authentication story here would only get in the
// way of the one being told.
const userHeader = "X-Hedge-User"

const userKey = "hedge.user"

// identified refuses a request that does not say who it is from.
func (s *Server) identified(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		raw := c.Request().Header.Get(userHeader)
		if raw == "" {
			return echo.NewHTTPError(http.StatusUnauthorized, "no "+userHeader+" header")
		}

		id, err := uuid.Parse(raw)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnauthorized, userHeader+" is not a user id")
		}

		c.Set(userKey, id)
		return next(c)
	}
}

// caller is who the request is from. It is only ever called from a handler the
// identified middleware wraps.
func caller(c echo.Context) uuid.UUID {
	id, _ := c.Get(userKey).(uuid.UUID)
	return id
}

// pathID reads a :id parameter.
func pathID(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, echo.NewHTTPError(http.StatusBadRequest, "that is not a contract id")
	}
	return id, nil
}
