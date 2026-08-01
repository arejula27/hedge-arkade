package server

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/domain"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type partyResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// termsResponse carries the parameters structured rather than only the address.
//
// A client is supposed to recognise the contract it is about to fund, not take
// the service's word for it: everything the taproot address is a function of is
// here, so the address below can be re-derived and compared.
type termsResponse struct {
	HedgeValueCents      int64 `json:"hedge_value_cents"`
	PayoutSats           int64 `json:"payout_sats"`
	LowLiquidationCents  int64 `json:"low_liquidation_cents"`
	HighLiquidationCents int64 `json:"high_liquidation_cents"`
	StartTimestamp       int64 `json:"start_timestamp"`
	MaturityTimestamp    int64 `json:"maturity_timestamp"`

	OraclePubKey    string `json:"oracle_pubkey"`
	ShortLockScript string `json:"short_lock_script,omitempty"`
	LongLockScript  string `json:"long_lock_script,omitempty"`

	ShortKey       string `json:"short_key,omitempty"`
	LongKey        string `json:"long_key,omitempty"`
	ArkdSigner     string `json:"arkd_signer"`
	EmulatorSigner string `json:"emulator_signer"`

	ExitDelay              int64 `json:"exit_delay"`
	ExitDelayInBlocks      bool  `json:"exit_delay_in_blocks"`
	EnableMutualRedemption bool  `json:"enable_mutual_redemption"`
}

type outpointResponse struct {
	Txid string `json:"txid"`
	Vout uint32 `json:"vout"`
}

type projectionResponse struct {
	Price      int64 `json:"price"`
	ShortSats  int64 `json:"short_sats"`
	LongSats   int64 `json:"long_sats"`
	Liquidated bool  `json:"liquidated"`
	Matured    bool  `json:"matured"`
}

type eventResponse struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Detail string `json:"detail"`
}

type contractResponse struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Creator string `json:"creator"`

	// Address is empty until both sides are known: it is a function of both
	// payout scripts, and only one of them exists while the contract is on
	// offer.
	Address  string `json:"address,omitempty"`
	PkScript string `json:"pk_script,omitempty"`

	Short *partyResponse `json:"short"`
	Long  *partyResponse `json:"long"`

	Terms termsResponse `json:"terms"`

	ShortStake int64 `json:"short_stake"`
	LongStake  int64 `json:"long_stake"`

	Funding   *outpointResponse `json:"funding,omitempty"`
	ExitReady bool              `json:"exit_ready"`

	Projection  *projectionResponse  `json:"projection,omitempty"`
	Redemption  *redemptionResponse  `json:"redemption,omitempty"`
	Arbitration *arbitrationResponse `json:"arbitration,omitempty"`
	Events      []eventResponse      `json:"events,omitempty"`
}

// view renders a contract. names resolves a user id to a person; there are two
// of them in a demo, so they are all loaded once rather than joined.
func (s *Server) view(c *domain.Contract, names map[uuid.UUID]string) contractResponse {
	out := contractResponse{
		ID:      c.ID.String(),
		State:   string(c.State),
		Creator: string(c.Creator),
		Terms: termsResponse{
			HedgeValueCents:      domain.HedgeValueCents(c.Terms.NominalUnitsXSatsPerBtc),
			PayoutSats:           c.Terms.PayoutSats,
			LowLiquidationCents:  c.Terms.LowLiquidationPrice,
			HighLiquidationCents: c.Terms.HighLiquidationPrice,
			StartTimestamp:       c.Terms.StartTimestamp,
			MaturityTimestamp:    c.Terms.MaturityTimestamp,

			OraclePubKey:    hex.EncodeToString(c.Terms.OraclePubKey),
			ShortLockScript: hex.EncodeToString(c.Terms.ShortLockScript),
			LongLockScript:  hex.EncodeToString(c.Terms.LongLockScript),

			ShortKey:       hex.EncodeToString(c.ShortKey),
			LongKey:        hex.EncodeToString(c.LongKey),
			ArkdSigner:     hex.EncodeToString(c.ArkdSigner),
			EmulatorSigner: hex.EncodeToString(c.EmulatorSigner),

			ExitDelay:              int64(c.ExitDelay.Value),
			ExitDelayInBlocks:      c.ExitDelay.Type == arklib.LocktimeTypeBlock,
			EnableMutualRedemption: c.EnableMutualRedemption,
		},
		ShortStake: c.ShortStake,
		LongStake:  c.LongStake,
	}

	if c.ShortUser != nil {
		out.Short = &partyResponse{ID: c.ShortUser.String(), Name: names[*c.ShortUser]}
	}
	if c.LongUser != nil {
		out.Long = &partyResponse{ID: c.LongUser.String(), Name: names[*c.LongUser]}
	}
	if len(c.PkScript) > 0 {
		out.PkScript = hex.EncodeToString(c.PkScript)
		if address, err := c.Address(s.params); err == nil {
			out.Address = address
		}
	}
	if c.Funding != nil {
		out.Funding = &outpointResponse{Txid: c.Funding.Txid, Vout: c.Funding.Vout}
	}

	return out
}

