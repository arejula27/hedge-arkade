//go:build integration

// These run against a real postgres in a throwaway container, so they need
// Docker. `just check` never touches them.
package postgres

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// dsn points at the container started in TestMain.
var dsn string

func mustStartPostgresContainer() (func(context.Context, ...testcontainers.TerminateOption) error, error) {
	var (
		dbName = "database"
		dbPwd  = "password"
		dbUser = "user"
	)

	container, err := postgres.Run(
		context.Background(),
		"postgres:latest",
		postgres.WithDatabase(dbName),
		postgres.WithUsername(dbUser),
		postgres.WithPassword(dbPwd),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		return nil, err
	}

	host, err := container.Host(context.Background())
	if err != nil {
		return container.Terminate, err
	}
	port, err := container.MappedPort(context.Background(), "5432/tcp")
	if err != nil {
		return container.Terminate, err
	}

	dsn = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		dbUser, dbPwd, host, port.Port(), dbName)

	return container.Terminate, nil
}

func TestMain(m *testing.M) {
	teardown, err := mustStartPostgresContainer()
	if err != nil {
		log.Fatalf("could not start postgres container: %v", err)
	}

	code := m.Run()

	if teardown != nil {
		if err := teardown(context.Background()); err != nil {
			log.Fatalf("could not teardown postgres container: %v", err)
		}
	}
	os.Exit(code)
}

func TestOpen(t *testing.T) {
	db, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db == nil {
		t.Fatal("Open returned nil")
	}
	defer db.Close()
}

func TestCheck(t *testing.T) {
	db, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	health := db.Check(context.Background())

	if !health.Up {
		t.Fatalf("expected the database to be up, got %q", health.Error)
	}
	if health.Error != "" {
		t.Fatalf("expected no error, got %q", health.Error)
	}
	if health.OpenConnections < 1 {
		t.Fatalf("expected at least one open connection, got %d", health.OpenConnections)
	}
}

func TestClose(t *testing.T) {
	db, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("expected Close to return nil, got %v", err)
	}
}

// sql.Open does not dial, so without the ping in Open a wrong address would
// only surface on the first request.
func TestOpenFailsOnAnAddressThatAnswersNothing(t *testing.T) {
	_, err := Open(context.Background(), "postgres://nobody:nothing@127.0.0.1:1/none?sslmode=disable")
	if err == nil {
		t.Fatal("Open succeeded against a port with nothing behind it")
	}
	if !strings.Contains(err.Error(), "connecting to the database") {
		t.Errorf("error = %v, want it to name the connection", err)
	}
}

// A health check exists to report a database that is down, so it must survive
// one rather than take the process with it.
func TestCheckSurvivesAClosedPool(t *testing.T) {
	db, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	health := db.Check(context.Background())
	if health.Up {
		t.Error("a closed pool reports itself up")
	}
	if health.Error == "" {
		t.Error("a closed pool reports no error")
	}
}
