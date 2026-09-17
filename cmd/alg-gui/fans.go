package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"alg/internal/proto"
)

const (
	modeAuto   = "Automatic (firmware)"
	modeManual = "Manual speed"
	modeCurve  = "Automatic curve (alg daemon)"
)

type fansTab struct {
	bars  map[int]*widget.ProgressBar
	rpm   map[int]*widget.Label
	temps *widget.Label

	mode     *widget.RadioGroup
	slider   *widget.Slider
	sliderAt *widget.Label
	profile  *widget.Select

	timer     *time.Timer
	pending   bool      // a debounced slider value has not been sent yet
	lastTouch time.Time // when the user last moved the slider
	lastSent  float64
}

func note(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	l.SizeName = theme.SizeNameCaptionText
	return l
}

func (u *ui) buildFansTab() fyne.CanvasObject {
	f := &u.fans
	f.bars, f.rpm = map[int]*widget.ProgressBar{}, map[int]*widget.Label{}
	f.lastSent = -1

	rows := container.New(layout.NewFormLayout())
	for _, fan := range []struct {
		id   int
		name string
	}{{1, "CPU fan"}, {2, "GPU fan"}} {
		bar := widget.NewProgressBar()
		bar.Max = 100
		bar.TextFormatter = func() string { return fmt.Sprintf("%.0f%%", bar.Value) }
		rpm := widget.NewLabel("    - rpm")
		rpm.TextStyle.Monospace = true // fixed width, so the bar does not jitter
		f.bars[fan.id], f.rpm[fan.id] = bar, rpm
		rows.Add(widget.NewLabel(fan.name))
		rows.Add(container.NewBorder(nil, nil, nil, rpm, bar))
	}
	f.temps = widget.NewLabel("")
	f.temps.SizeName = theme.SizeNameCaptionText
	f.temps.Wrapping = fyne.TextWrapWord
	current := widget.NewCard("Current", "", container.NewVBox(rows, f.temps))

	f.mode = widget.NewRadioGroup([]string{modeAuto, modeManual, modeCurve}, u.onModeChosen)
	f.mode.Required = true

	f.sliderAt = widget.NewLabel(" 60%")
	f.sliderAt.TextStyle.Monospace = true
	f.slider = widget.NewSlider(0, 100)
	f.slider.Step = 1
	f.slider.Value = 60
	f.slider.OnChanged = u.onFanSlider
	f.slider.OnChangeEnded = func(float64) { u.flushFanSpeed() }
	f.slider.Disable()

	f.profile = widget.NewSelect(nil, u.onProfileChosen)
	f.profile.PlaceHolder = "(loading)"
	f.profile.Disable()

	control := widget.NewCard("Control", "", container.NewVBox(
		f.mode,
		container.NewBorder(nil, nil, nil, f.sliderAt, f.slider),
		container.NewBorder(nil, nil, widget.NewLabel("Profile:"), nil, f.profile),
		note("These settings stay put - closing the window, quitting from the tray, "+
			"sleeping and rebooting all keep them. They only change when you change them."),
	))
	return container.NewVBox(current, control)
}

func (u *ui) setFanControls(mode string) {
	f := &u.fans
	if mode == modeManual {
		f.slider.Enable()
	} else {
		f.slider.Disable()
	}
	if mode == modeCurve {
		f.profile.Enable()
	} else {
		f.profile.Disable()
	}
}

func (u *ui) onModeChosen(mode string) {
	u.setFanControls(mode)
	if u.syncing {
		return
	}
	switch mode {
	case modeManual:
		u.fans.lastSent = -1 // always send: the fans are not at this speed yet
		u.flushFanSpeed()
	case modeCurve:
		// An empty profile asks the daemon for the configured default.
		u.link.send(proto.Request{Op: proto.OpFanCurve, Profile: u.fans.profile.Selected}, u.ack)
	default:
		u.link.send(proto.Request{Op: proto.OpFanAuto}, u.ack)
	}
}

func (u *ui) onFanSlider(v float64) {
	f := &u.fans
	f.sliderAt.SetText(fmt.Sprintf("%3.0f%%", v))
	if u.syncing || f.mode.Selected != modeManual {
		return
	}
	// Debounce: dragging fires continuously and each write hits the EC.
	f.lastTouch = time.Now()
	f.pending = true
	if f.timer != nil {
		f.timer.Stop()
	}
	f.timer = time.AfterFunc(debounce, func() { runOnUI(u.flushFanSpeed) })
}

func (u *ui) flushFanSpeed() {
	f := &u.fans
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.pending = false
	if u.syncing || f.mode.Selected != modeManual || f.slider.Value == f.lastSent {
		return
	}
	f.lastSent = f.slider.Value
	f.lastTouch = time.Now()
	u.link.send(proto.Request{Op: proto.OpFanSet, Percent: proto.Pct(f.slider.Value)}, u.ack)
}

func (u *ui) onProfileChosen(name string) {
	if u.syncing || u.fans.mode.Selected != modeCurve || name == "" {
		return
	}
	u.link.send(proto.Request{Op: proto.OpFanCurve, Profile: name}, u.ack)
}

func (u *ui) loadProfiles() {
	fetch(u.link, proto.Request{Op: proto.OpProfiles}, func(p *proto.Profiles, err error) {
		if err != nil {
			return // the status poll reports connection trouble
		}
		names := make([]string, len(p.Profiles))
		for i, info := range p.Profiles {
			names[i] = info.Name
		}
		u.syncing = true
		u.fans.profile.PlaceHolder = "(choose a profile)"
		u.fans.profile.SetOptions(names)
		u.fans.profile.SetSelected(p.Current)
		u.syncing = false
	})
}

var prettySensor = map[string]string{"cpu": "CPU", "gpu": "GPU", "ec": "Board"}

func (u *ui) applyFanStatus(st *proto.Status) {
	f := &u.fans
	for _, fan := range st.Fans {
		if bar, ok := f.bars[fan.ID]; ok {
			bar.SetValue(float64(fan.Percent))
			f.rpm[fan.ID].SetText(fmt.Sprintf("%5d rpm", fan.RPM))
		}
	}

	keys := make([]string, 0, len(st.Temps))
	for k := range st.Temps {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return st.Temps[keys[i]] > st.Temps[keys[j]] })
	var parts []string
	for _, k := range keys[:min(5, len(keys))] {
		name := k
		if p, ok := prettySensor[k]; ok {
			name = p
		}
		parts = append(parts, fmt.Sprintf("%s %.0f°C", name, st.Temps[k]))
	}
	f.temps.SetText(strings.Join(parts, "    "))

	u.syncing = true
	defer func() { u.syncing = false }()

	mode := modeAuto
	switch st.Kind {
	case "manual":
		mode = modeManual
	case "curve":
		mode = modeCurve
	}
	if f.mode.Selected != mode {
		f.mode.SetSelected(mode)
	}
	u.setFanControls(mode)

	switch st.Kind {
	case "manual":
		// Track the real setting (it may have been changed from the CLI),
		// but never yank the handle out from under an in-progress drag.
		if !f.pending && time.Since(f.lastTouch) > 2500*time.Millisecond {
			commanded := -1.0
			for _, pct := range st.Manual {
				commanded = max(commanded, pct)
			}
			if commanded >= 0 && f.slider.Value != commanded {
				f.slider.SetValue(commanded)
				f.lastSent = commanded
			}
		}
	case "curve":
		if st.Profile != "" && f.profile.Selected != st.Profile {
			f.profile.SetSelected(st.Profile)
		}
	}
}