func (s *Server) names(ctx context.Context) (map[uuid.UUID]string, error) {
	users, err := s.app.Users(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[uuid.UUID]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Name
	}
	return names, nil
}

type proposeRequest struct {
	Side                   string `json:"side"`
	HedgeValueCents        int64  `json:"hedge_value_cents"`
	PayoutSats             int64  `json:"payout_sats"`
	LowLiquidationCents    int64  `json:"low_liquidation_cents"`
	HighLiquidationCents   int64  `json:"high_liquidation_cents"`
	MaturityInSeconds      int64  `json:"maturity_in_seconds"`
	EnableMutualRedemption bool   `json:"enable_mutual_redemption"`
}

func (s *Server) propose(c echo.Context) error {
	var request proposeRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "that is not a proposal")
	}

	ctx := c.Request().Context()
	contract, err := s.app.Propose(ctx, app.Proposal{
		Creator:                caller(c),
		Side:                   domain.Side(request.Side),
		HedgeValueCents:        request.HedgeValueCents,
		PayoutSats:             request.PayoutSats,
		LowLiquidationCents:    request.LowLiquidationCents,
		HighLiquidationCents:   request.HighLiquidationCents,
		MaturityIn:             time.Duration(request.MaturityInSeconds) * time.Second,
		EnableMutualRedemption: request.EnableMutualRedemption,
	})
	if err != nil {
		return err
	}

	names, err := s.names(ctx)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, s.view(contract, names))
}

func (s *Server) listContracts(c echo.Context) error {
	ctx := c.Request().Context()

	filter := domain.ContractFilter{
		State: domain.State(c.QueryParam("state")),
		Open:  c.QueryParam("open") == "true",
	}
	if raw := c.QueryParam("user"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "user is not a user id")
		}
		filter.User = &id
	}
	if filter.State != "" && !filter.State.Valid() {
		return echo.NewHTTPError(http.StatusBadRequest, "there is no such state")
	}

	contracts, err := s.app.Contracts(ctx, filter)
	if err != nil {
		return err
	}
	names, err := s.names(ctx)
	if err != nil {
		return err
	}

	out := make([]contractResponse, 0, len(contracts))
	for _, contract := range contracts {
		out = append(out, s.view(contract, names))
	}
	return c.JSON(http.StatusOK, out)
}

// showContract is the whole of what the contract page needs: the contract, its
// history, what it would pay right now, and whether the exit is in place.
func (s *Server) showContract(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	contract, err := s.app.Contract(ctx, id)
	if err != nil {
		return err
	}
	names, err := s.names(ctx)
	if err != nil {
		return err
	}

	out := s.view(contract, names)

	events, err := s.app.History(ctx, id)
	if err != nil {
		return err
	}
	out.Events = make([]eventResponse, 0, len(events))
	for _, e := range events {
		out.Events = append(out.Events, eventResponse{
			From: string(e.From), To: string(e.To), Detail: e.Detail,
		})
	}

	// A projection needs both payout scripts, so it only exists once someone
	// has accepted.
	if len(contract.PkScript) > 0 {
		if projection, err := s.app.Project(ctx, contract); err == nil {
			out.Projection = &projectionResponse{
				Price:      projection.Price,
				ShortSats:  projection.ShortSats,
				LongSats:   projection.LongSats,
				Liquidated: projection.Liquidated,
				Matured:    projection.Matured,
			}
		}
	}

	if _, err := s.app.ExitPackage(ctx, id); err == nil {
		out.ExitReady = true
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}

	if proposal, err := s.app.Redemption(ctx, id); err == nil {
		view := asRedemption(proposal)
		out.Redemption = &view
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}

	if proposal, err := s.app.Arbitration(ctx, id); err == nil {
		view := asArbitration(proposal)
		out.Arbitration = &view
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}

	return c.JSON(http.StatusOK, out)
}

func (s *Server) accept(c echo.Context) error {
	return s.act(c, s.app.Accept)
}

func (s *Server) cancel(c echo.Context) error {
	return s.act(c, s.app.Cancel)
}

func (s *Server) fundContract(c echo.Context) error {
	return s.act(c, s.app.Fund)
}

// act runs one of the use cases that take a contract and a caller, and answers
// with the contract as it now stands.
func (s *Server) act(
	c echo.Context,
	use func(ctx context.Context, id, who uuid.UUID) (*domain.Contract, error),
) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	contract, err := use(ctx, id, caller(c))
	if err != nil {
		return err
	}
	names, err := s.names(ctx)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s.view(contract, names))
}

func (s *Server) settle(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	contract, err := s.app.Settle(ctx, id)
	if err != nil {
		return err
	}
	names, err := s.names(ctx)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s.view(contract, names))
}
