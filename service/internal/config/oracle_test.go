package config

import (
	"strings"
	"testing"
	"time"
)

// The database half is shared with Load and covered there; what these check is
// the oracle's own five.
func oracleEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"DB_HOST":     "db.internal",
		"DB_NAME":     "hedge",
		"DB_USER":     "hedge",
		"DB_PASSWORD": "hunter2",
		"ORACLE_SEED": strings.Repeat("11", 32),
	}
	for name, value := range extra {
		env[name] = value
	}
	return env
}

func TestLoadOracleDefaults(t *testing.T) {
	setEnv(t, oracleEnv(nil))

	cfg, err := LoadOracle()
	if err != nil {
		t.Fatalf("LoadOracle: %v", err)
	}

	if cfg.Port != 8081 {
		t.Errorf("Port = %d, want 8081", cfg.Port)
	}
	if cfg.Interval != 5*time.Second {
		t.Errorf("Interval = %s, want 5s", cfg.Interval)
	}
	if cfg.StartPrice != 10_000_000 {
		t.Errorf("StartPrice = %d, want 10000000", cfg.StartPrice)
	}
	if cfg.AllowManual {
		t.Error("AllowManual defaults to true; taking prices over HTTP is opt-in")
	}
}

func TestLoadOracleReadsTheEnvironment(t *testing.T) {
	setEnv(t, oracleEnv(map[string]string{
		"ORACLE_PORT":             "9000",
		"ORACLE_INTERVAL_SECONDS": "30",
		"ORACLE_START_PRICE":      "6500000",
		"ORACLE_ALLOW_MANUAL":     "true",
	}))

	cfg, err := LoadOracle()
	if err != nil {
		t.Fatalf("LoadOracle: %v", err)
	}

	if cfg.Port != 9000 {
		t.Errorf("Port = %d", cfg.Port)
	}
	if cfg.Interval != 30*time.Second {
		t.Errorf("Interval = %s", cfg.Interval)
	}
	if cfg.StartPrice != 6_500_000 {
		t.Errorf("StartPrice = %d", cfg.StartPrice)
	}
	if !cfg.AllowManual {
		t.Error("AllowManual = false")
	}
}

// The public half of this key is baked into every contract's taproot address, so
// a seed that only fails when the first price is signed is a service that starts
// and then cannot do its one job.
func TestLoadOracleRefusesASeedThatIsNotAKey(t *testing.T) {
	for _, tc := range []struct{ name, seed string }{
		{"missing", ""},
		{"not hex", strings.Repeat("zz", 32)},
		{"too short", strings.Repeat("11", 31)},
		{"too long", strings.Repeat("11", 33)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := oracleEnv(nil)
			env["ORACLE_SEED"] = tc.seed
			setEnv(t, env)

			if _, err := LoadOracle(); err == nil {
				t.Error("LoadOracle accepted it")
			}
		})
	}
}

func TestLoadOracleRefusesValuesThatMakeNoSense(t *testing.T) {
	for _, tc := range []struct{ name, variable, value string }{
		{"a port that is not a number", "ORACLE_PORT", "eight-thousand"},
		{"an interval of zero", "ORACLE_INTERVAL_SECONDS", "0"},
		{"a negative interval", "ORACLE_INTERVAL_SECONDS", "-5"},
		{"an interval that is not a number", "ORACLE_INTERVAL_SECONDS", "often"},
		{"a start price of zero", "ORACLE_START_PRICE", "0"},
		{"a negative start price", "ORACLE_START_PRICE", "-1"},
		{"a manual flag that is not a bool", "ORACLE_ALLOW_MANUAL", "sometimes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, oracleEnv(map[string]string{tc.variable: tc.value}))

			if _, err := LoadOracle(); err == nil {
				t.Errorf("LoadOracle accepted %s=%q", tc.variable, tc.value)
			}
		})
	}
}

func TestLoadOracleNeedsADatabase(t *testing.T) {
	full := oracleEnv(nil)

	for missing := range full {
		if !strings.HasPrefix(missing, "DB_") {
			continue
		}
		t.Run(missing, func(t *testing.T) {
			env := oracleEnv(nil)
			delete(env, missing)
			setEnv(t, env)

			if _, err := LoadOracle(); err == nil {
				t.Errorf("LoadOracle ran without %s", missing)
			}
		})
	}
}
