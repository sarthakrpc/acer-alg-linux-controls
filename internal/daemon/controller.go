// Package daemon is the root service behind alg: it owns the EC, runs the
// fan curve, and serves the control socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"alg/internal/config"
	"alg/internal/ec"
	"alg/internal/fan"
	"alg/internal/kbd"
	"alg/internal/proto"
)

// tempSource is what the controller needs from the sensors; tests drive the
// temperature through it.
type tempSource interface {
	ReadAll() map[string]float64
	Hottest() (float64, string)
	SetUseGPU(mode string)
}

// Controller holds the fan mode and performs every transition between modes.
type Controller struct {
	bus     ec.Bus
	fans    *fan.Fans
	sensors tempSource
	bl      *kbd.Backlight

	// mu serialises mode transitions and guards mode, cfg and loop. The
	// curve goroutine never takes it, so stopping the loop while holding it
	// cannot deadlock.
	mu   sync.Mutex
	mode fan.Mode
	cfg  *config.Settings
	loop *curveLoop

	// note overrides the mode line while something transient is going on
	// (the emergency override). Written by the curve goroutine.
	note atomic.Value // string

	// kbdMu guards the read-modify-write of the backlight state file.
	kbdMu sync.Mutex
}

type curveLoop struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewController(bus ec.Bus, cfg *config.Settings) *Controller {
	c := &Controller{
		bus:     bus,
		fans:    fan.New(bus),
		sensors: fan.NewSensors(bus, cfg.UseGPU),
		bl:      kbd.New(bus),
		cfg:     cfg,
		mode:    fan.AutoMode(),
	}
	c.note.Store("")
	return c
}

// ---------------------------------------------------------------------------
// mode transitions
// ---------------------------------------------------------------------------

func (c *Controller) stopLoopLocked() {
	if c.loop == nil {
		return
	}
	c.loop.cancel()
	<-c.loop.done
	c.loop = nil
	c.note.Store("")
}

func (c *Controller) startLoopLocked(profile string) error {
	curve, err := c.cfg.Curve(profile)
	if err != nil {
		return err
	}
	c.stopLoopLocked()
	ctx, cancel := context.WithCancel(context.Background())
	c.loop = &curveLoop{cancel: cancel, done: make(chan struct{})}
	log.Printf("fan curve '%s' -> %s", profile, curve)
	log.Printf("ramp up %g%%/s, down %g%%/s", c.cfg.RampUp, c.cfg.RampDown)
	go c.runCurve(ctx, c.loop.done, profile, curve, *c.cfg)
	return nil
}

// persistLocked records the user's choice. A failure to save is logged, not
// returned: the fans are already doing what was asked.
func (c *Controller) persistLocked() {
	if err := fan.SaveMode(c.mode); err != nil {
		log.Printf("could not save the fan mode: %v", err)
	}
}

// SetManual pins one fan, or every fan when which is 0.
func (c *Controller) SetManual(pct float64, which int, verify bool) (*proto.FanSetResult, error) {
	if pct < 0 || pct > 100 || pct != pct {
		return nil, fmt.Errorf("percent must be between 0 and 100, not %g", pct)
	}
	if which != 0 && !fan.ValidFan(which) {
		return nil, fmt.Errorf("there is no fan %d", which)
	}
	raw := fan.PctToRaw(pct)

	c.mu.Lock()
	next := fan.ManualAll(pct)
	if which != 0 {
		// Keep whatever the other fan was pinned to. Coming from the curve
		// or the firmware it was not pinned at all, and goes back to auto.
		next = fan.Mode{Kind: fan.Manual, Duty: map[int]float64{which: pct}}
		if c.mode.Kind == fan.Manual {
			for f, p := range c.mode.Duty {
				if f != which {
					next.Duty[f] = p
				}
			}
		}
	}
	c.stopLoopLocked()
	err := c.applyManualLocked(next)
	if err == nil {
		c.mode = next
		c.persistLocked()
	}
	mode := c.mode.String()
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	res := &proto.FanSetResult{Percent: pct, Raw: raw, Mode: mode}
	if verify {
		// Outside the lock: this can take seconds, and status requests
		// should not queue up behind it.
		for _, f := range ec.Fans {
			if which != 0 && f != which {
				continue
			}
			if !c.fans.Confirm(f, raw, 1500*time.Millisecond) {
				res.Unconfirmed = append(res.Unconfirmed, f)
			}
		}
	}
	return res, nil
}

