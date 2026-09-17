// Package sim is a software stand-in for the embedded controller, modelling
// just enough of the mailbox for the rest of alg to be tested without
// hardware: fan duty and auto commands are reflected in the status mirror
// the way the real firmware reflects them.
package sim

import (
	"errors"
	"sync"

	"alg/internal/ec"
)

// AutoDuty is the raw duty the simulated firmware curve settles on.
const AutoDuty = 77

// Command is one mailbox command as the controller received it.
type Command struct {
	Cmd     byte
	Payload []byte
}

type EC struct {
	mu   sync.Mutex
	reg  [256]byte
	auto map[int]bool
	log  []Command

	fail     bool
	failNext int
}

func New() *EC {
	e := &EC{auto: map[int]bool{}}
	e.reg[ec.RegTemp] = 45
	for _, f := range ec.Fans {
		e.auto[f] = true
		e.setDuty(f, AutoDuty)
	}
	return e
}

var errAsleep = errors.New("simulated EC is not responding")

// SetFail makes every access return an error, like an EC that has not woken
// up yet.
func (e *EC) SetFail(fail bool) {
	e.mu.Lock()
	e.fail = fail
	e.mu.Unlock()
}

// FailNext fails that many accesses, then recovers.
func (e *EC) FailNext(n int) {
	e.mu.Lock()
	e.failNext = n
	e.mu.Unlock()
}

func (e *EC) failing() bool {
	if e.failNext > 0 {
		e.failNext--
		return true
	}
	return e.fail
}

func (e *EC) setDuty(fan int, raw byte) {
	e.reg[ec.DutyReg(fan)] = raw
	period := 0
	if rpm := int(raw) * 5200 / 255; rpm > 0 {
		period = ec.TachConst / rpm
	}
	e.reg[ec.TachReg(fan)] = byte(period >> 8)
	e.reg[ec.TachReg(fan)+1] = byte(period)
}

func (e *EC) Read(off int64, n int) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failing() {
		return nil, errAsleep
	}
	return append([]byte(nil), e.reg[off:off+int64(n)]...), nil
}

func (e *EC) Command(cmd byte, payload ...byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failing() {
		return errAsleep
	}
	e.log = append(e.log, Command{cmd, append([]byte(nil), payload...)})
	if cmd == ec.CmdFan && len(payload) >= 2 {
		switch sel, arg := payload[0], payload[1]; {
		case sel == ec.FanAutoSelector:
			e.auto[int(arg)] = true
			e.setDuty(int(arg), AutoDuty)
		case sel >= 1 && sel <= 2:
			e.auto[int(sel)] = false
			e.setDuty(int(sel), arg)
		}
	}
	return nil
}

func (e *EC) Close() error { return nil }

// SetTemp sets the board temperature register.
func (e *EC) SetTemp(c byte) {
	e.mu.Lock()
	e.reg[ec.RegTemp] = c
	e.mu.Unlock()
}

// IsAuto reports whether a fan is under simulated firmware control.
func (e *EC) IsAuto(fan int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.auto[fan]
}

// Duty returns a fan's raw duty.
func (e *EC) Duty(fan int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return int(e.reg[ec.DutyReg(fan)])
}

// Commands returns everything received so far and clears the record.
func (e *EC) Commands() []Command {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.log
	e.log = nil
	return out
}
