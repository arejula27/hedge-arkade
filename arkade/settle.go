package arkade

import (
	"bytes"
	"fmt"

	"github.com/arejula27/hedge/contract"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	emulatorvm "github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

// SignedPrice is one oracle publication: the 24-byte message and the signature
// over it.
type SignedPrice struct {
	Message   []byte
	Signature []byte
}

// SettlementWitness is the stack the settlement leaf expects, bottom item
// first. The covenant needs the settlement message and its immediate
// predecessor, which is what pins settlement to the first message published
// after maturity.
func SettlementWitness(settlement, previous SignedPrice) [][]byte {
	return [][]byte{
		settlement.Signature, settlement.Message,
		previous.Signature, previous.Message,
	}
}

// ContractInput points at the contract VTXO and reveals one of its leaves.
func ContractInput(
	c contract.Contract, leaf contract.Leaf, outpoint wire.OutPoint, amount int64,
) (offchain.VtxoInput, error) {
	proof, err := c.Tapscript(leaf)
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("Tapscript: %w", err)
	}

	control, err := txscript.ParseControlBlock(proof.ControlBlock)
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("ParseControlBlock: %w", err)
	}

	vtxo, err := c.VtxoScript()
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("VtxoScript: %w", err)
	}
	revealed, err := vtxo.Encode()
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("Encode: %w", err)
	}

	return offchain.VtxoInput{
		Outpoint: &outpoint,
		Tapscript: &waddrmgr.Tapscript{
			ControlBlock:   control,
			RevealedScript: proof.Script,
		},
		Amount:             amount,
		RevealedTapscripts: revealed,
	}, nil
}

// BuildSettlement spends the contract VTXO through the settlement leaf, paying
// each side its own lock script.
func BuildSettlement(
	s *Stack, c contract.Contract, outpoint wire.OutPoint, short, long int64, witness [][]byte,
) (*psbt.Packet, []*psbt.Packet, error) {
	return BuildSettlementPaying(s, c, outpoint, []*wire.TxOut{
		{Value: short, PkScript: c.Terms.ShortLockScript},
		{Value: long, PkScript: c.Terms.LongLockScript},
	}, witness)
}

// BuildSettlementPaying is BuildSettlement with the outputs given explicitly,
// which is what a test that pays the wrong recipient needs.
func BuildSettlementPaying(
	s *Stack, c contract.Contract, outpoint wire.OutPoint, outputs []*wire.TxOut, witness [][]byte,
) (*psbt.Packet, []*psbt.Packet, error) {
	input, err := ContractInput(c, contract.LeafSettlement, outpoint, c.Terms.PayoutSats)
	if err != nil {
		return nil, nil, err
	}

	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{input}, outputs, s.CheckpointTapscript,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("building the settlement transaction: %w", err)
	}

	settlementScript, err := c.SettlementScript()
	if err != nil {
		return nil, nil, fmt.Errorf("SettlementScript: %w", err)
	}

	if err := AddEmulatorPacket(arkTx, emulatorvm.EmulatorEntry{
		Vin:     0,
		Script:  settlementScript,
		Witness: witness,
	}); err != nil {
		return nil, nil, err
	}

	return arkTx, checkpoints, nil
}

// AddEmulatorPacket inserts the extension OP_RETURN before the P2A anchor, so
// the payout output indices the covenant inspects do not shift.
func AddEmulatorPacket(ptx *psbt.Packet, entries ...emulatorvm.EmulatorEntry) error {
	packet, err := emulatorvm.NewPacket(entries...)
	if err != nil {
		return fmt.Errorf("building the emulator packet: %w", err)
	}

	txOut, err := extension.Extension{packet}.TxOut()
	if err != nil {
		return fmt.Errorf("encoding the extension output: %w", err)
	}

	last := len(ptx.UnsignedTx.TxOut) - 1
	if last >= 0 && bytes.Equal(ptx.UnsignedTx.TxOut[last].PkScript, txutils.ANCHOR_PKSCRIPT) {
		anchor := ptx.UnsignedTx.TxOut[last]
		ptx.UnsignedTx.TxOut[last] = txOut
		ptx.UnsignedTx.AddTxOut(anchor)
	} else {
		ptx.UnsignedTx.AddTxOut(txOut)
	}
	ptx.Outputs = append(ptx.Outputs, psbt.POutput{})
	return nil
}
