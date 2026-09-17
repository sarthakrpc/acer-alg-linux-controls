package fan

import (
	"math"
	"testing"

	"alg/internal/paths"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

// Expected values were produced by the Python implementation this replaces.
func TestCurveMatchesPython(t *testing.T) {
	balanced, _ := BuiltinPoints("balanced")
	silent, _ := BuiltinPoints("silent")
	quiet, err := ParsePoints("55:12, 70:22, 80:45, 88:75, 93:100")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		points  []Point
		minDuty float64
		want    [][2]float64
	}{
		{"balanced", balanced, 10, [][2]float64{{20, 25}, {45, 25}, {52.5, 32.5}, {60, 40}, {66, 47.5},
			{72, 55}, {80, 71}, {82, 75}, {86, 87.5}, {90, 100}, {99, 100}}},
		{"quiet", quiet, 10, [][2]float64{{30, 12}, {55, 12}, {62, 16.6667}, {70, 22}, {75, 33.5},
			{84, 60}, {90.5, 87.5}, {93, 100}, {100, 100}}},
		{"silent min 20", silent, 20, [][2]float64{{40, 20}, {50, 20}, {60, 21.6667}, {65, 25}}},
	}
	for _, tc := range cases {
		c, err := NewCurve(tc.points, tc.minDuty)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range tc.want {
			if got := c.PercentFor(w[0]); !near(got, w[1]) {
				t.Errorf("%s: PercentFor(%g) = %g, want %g", tc.name, w[0], got, w[1])
			}
		}
	}
}

func TestCurveUnsortedAndBad(t *testing.T) {
	pts, err := ParsePoints("90:100 45:20,60:35")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewCurve(pts, 0)
	if got := c.PercentFor(52.5); !near(got, 27.5) {
		t.Errorf("unsorted points: got %g, want 27.5", got)
	}
	for _, bad := range []string{"45", "45:x", "a:b", "45:20:3x"} {
		if _, err := ParsePoints(bad); err == nil {
			t.Errorf("ParsePoints(%q) should fail", bad)
		}
	}
	if _, err := NewCurve(nil, 10); err == nil {
		t.Error("an empty curve should be rejected")
	}
}

func TestConversionsMatchPython(t *testing.T) {
	for pct, raw := range map[float64]int{0: 0, 10: 26, 12.5: 32, 30: 76, 50: 128, 70: 178, 99.9: 255, 100: 255, 150: 255, -5: 0} {
		if got := PctToRaw(pct); got != raw {
			t.Errorf("PctToRaw(%g) = %d, want %d", pct, got, raw)
		}
	}
	for raw, pct := range map[int]int{0: 0, 1: 0, 26: 10, 77: 30, 127: 50, 128: 50, 254: 100, 255: 100} {
		if got := RawToPct(raw); got != pct {
			t.Errorf("RawToPct(%d) = %d, want %d", raw, got, pct)
		}
	}
}

func TestModeRoundTrip(t *testing.T) {
	cases := map[string]Mode{
		"auto (EC default)":        AutoMode(),
		"manual 70%":               ManualAll(70),
		"manual 12.5%":             ManualAll(12.5),
		"manual fan1=50%":          {Kind: Manual, Duty: map[int]float64{1: 50}},
		"manual fan1=50% fan2=70%": {Kind: Manual, Duty: map[int]float64{1: 50, 2: 70}},
		"curve:quiet":              CurveMode("quiet"),
	}
	for text, mode := range cases {
		if got := mode.String(); got != text {
			t.Errorf("String() = %q, want %q", got, text)
		}
		if got := ParseMode(text + "\n").String(); got != text {
			t.Errorf("ParseMode(%q) round-tripped to %q", text, got)
		}
	}
	// Two fans pinned to the same speed is just "manual N%".
	same := Mode{Kind: Manual, Duty: map[int]float64{1: 40, 2: 40}}
	if got := same.String(); got != "manual 40%" {
		t.Errorf("uniform duty rendered as %q", got)
	}
}

func TestParseModeFallsBackToAuto(t *testing.T) {
	for _, text := range []string{"", "garbage", "EMERGENCY -> 100% (96C)", "manual", "manual fan9=50%", "curve:"} {
		if got := ParseMode(text); got.Kind != Auto {
			t.Errorf("ParseMode(%q) = %v, want auto", text, got)
		}
	}
}

func TestSaveLoadMode(t *testing.T) {
	paths.StateDir = t.TempDir()
	if _, ok := LoadMode(); ok {
		t.Error("nothing saved yet, but LoadMode reported a mode")
	}
	if err := SaveMode(CurveMode("mine")); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadMode()
	if !ok || got.Kind != CurveRun || got.Profile != "mine" {
		t.Errorf("LoadMode = %v, %v", got, ok)
	}
}

func TestHottest(t *testing.T) {
	if temp, src := Hottest(nil); temp != 100 || src != "none" {
		t.Errorf("no sensors must fail hot, got %g from %s", temp, src)
	}
	if temp, src := Hottest(map[string]float64{"cpu": 61, "gpu": 72.5, "ec": 44}); temp != 72.5 || src != "gpu" {
		t.Errorf("got %g from %s", temp, src)
	}
}
