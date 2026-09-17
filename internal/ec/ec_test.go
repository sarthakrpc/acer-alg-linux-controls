package ec

import (
	"os"
	"path/filepath"
	"testing"

	"alg/internal/paths"
)

// A plain 256-byte file stands in for /sys/kernel/debug/ec/ec0/io: the point
// is to pin down which offsets get written, not to model the firmware.
func TestMailboxLayout(t *testing.T) {
	paths.ECIO = filepath.Join(t.TempDir(), "io")
	if err := os.WriteFile(paths.ECIO, make([]byte, 256), 0o600); err != nil {
		t.Fatal(err)
	}
	dev, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()

	if err := dev.Command(CmdKbdColor, 4, 0x33, 0x11, 0x22); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(paths.ECIO)
	want := map[int]byte{FCMD: CmdKbdColor, FDAT: 4, FBUF: 0x33, FBF1: 0x11, FBF2: 0x22, FBF3: 0}
	for off, v := range want {
		if got[off] != v {
			t.Errorf("reg 0x%02X = 0x%02X, want 0x%02X", off, got[off], v)
		}
	}
	for off, v := range got {
		if _, touched := want[off]; !touched && v != 0 {
			t.Errorf("reg 0x%02X was written (0x%02X) but is outside the mailbox", off, v)
		}
	}

	// A shorter command must leave the registers beyond its payload alone.
	if err := dev.Command(CmdFan, 1, 128); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(paths.ECIO)
	if got[FDAT] != 1 || got[FBUF] != 128 || got[FBF1] != 0x11 || got[FCMD] != CmdFan {
		t.Errorf("after fan command: % X", got[FCMD:FBF3+1])
	}

	if err := dev.Command(CmdFan, 1, 2, 3, 4, 5, 6); err == nil {
		t.Error("an oversized payload should be rejected")
	}

	b, err := dev.Read(FDAT, 2)
	if err != nil || b[0] != 1 || b[1] != 128 {
		t.Errorf("Read = % X, %v", b, err)
	}
}

func TestRegisterMap(t *testing.T) {
	if DutyReg(1) != 0xCE || DutyReg(2) != 0xCF || TachReg(1) != 0xD0 || TachReg(2) != 0xD2 {
		t.Error("status mirror offsets do not match the DSDT")
	}
}

func TestOpenErrors(t *testing.T) {
	paths.ECIO = filepath.Join(t.TempDir(), "missing")
	if _, err := Open(); err == nil {
		t.Error("opening a missing EC should fail")
	}
}
