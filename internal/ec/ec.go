// Package ec talks to the embedded controller of the Acer ALG AL15G-53
// (a Clevo/Tongfang design).
//
// Everything goes through the kernel's own EC driver at
// /sys/kernel/debug/ec/ec0/io (module ec_sys, write_support=1), so accesses
// are serialised by the kernel rather than banging ports 0x62/0x66 directly.
//
// The DSDT's ECMD method describes a six-register mailbox. Payload registers
// are written first; writing FCMD last is what triggers the EC:
//
//	FCMD 0xF8   FDAT 0xF9   FBUF 0xFA   FBF1 0xFB   FBF2 0xFC   FBF3 0xFD
package ec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"alg/internal/paths"
)

const (
	FCMD = 0xF8
	FDAT = 0xF9
	FBUF = 0xFA
	FBF1 = 0xFB
	FBF2 = 0xFC
	FBF3 = 0xFD

	// Live status mirror (DSDT: OperationRegion RAM @ 0xFE0B0100)
	RegTemp = 0x07

	// Commands decoded from the firmware's own SCMD cases
	CmdFan      = 0xC1 // SCMD 0x68 / 0x69
	CmdKbdCtl   = 0xC4 // SCMD 0x67, brightness and effects
	CmdKbdColor = 0xCA // SCMD 0x67 sub-op 0x0F

	FanAutoSelector = 0xFF

	// TachConst is the Clevo tachometer constant; rpm = TachConst / period.
	TachConst = 2156220
)

// Fans lists the fan indices: 1 = CPU side, 2 = GPU side.
var Fans = []int{1, 2}

var payloadRegs = [...]int64{FDAT, FBUF, FBF1, FBF2, FBF3}

// DutyReg and TachReg give the status-mirror offsets for a fan.
func DutyReg(fan int) int64 { return 0xCE + int64(fan-1) }
func TachReg(fan int) int64 { return 0xD0 + 2*int64(fan-1) }

// Bus is the EC as the rest of alg sees it. The real implementation is *EC;
// tests substitute a simulated controller.
type Bus interface {
	Read(off int64, n int) ([]byte, error)
	// Command issues a mailbox command. The payload bytes go to FDAT, FBUF,
	// FBF1, FBF2 and FBF3 in that order; registers beyond the payload are
	// left untouched, mirroring the DSDT ECMD sequence.
	Command(cmd byte, payload ...byte) error
}

// Device is a Bus that can be closed.
type Device interface {
	Bus
	Close() error
}

// EC is an open handle on the EC register space.
type EC struct {
	mu sync.Mutex
	f  *os.File
}

// ensureLoaded loads ec_sys with write support and mounts debugfs if needed.
func ensureLoaded() {
	if !isMount("/sys/kernel/debug") {
		exec.Command("mount", "-t", "debugfs", "none", "/sys/kernel/debug").Run()
	}
	if _, err := os.Stat(paths.ECIO); err != nil {
		exec.Command("modprobe", "ec_sys", "write_support=1").Run()
	}
}

func isMount(dir string) bool {
	var a, b syscall.Stat_t
	if syscall.Stat(dir, &a) != nil || syscall.Stat(dir+"/..", &b) != nil {
		return false
	}
	return a.Dev != b.Dev
}

// Open opens the EC for reading and writing.
func Open() (*EC, error) {
	if _, err := os.Stat(paths.ECIO); err != nil && paths.ECIsDefault() {
		ensureLoaded()
	}
	if _, err := os.Stat(paths.ECIO); err != nil {
		return nil, fmt.Errorf("%s is not available. Load the module with:\n"+
			"    sudo modprobe ec_sys write_support=1", paths.ECIO)
	}
	f, err := os.OpenFile(paths.ECIO, os.O_RDWR, 0)
	switch {
	case err == nil:
		return &EC{f: f}, nil
	case errors.Is(err, os.ErrPermission):
		return nil, errors.New("need root to talk to the embedded controller")
	case errors.Is(err, syscall.EROFS):
		return nil, errors.New("ec_sys is loaded without write support. Fix with:\n" +
			"    sudo rmmod ec_sys && sudo modprobe ec_sys write_support=1")
	default:
		return nil, err
	}
}

func (e *EC) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.f.Close()
}

func (e *EC) Read(off int64, n int) ([]byte, error) {
	buf := make([]byte, n)
	e.mu.Lock()
	_, err := e.f.ReadAt(buf, off)
	e.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("EC read 0x%02X: %w", off, err)
	}
	return buf, nil
}

// Command holds the lock for the whole sequence: the curve loop and socket
// clients share this handle, and an interleaved mailbox write would send the
// EC a command with someone else's payload.
func (e *EC) Command(cmd byte, payload ...byte) error {
	if len(payload) > len(payloadRegs) {
		return fmt.Errorf("EC command 0x%02X: payload too long", cmd)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, v := range payload {
		if _, err := e.f.WriteAt([]byte{v}, payloadRegs[i]); err != nil {
			return fmt.Errorf("EC write 0x%02X: %w", payloadRegs[i], err)
		}
	}
	if _, err := e.f.WriteAt([]byte{cmd}, FCMD); err != nil {
		return fmt.Errorf("EC write 0x%02X: %w", FCMD, err)
	}
	time.Sleep(5 * time.Millisecond)
	return nil
}
