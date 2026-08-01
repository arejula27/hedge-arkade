package arkade

import "context"

// Chain is what a regtest deployment can do to the underlying chain and a
// production one cannot: pay an address out of nowhere, and mine.
//
// Boarding needs both. The faucet pays the boarding address, and arkd will not
// treat the payment as confirmed until a block carries it — so on a stack with
// no automatic miner, retrying a settle without mining is retrying nothing.
type Chain interface {
	Faucet(ctx context.Context, address string, sats int64) error
	Mine(ctx context.Context, blocks int) error
}
