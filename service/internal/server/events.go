package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type streamEvent struct {
	Contract string `json:"contract"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
}

// heartbeat keeps proxies and browsers from deciding an idle stream is a dead
// one. A contract can sit in `active` for a long time with nothing to say.
const heartbeat = 20 * time.Second

// events streams contract transitions.
//
// This is what makes the demo readable: the tab that did not click watches the
// contract change state at the same moment as the one that did. Everything else
// the UI needs is cheap enough to poll.
//
// ?contract=<id> narrows it to one, which is what the contract page wants.
func (s *Server) events(c echo.Context) error {
	var only uuid.UUID
	if raw := c.QueryParam("contract"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "contract is not a contract id")
		}
		only = id
	}

	response := c.Response()
	response.Header().Set(echo.HeaderContentType, "text/event-stream")
	response.Header().Set(echo.HeaderCacheControl, "no-cache")
	response.Header().Set("Connection", "keep-alive")
	response.WriteHeader(http.StatusOK)
	response.Flush()

	feed, unsubscribe := s.broker.Subscribe()
	defer unsubscribe()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil

		case <-ticker.C:
			if _, err := response.Write([]byte(": still here\n\n")); err != nil {
				return nil
			}
			response.Flush()

		case event, open := <-feed:
			if !open {
				return nil
			}
			if only != uuid.Nil && event.Contract != only {
				continue
			}

			body, err := json.Marshal(streamEvent{
				Contract: event.Contract.String(),
				State:    string(event.State),
				Detail:   event.Detail,
			})
			if err != nil {
				continue
			}

			if _, err := response.Write([]byte("data: " + string(body) + "\n\n")); err != nil {
				return nil
			}
			response.Flush()
		}
	}
}
