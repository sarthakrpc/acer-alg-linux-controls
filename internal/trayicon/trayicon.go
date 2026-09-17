// Package trayicon draws the system tray icon: the hottest temperature as a
// number on a dark plate.
//
// The previous GTK build showed the temperature as an AppIndicator text label
// next to its icon. The portable StatusNotifierItem protocol has no such
// label, so the number is drawn into the icon itself, which works on every
// desktop. The dark plate keeps it legible on light and dark panels alike.
//
// This is deliberately plain Go with no GUI toolkit behind it: the tray runs
// all day, and should cost next to nothing while it does.
package trayicon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Size is the edge of the icon in pixels. Panels scale it down to their own
// icon size.
const Size = 64

var (
	plateColor = color.NRGBA{0x1b, 0x20, 0x27, 0xf0}
	coolColor  = color.NRGBA{0xf2, 0xf5, 0xf7, 0xff}
	warmColor  = color.NRGBA{0xff, 0xc1, 0x5a, 0xff}
	hotColor   = color.NRGBA{0xff, 0x6b, 0x5e, 0xff}
	dimColor   = color.NRGBA{0x8a, 0x93, 0x9c, 0xff}
)

// TempColor grades the number: neutral, amber from 70C, red from 85C.
func TempColor(temp int) color.NRGBA {
	switch {
	case temp >= 85:
		return hotColor
	case temp >= 70:
		return warmColor
	}
	return coolColor
}

var (
	parsed    *opentype.Font
	parseOnce sync.Once
)

func face(size float64) (font.Face, error) {
	var err error
	parseOnce.Do(func() { parsed, err = opentype.Parse(gobold.TTF) })
	if err != nil || parsed == nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

// plate is a rounded rectangle with anti-aliased corners.
func plate(size int) *image.NRGBA {
	half := float64(size) / 2
	radius := half * 3 / 8
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Distance from the pixel centre to the rounded outline.
			dx := math.Max(math.Abs(float64(x)+0.5-half)-(half-radius), 0)
			dy := math.Max(math.Abs(float64(y)+0.5-half)-(half-radius), 0)
			cover := math.Min(1, math.Max(0, radius-math.Hypot(dx, dy)+0.5))
			if cover > 0 {
				c := plateColor
				c.A = uint8(float64(c.A) * cover)
				img.SetNRGBA(x, y, c)
			}
		}
	}
	return img
}

func render(size int, text string, col color.NRGBA) ([]byte, error) {
	points := float64(size) * 44 / 64
	if len(text) >= 3 {
		points = float64(size) / 2
	}
	f, err := face(points)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img := plate(size)
	d := font.Drawer{Dst: img, Src: image.NewUniform(col), Face: f}
	// Centre on the glyphs' actual ink rather than the font's line metrics,
	// which would leave digits sitting visibly low.
	bounds, _ := d.BoundString(text)
	d.Dot = fixed.Point26_6{
		X: (fixed.I(size)-(bounds.Max.X-bounds.Min.X))/2 - bounds.Min.X,
		Y: (fixed.I(size)-(bounds.Max.Y-bounds.Min.Y))/2 - bounds.Min.Y,
	}
	d.DrawString(text)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Temperature renders the icon for a reading in degrees C.
func Temperature(temp int) ([]byte, error) {
	return render(Size, strconv.Itoa(temp), TempColor(temp))
}

// Offline renders the icon shown while the daemon cannot be reached. A stale
// temperature would be worse than none.
func Offline() ([]byte, error) {
	return render(Size, "--", dimColor)
}