func (c *Controller) applyManualLocked(m fan.Mode) error {
	for _, f := range ec.Fans {
		var err error
		if pct, pinned := m.Duty[f]; pinned {
			err = c.fans.SetDuty(f, fan.PctToRaw(pct))
		} else {
			err = c.fans.SetAuto(f)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// SetAuto hands the fans back to the firmware's built-in curve.
func (c *Controller) SetAuto() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLoopLocked()
	if err := c.fans.AllAuto(); err != nil {
		return err
	}
	c.mode = fan.AutoMode()
	c.persistLocked()
	return nil
}

// SetCurve starts following a profile; an empty name means the configured
// default. The config file is re-read first, so edits take effect here.
func (c *Controller) SetCurve(profile string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reloadLocked()
	if profile == "" {
		profile = c.cfg.Profile
	}
	if err := c.startLoopLocked(profile); err != nil {
		return "", err
	}
	c.mode = fan.CurveMode(profile)
	c.persistLocked()
	return profile, nil
}

func (c *Controller) reloadLocked() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("could not read the config, keeping the previous one: %v", err)
		return
	}
	for _, w := range cfg.Warnings {
		log.Printf("config: %s", w)
	}
	c.cfg = cfg
	c.sensors.SetUseGPU(cfg.UseGPU)
}

// Reload re-reads the config and, if the curve is running, restarts it so
// the new settings apply.
func (c *Controller) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reloadLocked()
	if c.mode.Kind == fan.CurveRun {
		return c.startLoopLocked(c.mode.Profile)
	}
	return nil
}

// Settings returns the current configuration.
func (c *Controller) Settings() *config.Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

