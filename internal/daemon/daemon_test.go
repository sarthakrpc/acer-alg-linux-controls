package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"alg/internal/config"
	"alg/internal/ec"
	"alg/internal/ec/sim"
	"alg/internal/fan"
	"alg/internal/kbd"
	"alg/internal/paths"
	"alg/internal/proto"
)

type fakeTemps struct {
	mu   sync.Mutex
	temp float64
}

func (f *fakeTemps) set(t float64) {
	f.mu.Lock()
	f.temp = t
	f.mu.Unlock()
}

func (f *fakeTemps) ReadAll() map[string]float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]float64{"cpu": f.temp}
}

func (f *fakeTemps) Hottest() (float64, string) { return fan.Hottest(f.ReadAll()) }
func (f *fakeTemps) SetUseGPU(string)           {}

// fastSettings runs the curve quickly enough to test, with ramping and
// smoothing off unless a test turns them on.
func fastSettings() *config.Settings {
	s := config.Defaults()
	s.PollInterval, s.Step = 0.2, 0.1 // the floor the config loader enforces
	s.RampUp, s.RampDown, s.Smoothing = 0, 0, 0
	return s
}

// newTestController also writes cfg out as the config file, because choosing
// a curve re-reads it from disk.
func newTestController(t *testing.T, cfg *config.Settings) (*Controller, *sim.EC, *fakeTemps) {
	t.Helper()
	dir := t.TempDir()
	paths.StateDir = filepath.Join(dir, "state")
	paths.RunDir = filepath.Join(dir, "run")
	paths.Config = filepath.Join(dir, "alg.conf")
	text := fmt.Sprintf("[general]\npoll_interval = %g\nstep_interval = %g\nramp_up = %g\n"+
		"ramp_down = %g\ntemp_smoothing = %g\nemergency_temp = %g\n",
		cfg.PollInterval, cfg.Step, cfg.RampUp, cfg.RampDown, cfg.Smoothing, cfg.Emergency)
	if err := os.WriteFile(paths.Config, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	bus := sim.New()
	temps := &fakeTemps{temp: 50}
	c := NewController(bus, loaded)
	c.sensors = temps
	t.Cleanup(c.Shutdown)
	return c, bus, temps
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func savedMode(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(paths.FanMode())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestManualAndAuto(t *testing.T) {
	c, bus, _ := newTestController(t, fastSettings())

	res, err := c.SetManual(70, 0, true)
	if err != nil || res.Raw != 178 || len(res.Unconfirmed) != 0 {
		t.Fatalf("SetManual = %+v, %v", res, err)
	}
	if bus.Duty(1) != 178 || bus.Duty(2) != 178 {
		t.Errorf("duties = %d, %d", bus.Duty(1), bus.Duty(2))
	}
	if got := savedMode(t); got != "manual 70%" {
		t.Errorf("saved %q", got)
	}

	// One fan only: the other keeps its pin.
	if _, err := c.SetManual(40, 2, false); err != nil {
		t.Fatal(err)
	}
	if bus.Duty(1) != 178 || bus.Duty(2) != 102 {
		t.Errorf("duties = %d, %d", bus.Duty(1), bus.Duty(2))
	}
	if got := savedMode(t); got != "manual fan1=70% fan2=40%" {
		t.Errorf("saved %q", got)
	}

	if err := c.SetAuto(); err != nil {
		t.Fatal(err)
	}
	if !bus.IsAuto(1) || !bus.IsAuto(2) || savedMode(t) != "auto (EC default)" {
		t.Errorf("auto: fan1=%v fan2=%v saved=%q", bus.IsAuto(1), bus.IsAuto(2), savedMode(t))
	}

	// From auto, pinning one fan leaves the other with the firmware.
	if _, err := c.SetManual(55, 1, false); err != nil {
		t.Fatal(err)
	}
	if bus.IsAuto(1) || !bus.IsAuto(2) || savedMode(t) != "manual fan1=55%" {
		t.Errorf("single: fan1 auto=%v fan2 auto=%v saved=%q", bus.IsAuto(1), bus.IsAuto(2), savedMode(t))
	}

	for _, bad := range []float64{-1, 101} {
		if _, err := c.SetManual(bad, 0, false); err == nil {
			t.Errorf("SetManual(%g) should be rejected", bad)
		}
	}
	if _, err := c.SetManual(50, 3, false); err == nil {
		t.Error("fan 3 does not exist")
	}
}

func TestCurveFollowsTemperature(t *testing.T) {
	c, bus, temps := newTestController(t, fastSettings())
	temps.set(60) // balanced: 60C -> 40%
	if _, err := c.SetCurve("balanced"); err != nil {
		t.Fatal(err)
	}
	if got := savedMode(t); got != "curve:balanced" {
		t.Errorf("saved %q", got)
	}
	eventually(t, "40% at 60C", func() bool { return bus.Duty(1) == fan.PctToRaw(40) && bus.Duty(2) == fan.PctToRaw(40) })

	temps.set(82) // -> 75%
	eventually(t, "75% at 82C", func() bool { return bus.Duty(1) == fan.PctToRaw(75) })

	// A 2-point drop in the target is inside the hysteresis band: no change.
	temps.set(81)
	time.Sleep(100 * time.Millisecond)
	if bus.Duty(1) != fan.PctToRaw(75) {
		t.Errorf("hysteresis should hold 75%%, duty = %d", bus.Duty(1))
	}
	temps.set(60)
	eventually(t, "back to 40%", func() bool { return bus.Duty(1) == fan.PctToRaw(40) })

	if _, err := c.SetCurve("no-such-profile"); err == nil {
		t.Error("an unknown profile should be rejected")
	}
	if st, _ := c.Status(); st.Kind != "curve" || st.Profile != "balanced" {
		t.Errorf("a rejected profile must not disturb the running curve: %+v", st)
	}
}

func TestCurveRampsGradually(t *testing.T) {
	cfg := fastSettings()
	cfg.RampUp = 100 // %/s: 10 points per 100 ms step, the fastest the config allows
	cfg.Emergency = 200
	c, bus, temps := newTestController(t, cfg)
	temps.set(94) // balanced tops out at 100%
	bus.Commands()
	if _, err := c.SetCurve("balanced"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "100%", func() bool { return bus.Duty(1) == 255 })
	c.SetAuto()

	var steps []int
	for _, cmd := range bus.Commands() {
		if cmd.Cmd == ec.CmdFan && cmd.Payload[0] == 1 {
			steps = append(steps, int(cmd.Payload[1]))
		}
	}
	if len(steps) < 5 {
		t.Fatalf("expected a staircase up to 255, got %v", steps)
	}
	for i := 1; i < len(steps); i++ {
		if d := steps[i] - steps[i-1]; d < 0 || d > fan.PctToRaw(10)+1 {
			t.Fatalf("step %d jumped by %d: %v", i, d, steps)
		}
	}
}

func TestEmergencyOverride(t *testing.T) {
	cfg := fastSettings()
	cfg.RampUp = 1 // slow enough that only the override can explain 100%
	c, bus, temps := newTestController(t, cfg)
	temps.set(50)
	if _, err := c.SetCurve("silent"); err != nil {
		t.Fatal(err)
	}
	temps.set(96)
	eventually(t, "emergency 100%", func() bool { return bus.Duty(1) == 255 && bus.Duty(2) == 255 })
	if st, _ := c.Status(); !strings.HasPrefix(st.Mode, "EMERGENCY") || st.Kind != "curve" {
		t.Errorf("status during emergency: %+v", st)
	}
	if got := savedMode(t); got != "curve:silent" {
		t.Errorf("the emergency must not overwrite the saved choice, got %q", got)
	}

	// Still inside the stand-down band: stays at full.
	temps.set(90)
	time.Sleep(80 * time.Millisecond)
	if bus.Duty(1) != 255 {
		t.Errorf("should hold 100%% until 8C below the trip point, duty = %d", bus.Duty(1))
	}
	temps.set(60)
	eventually(t, "curve resumed", func() bool {
		st, _ := c.Status()
		return st.Mode == "curve:silent"
	})
}

func TestShutdownReleasesCurveButKeepsManual(t *testing.T) {
	c, bus, _ := newTestController(t, fastSettings())
	if _, err := c.SetCurve("max"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "100%", func() bool { return bus.Duty(1) == 255 })
	c.Shutdown()
	if !bus.IsAuto(1) || !bus.IsAuto(2) {
		t.Error("a curve with nothing following it must go back to the firmware")
	}
	if got := savedMode(t); got != "curve:max" {
		t.Errorf("shutdown must not change the saved choice, got %q", got)
	}

	c2, bus2, _ := newTestController(t, fastSettings())
	c2.SetManual(65, 0, false)
	c2.Shutdown()
	if bus2.IsAuto(1) || bus2.Duty(1) != fan.PctToRaw(65) {
		t.Error("a manual speed must outlive the daemon")
	}
}

func TestRestoreFromDisk(t *testing.T) {
	c, bus, temps := newTestController(t, fastSettings())
	kbd.SaveState(kbd.State{Brightness: 26, Zones: map[string]kbd.RGB{"left": {1, 2, 3}}})

	fan.SaveMode(fan.ParseMode("manual fan1=30%"))
	bus.Commands()
	notes, err := c.Restore(true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if bus.Duty(1) != fan.PctToRaw(30) || !bus.IsAuto(2) {
		t.Errorf("manual restore: duty1=%d fan2 auto=%v", bus.Duty(1), bus.IsAuto(2))
	}
	var sawZone, sawBrightness bool
	for _, cmd := range bus.Commands() {
		if cmd.Cmd == ec.CmdKbdColor && cmd.Payload[0] == 3 {
			sawZone = cmd.Payload[1] == 3 && cmd.Payload[2] == 1 && cmd.Payload[3] == 2
		}
		if cmd.Cmd == ec.CmdKbdCtl && cmd.Payload[0] == 0x02 {
			sawBrightness = cmd.Payload[1] == 26
		}
	}
	if !sawZone || !sawBrightness {
		t.Errorf("backlight not restored (zone=%v brightness=%v), notes %v", sawZone, sawBrightness, notes)
	}

	temps.set(90)
	fan.SaveMode(fan.CurveMode("balanced"))
	if _, err := c.Restore(true, 1); err != nil {
		t.Fatal(err)
	}
	eventually(t, "curve running after restore", func() bool { return bus.Duty(1) == 255 })

	// A saved profile that no longer exists lands on the firmware curve.
	fan.SaveMode(fan.CurveMode("deleted"))
	if _, err := c.Restore(true, 1); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.Status(); st.Kind != "auto" || !bus.IsAuto(1) {
		t.Errorf("missing profile: %+v", st)
	}
}

func TestRestoreRetriesWhileECWakes(t *testing.T) {
	c, bus, _ := newTestController(t, fastSettings())
	c.SetManual(45, 0, false)
	bus.FailNext(1)
	if _, err := c.Restore(false, 3); err != nil {
		t.Fatalf("one failed access should be retried: %v", err)
	}
	if bus.Duty(1) != fan.PctToRaw(45) {
		t.Errorf("duty = %d", bus.Duty(1))
	}
	bus.SetFail(true)
	if _, err := c.Restore(false, 1); err == nil {
		t.Error("a dead EC must be reported")
	}
}

func TestBacklightOps(t *testing.T) {
	c, _, _ := newTestController(t, fastSettings())
	if st := c.KbdState(); st.Known {
		t.Error("nothing set yet")
	}
	if note, err := c.RestoreBacklight(); err != nil || !strings.Contains(note, "untouched") {
		t.Errorf("with nothing saved the backlight must be left alone: %q, %v", note, err)
	}
	if _, err := c.KbdBrightness(40); err != nil {
		t.Fatal(err)
	}
	c.KbdOff()
	if st := c.KbdState(); st.Raw != 0 {
		t.Errorf("off: %+v", st)
	}
	if raw, _ := c.KbdOn(); raw != kbd.PctToRaw(40) {
		t.Errorf("on should return to the previous level, got %d", raw)
	}
	c.KbdOff()
	res, err := c.KbdColor(kbd.RGB{255, 0, 0}, "left")
	if err != nil || !res.Raised {
		t.Errorf("a colour at zero brightness should raise it: %+v, %v", res, err)
	}
	if st := c.KbdState(); st.Raw != 255 || st.Zones["left"] != (kbd.RGB{255, 0, 0}) || len(st.Zones) != 1 {
		t.Errorf("state: %+v", st)
	}
	if _, err := c.KbdColor(kbd.RGB{0, 0, 300}, "all"); err == nil {
		t.Error("out-of-range channel should be rejected")
	}
	if _, err := c.KbdColor(kbd.RGB{}, "top"); err == nil {
		t.Error("unknown zone should be rejected")
	}
	if err := c.KbdEffect("disco"); err == nil {
		t.Error("unknown effect should be rejected")
	}
}

func TestSocketProtocol(t *testing.T) {
	c, bus, _ := newTestController(t, fastSettings())
	srv, err := Listen(c)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	if _, err := Listen(c); err == nil {
		t.Error("a second daemon on the same socket should be refused")
	}

	cl, err := proto.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	call := func(req proto.Request, out any) error { return cl.Call(req, 5*time.Second, out) }

	var pong proto.Pong
	if err := call(proto.Request{Op: proto.OpPing}, &pong); err != nil || pong.PID != os.Getpid() {
		t.Fatalf("ping: %+v, %v", pong, err)
	}
	if err := call(proto.Request{Op: proto.OpFanSet}, nil); err == nil {
		t.Error("fan_set without a percent must be rejected, not read as 0%")
	}
	var set proto.FanSetResult
	if err := call(proto.Request{Op: proto.OpFanSet, Percent: proto.Pct(0)}, &set); err != nil || bus.Duty(1) != 0 {
		t.Errorf("an explicit 0%% is allowed: %+v, %v", set, err)
	}
	if err := call(proto.Request{Op: proto.OpFanSet, Percent: proto.Pct(80), Verify: true}, &set); err != nil {
		t.Fatal(err)
	}
	var st proto.Status
	if err := call(proto.Request{Op: proto.OpStatus}, &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode != "manual 80%" || st.Kind != "manual" || len(st.Fans) != 2 || st.Fans[0].Percent != 80 || st.Fans[0].RPM == 0 {
		t.Errorf("status: %+v", st)
	}
	var profiles proto.Profiles
	if err := call(proto.Request{Op: proto.OpProfiles}, &profiles); err != nil || len(profiles.Profiles) != 4 {
		t.Errorf("profiles: %+v, %v", profiles, err)
	}
	if err := call(proto.Request{Op: "bogus"}, nil); err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Errorf("bogus op: %v", err)
	}
	var resumed proto.ResumeResult
	if err := call(proto.Request{Op: proto.OpResume, Retries: 2}, &resumed); err != nil || len(resumed.Notes) != 2 {
		t.Errorf("resume: %+v, %v", resumed, err)
	}
}

func TestAuthorisation(t *testing.T) {
	if !authorised(0, nil) {
		t.Error("root is always authorised")
	}
	if !authorised(uint32(os.Geteuid()), nil) {
		t.Error("the daemon's own user is authorised")
	}
	if authorised(4294967294, []string{"sudo"}) {
		t.Error("an unknown uid must not be authorised")
	}
}
