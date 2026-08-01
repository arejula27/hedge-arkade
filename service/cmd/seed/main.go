// Command seed fills a fresh demo with two people who already have money.
//
// Boarding is the slow part of the demo and the least interesting: a faucet
// payment to confirm, then a batch to close, twice. Doing it here means that by
// the time the browser is up there is nothing to wait for and the first thing
// anyone does is open a contract.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/boot"
	"github.com/arejula27/hedge/service/internal/config"
	"github.com/google/uuid"
)

// boardedSats is what each person gets: enough to fund the standard position
// twice over and still leave change well above dust.
const boardedSats = 50_000_000

func main() {
	names := flag.String("names", "alice,bob", "the people to create, comma separated")
	sats := flag.Int64("sats", boardedSats, "what to board for each of them")
	flag.Parse()

	if err := run(strings.Split(*names, ","), *sats); err != nil {
		log.Fatal(err)
	}
}

func run(names []string, sats int64) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()

	service, err := boot.Wire(ctx, cfg, boot.Options{})
	if err != nil {
		return err
	}
	defer service.Close()

	existing, err := service.App.Users(ctx)
	if err != nil {
		return err
	}
	byName := map[string]uuid.UUID{}
	for _, u := range existing {
		byName[u.Name] = u.ID
	}

	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}

		id, known := byName[name]
		if !known {
			user, err := service.App.CreateUser(ctx, name)
			if err != nil {
				return err
			}
			id = user.ID
			log.Printf("%s: created", name)
		}

		if err := board(ctx, service.App, id, name, sats); err != nil {
			return err
		}
	}

	log.Println("the demo is ready")
	return nil
}

// board tops a person up unless they already have enough, so running this twice
// costs nothing.
func board(ctx context.Context, a *app.App, id uuid.UUID, name string, sats int64) error {
	wallet, err := a.Wallet(ctx, id)
	if err != nil {
		return err
	}
	if wallet.SpendableSats >= sats {
		log.Printf("%s: already has %s", name, satsOf(wallet))
		return nil
	}

	log.Printf("%s: boarding %d sats, which takes a minute", name, sats)
	if err := a.TopUp(ctx, id, sats); err != nil {
		return err
	}

	if wallet, err = a.Wallet(ctx, id); err != nil {
		return err
	}
	log.Printf("%s: %s", name, satsOf(wallet))
	return nil
}

func satsOf(w app.Wallet) string {
	if w.RecoverableSats == 0 {
		return fmt.Sprintf("%d sats", w.SpendableSats)
	}
	return fmt.Sprintf("%d sats, and %d waiting on a batch to recover",
		w.SpendableSats, w.RecoverableSats)
}
