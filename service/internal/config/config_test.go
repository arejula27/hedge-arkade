package config

import (
	"strings"
	"testing"
)

func TestLoadReadsTheEnvironment(t *testing.T) {
	setEnv(t, map[string]string{
		"PORT":         "9090",
		"APP_ENV":      "staging",
		"DB_HOST":      "db.internal",
		"DB_PORT":      "6543",
		"DB_NAME":      "hedge",
		"DB_USER":      "hedge",
		"DB_PASSWORD":  "hunter2",
		"SERVICE_SEED": strings.Repeat("26", 32),
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
		"DB_HOST":      "localhost",
		"DB_NAME":      "hedge",
		"DB_USER":      "hedge",
		"DB_PASSWORD":  "p@ss/word?x",
		"SERVICE_SEED": strings.Repeat("26", 32),
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
		"DB_HOST":      "localhost",
		"DB_NAME":      "hedge",
		"DB_USER":      "hedge",
		"DB_PASSWORD":  "hunter2",
		"SERVICE_SEED": strings.Repeat("26", 32),
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
		"PORT":         "eight thousand",
		"DB_HOST":      "localhost",
		"DB_NAME":      "hedge",
		"DB_USER":      "hedge",
		"DB_PASSWORD":  "hunter2",
		"SERVICE_SEED": strings.Repeat("26", 32),
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
		"ORACLE_URL", "SERVICE_SEED", "REGTEST_SCRIPT",
		"ORACLE_PORT", "ORACLE_INTERVAL_SECONDS", "ORACLE_SEED",
		"ORACLE_START_PRICE", "ORACLE_ALLOW_MANUAL",
	} {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

// The service's key is baked into every pre-signed exit, so a mistyped one that
// only fails when a party needs to leave is a service that starts and then
// cannot honour what it promised.
func TestLoadRefusesAServiceSeedThatIsNotAKey(t *testing.T) {
	for _, tc := range []struct{ name, seed string }{
		{"not hex", strings.Repeat("zz", 32)},
		{"too short", strings.Repeat("26", 31)},
		{"too long", strings.Repeat("26", 33)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, map[string]string{
				"DB_HOST":      "localhost",
				"DB_NAME":      "hedge",
				"DB_USER":      "hedge",
				"DB_PASSWORD":  "hunter2",
				"SERVICE_SEED": tc.seed,
			})

			if _, err := Load(); err == nil {
				t.Error("Load accepted it")
			}
		})
	}
}

func TestLoadDefaultsTheOracleAndTheFaucet(t *testing.T) {
	setEnv(t, map[string]string{
		"DB_HOST":      "localhost",
		"DB_NAME":      "hedge",
		"DB_USER":      "hedge",
		"DB_PASSWORD":  "hunter2",
		"SERVICE_SEED": strings.Repeat("26", 32),
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Oracle != "http://localhost:8081" {
		t.Errorf("Oracle = %q", cfg.Oracle)
	}
	if cfg.RegtestScript != "../scripts/regtest.sh" {
		t.Errorf("RegtestScript = %q", cfg.RegtestScript)
	}
}
