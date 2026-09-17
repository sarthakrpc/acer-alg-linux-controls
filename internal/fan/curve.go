package fan

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type Point struct {
	Temp, Duty float64
}

// Profile is a named built-in curve.
type Profile struct {
	Name   string
	Points []Point
}

// Builtin lists the built-in profiles as temp:duty% pairs, in display order.
var Builtin = []Profile{
	{"silent", []Point{{50, 15}, {65, 25}, {75, 40}, {85, 70}, {92, 100}}},
	{"balanced", []Point{{45, 25}, {60, 40}, {72, 55}, {82, 75}, {90, 100}}},
	{"performance", []Point{{40, 35}, {55, 50}, {65, 70}, {75, 90}, {85, 100}}},
	{"max", []Point{{0, 100}}},
}

func BuiltinPoints(name string) ([]Point, bool) {
	for _, p := range Builtin {
		if p.Name == name {
			return p.Points, true
		}
	}
	return nil, false
}

func BuiltinNames() []string {
	names := make([]string, len(Builtin))
	for i, p := range Builtin {
		names[i] = p.Name
	}
	return names
}

type Curve struct {
	points  []Point
	minDuty float64
}

func NewCurve(points []Point, minDuty float64) (*Curve, error) {
	if len(points) == 0 {
		return nil, errors.New("a curve needs at least one point")
	}
	sorted := append([]Point(nil), points...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Temp < sorted[j].Temp })
	return &Curve{points: sorted, minDuty: minDuty}, nil
}

// ParsePoints reads "45:20, 60:35, ..." into points.
func ParsePoints(text string) ([]Point, error) {
	var pts []Point
	for _, chunk := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		t, d, ok := strings.Cut(chunk, ":")
		if !ok {
			return nil, fmt.Errorf("bad curve point '%s' (want <temp>:<duty%%>)", chunk)
		}
		temp, err1 := strconv.ParseFloat(t, 64)
		duty, err2 := strconv.ParseFloat(d, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad curve point '%s' (want <temp>:<duty%%>)", chunk)
		}
		pts = append(pts, Point{temp, duty})
	}
	return pts, nil
}

// PercentFor interpolates linearly between points and clamps outside them.
func (c *Curve) PercentFor(temp float64) float64 {
	pts := c.points
	pct := pts[len(pts)-1].Duty
	switch {
	case temp <= pts[0].Temp:
		pct = pts[0].Duty
	case temp < pts[len(pts)-1].Temp:
		for i := 0; i+1 < len(pts); i++ {
			a, b := pts[i], pts[i+1]
			if a.Temp <= temp && temp <= b.Temp {
				span := b.Temp - a.Temp
				if span == 0 {
					span = 1
				}
				pct = a.Duty + (b.Duty-a.Duty)*(temp-a.Temp)/span
				break
			}
		}
	}
	return math.Max(c.minDuty, math.Min(100, pct))
}

func FormatPoints(points []Point) string {
	parts := make([]string, len(points))
	for i, p := range points {
		parts[i] = fmt.Sprintf("%gC:%g%%", p.Temp, p.Duty)
	}
	return strings.Join(parts, ", ")
}

func (c *Curve) String() string { return FormatPoints(c.points) }
