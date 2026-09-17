// Package fan drives the fans of the Acer ALG AL15G-53.
//
// Fan protocol, decoded from the firmware's own SCMD cases 0x68 and 0x69:
//
//	FCMD=0xC1, FDAT=<fan 1..4>, FBUF=<duty 0..255>   manual duty
//	FCMD=0xC1, FDAT=0xFF,       FBUF=<fan 1..4>      hand back to EC auto
package fan

import (
	"math"
	"time"

	"alg/internal/ec"
)

type Fans struct {
	bus ec.Bus
}

func New(bus ec.Bus) *Fans { return &Fans{bus: bus} }

// Duty returns the fan's current raw duty, 0-255.
func (f *Fans) Duty(fan int) (int, error) {
	b, err := f.bus.Read(ec.DutyReg(fan), 1)
	if err != nil {
		return 0, err
	}
	return int(b[0]), nil
}

func (f *Fans) RPM(fan int) (int, error) {
	b, err := f.bus.Read(ec.TachReg(fan), 2)
	if err != nil {
		return 0, err
	}
	period := int(b[0])<<8 | int(b[1])
	if period == 0 {
		return 0, nil
	}
	return ec.TachConst / period, nil
}

// SetDuty commands a raw duty without waiting for the EC to reflect it.
func (f *Fans) SetDuty(fan, raw int) error {
	return f.bus.Command(ec.CmdFan, byte(fan), byte(clampInt(raw, 0, 255)))
}

// Confirm waits for the status mirror to show the commanded duty.
//
// The EC takes a moment to reflect a new duty, so this polls rather than
// checking once, and re-sends the command a single time if the first attempt
// never lands.
func (f *Fans) Confirm(fan, raw int, timeout time.Duration) bool {
	raw = clampInt(raw, 0, 255)
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			if f.SetDuty(fan, raw) != nil {
				return false
			}
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			if d, err := f.Duty(fan); err == nil && abs(d-raw) <= 2 {
				return true
			}
		}
	}
	return false
}

func (f *Fans) SetAuto(fan int) error {
	return f.bus.Command(ec.CmdFan, ec.FanAutoSelector, byte(fan))
}

func (f *Fans) AllAuto() error {
	var first error
	for _, n := range ec.Fans {
		if err := f.SetAuto(n); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func PctToRaw(pct float64) int {
	return int(math.RoundToEven(math.Max(0, math.Min(100, pct)) * 255 / 100))
}

func RawToPct(raw int) int {
	return int(math.Round(float64(raw) * 100 / 255))
}

func clampInt(v, lo, hi int) int {
	return max(lo, min(hi, v))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
