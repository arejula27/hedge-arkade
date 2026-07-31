module github.com/arejula27/hedge/integration

go 1.26.5

require (
	github.com/arejula27/hedge/covenant v0.0.0
	github.com/arkade-os/arkd/pkg/ark-lib v0.8.1-0.20260318170839-137daaec3a70
	github.com/arkade-os/emulator/pkg/arkade v0.0.0-20260728084232-b2c7d853d267
	github.com/arkade-os/emulator/pkg/client v0.0.0-20260728084232-b2c7d853d267
	github.com/arkade-os/go-sdk v0.8.2-0.20260303154656-f29d9e77d5c7
	github.com/btcsuite/btcd v0.24.3-0.20240921052913-67b8efd3ba53
	github.com/btcsuite/btcd/btcec/v2 v2.3.5
	github.com/btcsuite/btcd/btcutil/psbt v1.1.9
	github.com/btcsuite/btcwallet v0.16.10-0.20240718224643-db3a4a2543bd
	google.golang.org/grpc v1.79.3
)

require (
	cel.dev/expr v0.25.1 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.0 // indirect
	github.com/arkade-os/arkd/pkg/errors v0.0.0-20260303153651-8615412e4dea // indirect
	github.com/arkade-os/emulator/api-spec v0.0.0-20260728084232-b2c7d853d267 // indirect
	github.com/bits-and-blooms/bitset v1.20.0 // indirect
	github.com/btcsuite/btcd/btcutil v1.1.5 // indirect
	github.com/btcsuite/btcd/chaincfg/chainhash v1.2.0 // indirect
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f // indirect
	github.com/btcsuite/btcwallet/walletdb v1.4.2 // indirect
	github.com/consensys/gnark-crypto v0.19.2 // indirect
	github.com/decred/dcrd/crypto/blake256 v1.1.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0 // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/julienschmidt/httprouter v1.3.0 // indirect
	github.com/lightninglabs/neutrino/cache v1.1.2 // indirect
	github.com/lightningnetwork/lnd/fn v1.2.1 // indirect
	github.com/lightningnetwork/lnd/tlv v1.2.6 // indirect
	github.com/meshapi/grpc-api-gateway v0.1.0 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/stoewer/go-strcase v1.2.0 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/exp v0.0.0-20250106191152-7588d65b2ba8 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto v0.0.0-20241118233622-e639e219e697 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/arejula27/hedge/covenant => ../covenant