// Restore puts the saved backlight and fan mode back. The EC forgets both
// across suspend and power-off, and can be briefly unresponsive right after
// waking, so this retries before giving up.
//
// fromDisk loads the mode that was saved before the daemon last stopped;
// otherwise the mode already in memory is reapplied.
func (c *Controller) Restore(fromDisk bool, retries int) ([]string, error) {
	if retries < 1 {
		retries = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if fromDisk {
		c.mode, _ = fan.LoadMode()
	}
	var last error
	for attempt := 1; attempt <= retries; attempt++ {
		notes, err := c.restoreOnceLocked()
		if err == nil {
			return notes, nil
		}
		last = err
		if attempt < retries {
			time.Sleep(600 * time.Millisecond)
		}
	}
	return nil, fmt.Errorf("could not restore settings after %d attempts: %w", retries, last)
}

func (c *Controller) restoreOnceLocked() ([]string, error) {
	var notes []string

	// Fans first: if the EC is only half awake, cooling matters more than
	// the keyboard lighting up.
	switch c.mode.Kind {
	case fan.Manual:
		c.stopLoopLocked()
		if err := c.applyManualLocked(c.mode); err != nil {
			return nil, err
		}
		notes = append(notes, "fans "+c.mode.String())
	case fan.CurveRun:
		// Restarting the loop makes it re-read the duty the EC fell back to
		// and ramp from there, rather than assuming its last write survived.
		if err := c.startLoopLocked(c.mode.Profile); err != nil {
			// The profile may have been removed from the config since it
			// was chosen. The firmware curve is the safe place to land.
			log.Printf("%v; falling back to the firmware curve", err)
			c.mode = fan.AutoMode()
			if err := c.fans.AllAuto(); err != nil {
				return nil, err
			}
			notes = append(notes, "fans on the firmware curve (saved profile is gone)")
			break
		}
		notes = append(notes, "fan curve '"+c.mode.Profile+"'")
	default:
		c.stopLoopLocked()
		if err := c.fans.AllAuto(); err != nil {
			return nil, err
		}
		notes = append(notes, "fans on the firmware curve")
	}

	note, err := c.RestoreBacklight()
	if err != nil {
		return nil, err
	}
	return append(notes, note), nil
}

// Shutdown runs when the daemon exits. A curve with nothing left to follow it
// is just a pinned duty, so that goes back to the firmware. A manual speed is
// something the user asked for explicitly and stays put, exactly as it does
// for every other way of setting one.
func (c *Controller) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLoopLocked()
	if c.mode.Kind == fan.CurveRun {
		if err := c.fans.AllAuto(); err != nil {
			log.Printf("could not hand the fans back to the firmware: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func (c *Controller) Status() (*proto.Status, error) {
	st := &proto.Status{}
	for _, f := range ec.Fans {
		duty, err := c.fans.Duty(f)
		if err != nil {
			return nil, err
		}
		rpm, err := c.fans.RPM(f)
		if err != nil {
			return nil, err
		}
		name := "CPU"
		if f == 2 {
			name = "GPU"
		}
		st.Fans = append(st.Fans, proto.FanStatus{
			ID: f, Name: name, Duty: duty, Percent: fan.RawToPct(duty), RPM: rpm,
		})
	}
	st.Temps = c.sensors.ReadAll()
	st.Hottest, st.HottestSource = fan.Hottest(st.Temps)

	c.mu.Lock()
	mode := c.mode
	c.mu.Unlock()
	st.Mode = mode.String()
	if note, _ := c.note.Load().(string); note != "" {
		st.Mode = note
	}
	st.Kind = string(mode.Kind)
	st.Profile = mode.Profile
	st.Manual = mode.Duty
	return st, nil
}

func (c *Controller) Profiles() *proto.Profiles {
	c.mu.Lock()
	cfg, mode := c.cfg, c.mode
	c.mu.Unlock()

	out := &proto.Profiles{Default: cfg.Profile, Current: cfg.Profile}
	if mode.Kind == fan.CurveRun {
		out.Current = mode.Profile
	}
	custom := map[string]string{}
	for _, cc := range cfg.Curves {
		custom[cc.Name] = cc.Points
	}
	for _, name := range cfg.ProfileNames() {
		info := proto.ProfileInfo{Name: name}
		if pts, ok := custom[name]; ok {
			info.Points = pts
		} else if pts, ok := fan.BuiltinPoints(name); ok {
			info.Points, info.Builtin = fan.FormatPoints(pts), true
		}
		out.Profiles = append(out.Profiles, info)
	}
	return out
}

// ---------------------------------------------------------------------------
// keyboard backlight
// ---------------------------------------------------------------------------

func (c *Controller) KbdState() *proto.KbdState {
	c.kbdMu.Lock()
	st, known := kbd.LoadState()
	c.kbdMu.Unlock()
	return &proto.KbdState{
		Percent: kbd.RawToPct(st.Brightness), Raw: st.Brightness,
		Zones: st.Zones, Known: known,
	}
}

func saveKbd(st kbd.State) {
	if err := kbd.SaveState(st); err != nil {
		log.Printf("could not save the backlight state: %v", err)
	}
}

func (c *Controller) setBrightnessRaw(raw int) (int, error) {
	c.kbdMu.Lock()
	defer c.kbdMu.Unlock()
	if err := c.bl.SetBrightness(raw); err != nil {
		return 0, err
	}
	st, _ := kbd.LoadState()
	if st.Brightness > 0 && raw == 0 {
		st.LastOn = st.Brightness
	}
	st.Brightness = raw
	saveKbd(st)
	return raw, nil
}

func (c *Controller) KbdBrightness(pct float64) (int, error) {
	if pct < 0 || pct > 100 || pct != pct {
		return 0, fmt.Errorf("brightness must be between 0 and 100, not %g", pct)
	}
	return c.setBrightnessRaw(kbd.PctToRaw(pct))
}

func (c *Controller) KbdOff() error {
	_, err := c.setBrightnessRaw(0)
	return err
}

// KbdOn returns to the level the backlight had before it was turned off.
func (c *Controller) KbdOn() (int, error) {
	c.kbdMu.Lock()
	st, _ := kbd.LoadState()
	c.kbdMu.Unlock()
	raw := st.Brightness
	if raw == 0 {
		raw = st.LastOn
	}
	if raw == 0 {
		raw = kbd.MaxBrightness
	}
	return c.setBrightnessRaw(raw)
}

func (c *Controller) KbdColor(rgb kbd.RGB, zone string) (*proto.KbdColorResult, error) {
	if !rgb.Valid() {
		return nil, errors.New("colour channels must be between 0 and 255")
	}
	if zone == "" {
		zone = "all"
	}
	c.kbdMu.Lock()
	defer c.kbdMu.Unlock()
	st, _ := kbd.LoadState()
	if zone == "all" {
		if err := c.bl.SetAll(rgb); err != nil {
			return nil, err
		}
		for _, n := range kbd.ZoneNames {
			st.Zones[n] = rgb
		}
	} else {
		id, ok := kbd.ZoneID(zone)
		if !ok {
			return nil, fmt.Errorf("unknown zone '%s' (left, middle, right or all)", zone)
		}
		if err := c.bl.SetZone(id, rgb); err != nil {
			return nil, err
		}
		st.Zones[zone] = rgb
	}
	res := &proto.KbdColorResult{Color: rgb, Zone: zone}
	// A colour is invisible at zero brightness; bring it up if needed.
	if st.Brightness == 0 && rgb != (kbd.RGB{}) {
		if err := c.bl.SetBrightness(kbd.MaxBrightness); err != nil {
			return nil, err
		}
		st.Brightness = kbd.MaxBrightness
		res.Raised = true
	}
	saveKbd(st)
	return res, nil
}

func (c *Controller) KbdEffect(name string) error {
	code, ok := kbd.Effects[name]
	if !ok {
		return fmt.Errorf("unknown effect '%s'", name)
	}
	return c.bl.Effect(code)
}

// RestoreBacklight reapplies the saved colours and brightness. With nothing
// saved it leaves the backlight alone rather than forcing a default onto it.
func (c *Controller) RestoreBacklight() (string, error) {
	c.kbdMu.Lock()
	defer c.kbdMu.Unlock()
	st, known := kbd.LoadState()
	if !known {
		return "backlight untouched (nothing saved)", nil
	}
	for name, rgb := range st.Zones {
		if id, ok := kbd.ZoneID(name); ok && rgb.Valid() {
			if err := c.bl.SetZone(id, rgb); err != nil {
				return "", err
			}
		}
	}
	if err := c.bl.SetBrightness(st.Brightness); err != nil {
		return "", err
	}
	return fmt.Sprintf("backlight %d%%", kbd.RawToPct(st.Brightness)), nil
}
