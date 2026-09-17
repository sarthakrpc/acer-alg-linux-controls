package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"alg/internal/config"
	"alg/internal/daemon"
	"alg/internal/ec/sim"
	"alg/internal/fan"
	"alg/internal/kbd"
	"alg/internal/paths"
)

// The test driver runs fyne.Do inline on whatever goroutine calls it, so the
// tests supply the mutual exclusion the real UI goroutine would.
var uiMu sync.Mutex

func onUI(fn func()) {
	uiMu.Lock()
	defer uiMu.Unlock()
	fn()
}

func init() { runOnUI = onUI }

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		ok := false
		onUI(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole stack short of real hardware: window logic -> link -> Unix socket
// -> daemon -> simulated EC.
func TestWindowDrivesDaemon(t *testing.T) {
	dir := t.TempDir()
	paths.StateDir = filepath.Join(dir, "state")
	paths.RunDir = filepath.Join(dir, "run")
	paths.Config = filepath.Join(dir, "absent.conf")

	bus := sim.New()
	ctl := daemon.NewController(bus, config.Defaults())
	srv, err := daemon.Listen(ctl)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close(); ctl.Shutdown() })

	a := test.NewApp()
	t.Cleanup(a.Quit)
	var u *ui
	onUI(func() {
		u = newUI(a)
		u.start()
	})
	f, k := &u.fans, &u.kbd

	eventually(t, "first status", 3*time.Second, func() bool {
		return f.mode.Selected == modeAuto && len(f.profile.Options) == 4 && f.profile.Selected == "balanced"
	})
	onUI(func() {
		if !f.slider.Disabled() || !f.profile.Disabled() {
			t.Error("in firmware mode the slider and profile list should be disabled")
		}
	})

	// Choosing manual sends the slider's current value straight away.
	onUI(func() { f.mode.SetSelected(modeManual) })
	eventually(t, "manual 60%", 3*time.Second, func() bool { return bus.Duty(1) == fan.PctToRaw(60) })
	onUI(func() {
		if f.slider.Disabled() {
			t.Error("the slider should be enabled in manual mode")
		}
	})

	// A drag: several values in quick succession, then release. Only the
	// debounced/final value needs to reach the EC.
	bus.Commands()
	onUI(func() {
		for _, v := range []float64{62, 70, 77} {
			f.slider.Value = v
			f.slider.OnChanged(v)
		}
		f.slider.Value = 80
		f.slider.OnChanged(80)
		f.slider.OnChangeEnded(80)
	})
	eventually(t, "manual 80%", 3*time.Second, func() bool { return bus.Duty(1) == fan.PctToRaw(80) && bus.Duty(2) == fan.PctToRaw(80) })
	time.Sleep(2 * debounce)
	if n := len(bus.Commands()); n != 2 {
		t.Errorf("a drag should cost one write per fan, saw %d commands", n)
	}

	// Curve mode with the profile list.
	onUI(func() { f.mode.SetSelected(modeCurve) })
	eventually(t, "curve running", 3*time.Second, func() bool {
		st, _ := ctl.Status()
		return st.Kind == "curve" && st.Profile == "balanced"
	})
	onUI(func() { f.profile.SetSelected("silent") })
	eventually(t, "profile switched", 3*time.Second, func() bool {
		st, _ := ctl.Status()
		return st.Profile == "silent"
	})

	// A change made elsewhere (the CLI, say) shows up in the window.
	if _, err := ctl.SetManual(35, 0, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "window follows an outside change", 8*time.Second, func() bool {
		return f.mode.Selected == modeManual && f.slider.Value == 35
	})

	// ...and following it must not have echoed anything back.
	if st, _ := ctl.Status(); st.Mode != "manual 35%" {
		t.Errorf("mode = %q", st.Mode)
	}

	onUI(func() { f.mode.SetSelected(modeAuto) })
	eventually(t, "back to firmware", 3*time.Second, func() bool { return bus.IsAuto(1) && bus.IsAuto(2) })

	// Keyboard page.
	onUI(func() { k.slider.SetValue(40) })
	eventually(t, "brightness 40%", 3*time.Second, func() bool { return ctl.KbdState().Raw == kbd.PctToRaw(40) })
	onUI(func() {
		k.zone.SetSelectedIndex(2)
		u.applyColor(kbd.RGB{255, 90, 0})
	})
	eventually(t, "middle zone orange", 3*time.Second, func() bool {
		zones := ctl.KbdState().Zones
		return zones["middle"] == kbd.RGB{255, 90, 0} && len(zones) == 1
	})
}
