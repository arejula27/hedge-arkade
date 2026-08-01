package arkade

import (
	"bytes"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	emulatorvm "github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

// The covenant reads the payouts by index. offchain.BuildTxs puts a P2A anchor
// last, so appending the emulator packet would push the anchor to index 2 and
// leave the payouts where they are — but only while the anchor is last. Pinning
// the insertion point is what keeps the covenant's output 0 and output 1 the
// payouts in every transaction shape.
func TestAddEmulatorPacketGoesBeforeTheAnchor(t *testing.T) {
	short, long := script(0x51), script(0x52)
	ptx := packet(t,
		&wire.TxOut{Value: 10, PkScript: short},
		&wire.TxOut{Value: 20, PkScript: long},
		&wire.TxOut{Value: 0, PkScript: txutils.ANCHOR_PKSCRIPT},
	)

	if err := AddEmulatorPacket(ptx, entry(t)); err != nil {
		t.Fatalf("AddEmulatorPacket: %v", err)
	}

	outs := ptx.UnsignedTx.TxOut
	if len(outs) != 4 {
		t.Fatalf("got %d outputs, want 4", len(outs))
	}
	if !bytes.Equal(outs[0].PkScript, short) || !bytes.Equal(outs[1].PkScript, long) {
		t.Error("the payouts moved")
	}
	if !bytes.Equal(outs[3].PkScript, txutils.ANCHOR_PKSCRIPT) {
		t.Error("the anchor is no longer last")
	}
	if bytes.Equal(outs[2].PkScript, txutils.ANCHOR_PKSCRIPT) {
		t.Error("the emulator packet did not land at index 2")
	}
	if len(ptx.Outputs) != len(outs) {
		t.Errorf("the psbt has %d outputs for %d in the transaction", len(ptx.Outputs), len(outs))
	}
}

func TestAddEmulatorPacketAppendsWhenThereIsNoAnchor(t *testing.T) {
	short := script(0x51)
	ptx := packet(t, &wire.TxOut{Value: 10, PkScript: short})

	if err := AddEmulatorPacket(ptx, entry(t)); err != nil {
		t.Fatalf("AddEmulatorPacket: %v", err)
	}

	outs := ptx.UnsignedTx.TxOut
	if len(outs) != 2 {
		t.Fatalf("got %d outputs, want 2", len(outs))
	}
	if !bytes.Equal(outs[0].PkScript, short) {
		t.Error("the payout moved")
	}
	if len(ptx.Outputs) != len(outs) {
		t.Errorf("the psbt has %d outputs for %d in the transaction", len(ptx.Outputs), len(outs))
	}
}

// The covenant pops the stack from the top, so the order here is the whole
// meaning of the witness: settlement first, then its predecessor.
func TestSettlementWitnessIsInTheOrderTheCovenantPops(t *testing.T) {
	settlement := SignedPrice{Message: []byte("settle-msg"), Signature: []byte("settle-sig")}
	previous := SignedPrice{Message: []byte("prev-msg"), Signature: []byte("prev-sig")}

	got := SettlementWitness(settlement, previous)

	want := [][]byte{
		[]byte("settle-sig"), []byte("settle-msg"),
		[]byte("prev-sig"), []byte("prev-msg"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d stack items, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEncodeRoundTrips(t *testing.T) {
	first := packet(t, &wire.TxOut{Value: 10, PkScript: script(0x51)})
	second := packet(t, &wire.TxOut{Value: 20, PkScript: script(0x52)})

	encoded, err := Encode([]*psbt.Packet{first, second})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(encoded) != 2 {
		t.Fatalf("got %d packets, want 2", len(encoded))
	}

	for i, b64 := range encoded {
		if _, err := psbt.NewFromRawBytes(bytes.NewReader([]byte(b64)), true); err != nil {
			t.Errorf("packet %d does not decode: %v", i, err)
		}
	}
}

func TestEncodeOfNothingIsNotAnError(t *testing.T) {
	encoded, err := Encode(nil)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(encoded) != 0 {
		t.Errorf("got %d packets, want none", len(encoded))
	}
}

func packet(t *testing.T, outputs ...*wire.TxOut) *psbt.Packet {
	t.Helper()

	tx := wire.NewMsgTx(3)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Index: 0}, nil, nil))
	for _, out := range outputs {
		tx.AddTxOut(out)
	}

	ptx, err := psbt.NewFromUnsignedTx(tx)
	if err != nil {
		t.Fatalf("building the packet: %v", err)
	}
	return ptx
}

func entry(t *testing.T) emulatorvm.EmulatorEntry {
	t.Helper()

	return emulatorvm.EmulatorEntry{
		Vin:     0,
		Script:  []byte{0x51},
		Witness: [][]byte{{0x01}},
	}
}

// script is a stand-in pkScript: OP_1 and a one-byte push, valid enough to
// compare and short enough to read.
func script(tag byte) []byte { return []byte{0x51, 0x01, tag} }
