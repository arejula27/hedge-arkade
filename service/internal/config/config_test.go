package config

import (
	"strings"
	"testing"
)

func TestLoadReadsTheEnvironment(t *testing.T) {
	setEnv(t, map[string]string{
		"PORT":        "9090",
		"APP_ENV":     "staging",
		"DB_HOST":     "db.internal",
		"DB_PORT":     "6543",
		"DB_NAME":     "hedge",
		"DB_USER":     "hedge",
		"DB_PASSWORD": "hunter2",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Port)
	}
	if cfg.Env != "staging" {
		t.Errorf("env = %q, want staging", cfg.Env)
	}
	want := "postgres://hedge:hunter2@db.internal:6543/hedge?search_path=public&sslmode=disable"
	if cfg.Database != want {
		t.Errorf("database =\n  %q\nwant\n  %q", cfg.Database, want)
	}
}

// A password with URL metacharacters in it is the reason the connection string
// is assembled rather than read whole.
func TestLoadEscapesThePassword(t *testing.T) {
	setEnv(t, map[string]string{
		"DB_HOST":     "localhost",
		"DB_NAME":     "hedge",
		"DB_USER":     "hedge",
		"DB_PASSWORD": "p@ss/word?x",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(cfg.Database, "p%40ss%2Fword%3Fx") {
		t.Errorf("password not escaped in %q", cfg.Database)
	}
}

func TestLoadRefusesAnIncompleteEnvironment(t *testing.T) {
	full := map[string]string{
		"DB_HOST":     "localhost",
		"DB_NAME":     "hedge",
		"DB_USER":     "hedge",
		"DB_PASSWORD": "hunter2",
	}

	for missing := range full {
		t.Run(missing, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range full {
				if k != missing {
					env[k] = v
				}
			}
			setEnv(t, env)

			if _, err := Load(); err == nil {
				t.Fatalf("Load succeeded without %s", missing)
			}
		})
	}
}

func TestLoadRefusesANonNumericPort(t *testing.T) {
	setEnv(t, map[string]string{
		"PORT":        "eight thousand",
		"DB_HOST":     "localhost",
		"DB_NAME":     "hedge",
		"DB_USER":     "hedge",
		"DB_PASSWORD": "hunter2",
	})

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a non-numeric port")
	}
}

// setEnv clears every variable Load reads, then sets the ones given, so a test
// never inherits a value from the developer's own shell or from another test.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()

	for _, name := range []string{
		"PORT", "APP_ENV",
		"DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD",
		"DB_SSLMODE", "DB_SCHEMA",
	} {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}
