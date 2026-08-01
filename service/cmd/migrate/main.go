// Command migrate brings the schema up to date.
//
// It is its own binary because two services share one database, and two
// processes running migrations at startup race: one creates the tables while
// the other reads a schema that is half there. Neither the API nor the oracle
// migrates, so there is exactly one thing that owns the schema and it is run
// before either of them.
package main

import (
	"context"
	"log"

	"github.com/arejula27/hedge/service/internal/config"
	"github.com/arejula27/hedge/service/internal/postgres"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()

	db, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := postgres.Migrate(ctx, db); err != nil {
		return err
	}

	log.Println("the schema is up to date")
	return nil
}
