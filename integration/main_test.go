//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/arejula27/hedge/arkade"
)

// stack holds what the live services told us about themselves. Nothing here is
// a constant we chose: the point of these tests is that the contract is built
// from what the operator actually runs.
var stack *arkade.Stack

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := arkade.WaitFor(ctx, stackConfig); err != nil {
		fmt.Fprintf(os.Stderr, "integration stack unavailable: %v\n", err)
		fmt.Fprintln(os.Stderr, "start it with `just regtest-up`")
		os.Exit(1)
	}

	var err error
	stack, err = arkade.Connect(ctx, stackConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read the stack's configuration: %v\n", err)
		os.Exit(1)
	}
	defer stack.Close()

	os.Exit(m.Run())
}

// A sanity check on the fixture, not on our code: if the stack reports keys we
// cannot parse or an exit delay of zero, every test below is meaningless.
func TestStackIsUsable(t *testing.T) {
	if stack.ArkdSigner == nil || stack.EmulatorSigner == nil {
		t.Fatal("the stack did not report both signer keys")
	}
	if stack.ExitDelay.Value == 0 {
		t.Fatal("arkd reports a zero unilateral exit delay")
	}
	t.Logf("arkd signer %x", stack.ArkdSigner.SerializeCompressed())
	t.Logf("emulator signer %x", stack.EmulatorSigner.SerializeCompressed())
	t.Logf("exit delay %+v, dust %d", stack.ExitDelay, stack.Dust)
}
