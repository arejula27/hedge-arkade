// Package config reads the process environment once, at startup, into typed
// values.
//
// Nothing below the composition root calls os.Getenv. A package that reads its
// own configuration cannot be constructed twice with different settings, which
// is what makes it untestable.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"

	_ "github.com/joho/godotenv/autoload"
)

// Config is the whole of what the service reads from its environment.
type Config struct {
	// Port the HTTP server listens on.
	Port int

	// Env is "local", "staging" or "production". It only decides how loud the
	// logs are and whether errors are returned in full.
	Env string

	// Database is a libpq connection string.
	Database string
}

// Load reads the environment. It fails rather than defaulting, except for the
// two values that have a safe default.
func Load() (Config, error) {
	port, err := intVar("PORT", 8080)
	if err != nil {
		return Config{}, err
	}

	db, err := databaseURL()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Port:     port,
		Env:      stringVar("APP_ENV", "local"),
		Database: db,
	}, nil
}

// databaseURL assembles the connection string from its parts, so that the
// password never has to be pasted into a URL by hand and percent-escaped
// correctly by whoever writes the .env.
func databaseURL() (string, error) {
	host, err := required("DB_HOST")
	if err != nil {
		return "", err
	}
	name, err := required("DB_NAME")
	if err != nil {
		return "", err
	}
	user, err := required("DB_USER")
	if err != nil {
		return "", err
	}
	password, err := required("DB_PASSWORD")
	if err != nil {
		return "", err
	}
	port, err := intVar("DB_PORT", 5432)
	if err != nil {
		return "", err
	}

	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   name,
	}
	q := url.Values{}
	q.Set("sslmode", stringVar("DB_SSLMODE", "disable"))
	q.Set("search_path", stringVar("DB_SCHEMA", "public"))
	u.RawQuery = q.Encode()

	return u.String(), nil
}

func required(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s is not set", name)
	}
	return v, nil
}

func stringVar(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func intVar(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", name, raw)
	}
	return v, nil
}
