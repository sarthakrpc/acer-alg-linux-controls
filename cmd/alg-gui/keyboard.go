package main

import (
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"alg/internal/kbd"
	"alg/internal/proto"
)

var presetColors = []struct {
	name string
	rgb  kbd.RGB
}{
	{"White", kbd.RGB{255, 255, 255}}, {"Red", kbd.RGB{255, 0, 0}}, {"Green", kbd.RGB{0, 255, 0}},
	{"Blue", kbd.RGB{0, 0, 255}}, {"Cyan", kbd.RGB{0, 255, 255}}, {"Magenta", kbd.RGB{255, 0, 255}},
	{"Orange", kbd.RGB{255, 90, 0}}, {"Purple", kbd.RGB{160, 32, 240}},
}

var zoneChoices = []string{"All", "Left", "Middle", "Right"}

type kbdTab struct {
	slider   *widget.Slider
	sliderAt *widget.Label
	zone     *widget.Select
	current  *swatch
	timer    *time.Timer
	lastSent float64
}

func toColor(c kbd.RGB) color.Color {
	return color.NRGBA{uint8(c[0]), uint8(c[1]), uint8(c[2]), 255}
}

// swatch is a tappable block of colour.
type swatch struct {
	widget.BaseWidget
	rect  *canvas.Rectangle
	onTap func()
}

func newSwatch(c kbd.RGB, size fyne.Size, onTap func()) *swatch {
	s := &swatch{rect: canvas.NewRectangle(toColor(c)), onTap: onTap}
	s.rect.StrokeColor = color.NRGBA{0, 0, 0, 90}
	s.rect.StrokeWidth = 1
	s.rect.CornerRadius = 4
	s.rect.SetMinSize(size)
	s.ExtendBaseWidget(s)
	return s
}

func (s *swatch) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(s.rect) }
func (s *swatch) Tapped(*fyne.PointEvent)             { s.onTap() }
func (s *swatch) Cursor() desktop.Cursor              { return desktop.PointerCursor }

func (s *swatch) set(c kbd.RGB) {
	s.rect.FillColor = toColor(c)
	s.rect.Refresh()
}

func (u *ui) buildKbdTab() fyne.CanvasObject {
	k := &u.kbd
	k.lastSent = -1

	k.sliderAt = widget.NewLabel("  0%")
	k.sliderAt.TextStyle.Monospace = true
	k.slider = widget.NewSlider(0, 100)
	k.slider.Step = 1
	k.slider.OnChanged = u.onKbdSlider
	k.slider.OnChangeEnded = func(float64) { u.flushKbdBrightness() }

	levels := container.NewGridWithColumns(4)
	for _, level := range []struct {
		label string
		pct   float64
	}{{"Off", 0}, {"Low", 25}, {"Medium", 60}, {"Full", 100}} {
		levels.Add(widget.NewButton(level.label, func() { k.slider.SetValue(level.pct) }))
	}
	brightness := widget.NewCard("Brightness", "", container.NewVBox(
		container.NewBorder(nil, nil, nil, k.sliderAt, k.slider), levels))

	k.zone = widget.NewSelect(zoneChoices, nil)
	k.zone.SetSelectedIndex(0)
	k.current = newSwatch(kbd.RGB{255, 255, 255}, fyne.NewSize(56, 30), u.pickColor)
	pick := widget.NewButton("Pick colour...", u.pickColor)

	presets := container.NewGridWithColumns(len(presetColors))
	for _, p := range presetColors {
		presets.Add(newSwatch(p.rgb, fyne.NewSize(48, 30), func() { u.applyColor(p.rgb) }))
	}

	colour := widget.NewCard("Colour", "", container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Zone:"), container.NewHBox(k.current, pick), k.zone),
		presets,
		widget.NewButton("Run colour test (red, green, blue, ...)", u.runColorTest),
		note("The EC cannot report backlight state, so these show what was last set here - "+
			"not a hardware reading."),
	))
	return container.NewVBox(brightness, colour)
}

func (u *ui) onKbdSlider(v float64) {
	k := &u.kbd
	k.sliderAt.SetText(fmt.Sprintf("%3.0f%%", v))
	if u.syncing {
		return
	}
	if k.timer != nil {
		k.timer.Stop()
	}
	k.timer = time.AfterFunc(debounce, func() { runOnUI(u.flushKbdBrightness) })
}

func (u *ui) flushKbdBrightness() {
	k := &u.kbd
	if k.timer != nil {
		k.timer.Stop()
		k.timer = nil
	}
	if u.syncing || k.slider.Value == k.lastSent {
		return
	}
	k.lastSent = k.slider.Value
	u.link.send(proto.Request{Op: proto.OpKbdBrightness, Percent: proto.Pct(k.slider.Value)}, u.ack)
}

func (u *ui) selectedZone() string {
	switch u.kbd.zone.SelectedIndex() {
	case 1:
		return "left"
	case 2:
		return "middle"
	case 3:
		return "right"
	}
	return "all"
}

func (u *ui) pickColor() {
	picker := dialog.NewColorPicker("Keyboard backlight colour", "", func(c color.Color) {
		r, g, b, _ := c.RGBA()
		u.applyColor(kbd.RGB{int(r >> 8), int(g >> 8), int(b >> 8)})
	}, u.win)
	picker.Advanced = true
	picker.SetColor(u.kbd.current.rect.FillColor)
	picker.Show()
}

func (u *ui) applyColor(rgb kbd.RGB) {
	u.kbd.current.set(rgb)
	req := proto.Request{Op: proto.OpKbdColor, R: rgb[0], G: rgb[1], B: rgb[2], Zone: u.selectedZone()}
	fetch(u.link, req, func(res *proto.KbdColorResult, err error) {
		if u.fail(err) {
			return
		}
		if res.Raised {
			// The daemon turned the backlight up so the colour is visible.
			u.loadKbdState()
		}
	})
}

func (u *ui) runColorTest() {
	go func() {
		for _, step := range kbd.TestSequence {
			rgb := step.Color
			runOnUI(func() {
				u.setStatus("Colour test: " + step.Name)
				u.kbd.current.set(rgb)
				u.link.send(proto.Request{Op: proto.OpKbdColor, R: rgb[0], G: rgb[1], B: rgb[2], Zone: "all"}, nil)
			})
			time.Sleep(2 * time.Second)
		}
		runOnUI(func() { u.setStatus("Colour test finished") })
	}()
}

func (u *ui) loadKbdState() {
	fetch(u.link, proto.Request{Op: proto.OpKbdState}, func(st *proto.KbdState, err error) {
		if err != nil || !st.Known {
			return
		}
		k := &u.kbd
		u.syncing = true
		k.slider.SetValue(float64(st.Percent))
		k.lastSent = float64(st.Percent)
		u.syncing = false
		k.sliderAt.SetText(fmt.Sprintf("%3d%%", st.Percent))
		for _, name := range kbd.ZoneNames {
			if rgb, ok := st.Zones[name]; ok {
				k.current.set(rgb)
				break
			}
		}
	})
}
