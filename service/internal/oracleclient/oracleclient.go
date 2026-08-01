// Package oracleclient reaches the oracle over HTTP.
//
// The oracle is a separate process that knows about no contract, so the service
// talks to it the way anyone else would. Nothing here parses the 24-byte
// message: the signed bytes travel whole, from the oracle that made them to the
// covenant that checks them.
package oracleclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/domain"
)

type Client struct {
	base string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		base: baseURL,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

type publication struct {
	Sequence  uint64 `json:"sequence"`
	Timestamp int64  `json:"timestamp"`
	Price     int64  `json:"price"`
	Message   string `json:"message"`
	Signature string `json:"signature"`
}

func (p publication) price() domain.Price {
	return domain.Price{Sequence: p.Sequence, Timestamp: p.Timestamp, Price: p.Price}
}

func (p publication) signed() (arkade.SignedPrice, error) {
	message, err := hex.DecodeString(p.Message)
	if err != nil {
		return arkade.SignedPrice{}, fmt.Errorf("the message is not hex: %w", err)
	}
	signature, err := hex.DecodeString(p.Signature)
	if err != nil {
		return arkade.SignedPrice{}, fmt.Errorf("the signature is not hex: %w", err)
	}
	return arkade.SignedPrice{Message: message, Signature: signature}, nil
}

func (c *Client) PubKey(ctx context.Context) ([]byte, error) {
	var body struct {
		PubKey string `json:"pubkey"`
	}
	if err := c.get(ctx, "/oracle/info", &body); err != nil {
		return nil, err
	}

	key, err := hex.DecodeString(body.PubKey)
	if err != nil {
		return nil, fmt.Errorf("the oracle's key is not hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("the oracle's key is %d bytes, want 32", len(key))
	}
	return key, nil
}

func (c *Client) Latest(ctx context.Context) (domain.Price, error) {
	var body publication
	if err := c.get(ctx, "/oracle/latest", &body); err != nil {
		return domain.Price{}, err
	}
	return body.price(), nil
}

func (c *Client) Pair(ctx context.Context) (app.Pair, error) {
	var body struct {
		Settlement publication `json:"settlement"`
		Previous   publication `json:"previous"`
	}
	if err := c.get(ctx, "/oracle/pair", &body); err != nil {
		return app.Pair{}, err
	}

	settlement, err := body.Settlement.signed()
	if err != nil {
		return app.Pair{}, err
	}
	previous, err := body.Previous.signed()
	if err != nil {
		return app.Pair{}, err
	}

	// The oracle promises adjacency; checking it here costs one comparison and
	// turns a covenant failure nobody can read into a plain sentence.
	if body.Settlement.Sequence != body.Previous.Sequence+1 {
		return app.Pair{}, fmt.Errorf(
			"the oracle returned sequences %d and %d, which are not adjacent",
			body.Settlement.Sequence, body.Previous.Sequence)
	}

	return app.Pair{Settlement: settlement, Previous: previous}, nil
}

func (c *Client) History(ctx context.Context, limit int) ([]domain.Price, error) {
	var body []publication
	if err := c.get(ctx, fmt.Sprintf("/oracle/history?limit=%d", limit), &body); err != nil {
		return nil, err
	}

	prices := make([]domain.Price, 0, len(body))
	for _, p := range body {
		prices = append(prices, p.price())
	}
	return prices, nil
}

func (c *Client) SetPrice(ctx context.Context, price int64) error {
	body, err := json.Marshal(map[string]int64{"price": price})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/oracle/price", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the oracle: %w", err)
	}
	defer resp.Body.Close()

	return statusError(resp)
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the oracle: %w", err)
	}
	defer resp.Body.Close()

	if err := statusError(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	return nil
}

// statusError turns a 404 into domain.ErrNotFound, so an oracle that has not
// published enough yet reads the same as any other missing thing.
func statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return domain.ErrNotFound
	case resp.StatusCode >= 400:
		var body struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Message == "" {
			body.Message = resp.Status
		}
		return fmt.Errorf("the oracle refused: %s", body.Message)
	}
	return nil
}
