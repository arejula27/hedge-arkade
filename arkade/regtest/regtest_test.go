package regtest

import (
	"path/filepath"
	"testing"
)

// The script resolves .regtest against its own working directory, so it has to
// run from the repo root whatever directory the caller is in. Getting this
// wrong surfaces as a faucet that refuses, several layers from the cause.
func TestNewDerivesTheRepoRootFromTheScriptPath(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"from a sibling directory", "../scripts/regtest.sh"},
		{"from the root", "scripts/regtest.sh"},
		{"two levels down", "../../scripts/regtest.sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(tc.path)

			if !filepath.IsAbs(s.Path) {
				t.Errorf("Path %q is not absolute", s.Path)
			}
			if !filepath.IsAbs(s.Root) {
				t.Errorf("Root %q is not absolute", s.Root)
			}
			if got := filepath.Join(s.Root, "scripts", "regtest.sh"); got != s.Path {
				t.Errorf("Root %q does not contain the script: %q != %q", s.Root, got, s.Path)
			}
		})
	}
}

func TestNewAcceptsAnAbsolutePathUnchanged(t *testing.T) {
	s := New("/srv/hedge/scripts/regtest.sh")

	if s.Path != "/srv/hedge/scripts/regtest.sh" {
		t.Errorf("Path = %q", s.Path)
	}
	if s.Root != "/srv/hedge" {
		t.Errorf("Root = %q, want /srv/hedge", s.Root)
	}
}
