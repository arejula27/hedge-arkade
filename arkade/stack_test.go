package arkade

import (
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
)

// arkd reads any delay below 512 as blocks and any above as seconds. Reading it
// the other way round builds a contract the operator will not accept, and the
// boundary is the only place the rule is visible.
func TestLocktimeReadsTheDelayTheWayArkdDoes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value int64
		want  arklib.RelativeLocktime
	}{
		{"one block", 1, arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 1}},
		{"the regtest exit delay", 5, arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 5}},
		{"the last block value", 511, arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 511}},
		{"the first second value", 512, arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: 512}},
		{"a day", 86400, arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: 86400}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Locktime(tc.value); got != tc.want {
				t.Errorf("Locktime(%d) = %+v, want %+v", tc.value, got, tc.want)
			}
		})
	}
}

func TestAllowsBlockTimelocksFollowsTheOperatorsOwnDelay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay int64
		want  bool
	}{
		{"a regtest operator counts blocks", 5, true},
		{"a production operator counts seconds", 1024, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Stack{ExitDelay: Locktime(tc.delay)}
			if got := s.AllowsBlockTimelocks(); got != tc.want {
				t.Errorf("AllowsBlockTimelocks() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultConfigPrefersTheEnvironment(t *testing.T) {
	t.Setenv("HEDGE_ARKD_URL", "arkd.internal:7070")
	t.Setenv("HEDGE_EMULATOR_URL", "")
	t.Setenv("HEDGE_EXPLORER_URL", "http://explorer.internal/api")

	cfg := DefaultConfig()

	if cfg.ArkdURL != "arkd.internal:7070" {
		t.Errorf("ArkdURL = %q", cfg.ArkdURL)
	}
	if cfg.EmulatorURL != "localhost:7073" {
		t.Errorf("an empty variable should fall back, got %q", cfg.EmulatorURL)
	}
	if cfg.ExplorerURL != "http://explorer.internal/api" {
		t.Errorf("ExplorerURL = %q", cfg.ExplorerURL)
	}
	if cfg.Network != arklib.BitcoinRegTest {
		t.Errorf("Network = %+v", cfg.Network)
	}
}

func TestParseKeyRefusesWhatIsNotAKey(t *testing.T) {
	for _, tc := range []struct{ name, hexKey string }{
		{"empty", ""},
		{"not hex", "zzzz"},
		{"hex but too short", "0201"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseKey(tc.hexKey); err == nil {
				t.Errorf("ParseKey(%q) accepted it", tc.hexKey)
			}
		})
	}
}
