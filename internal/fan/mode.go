package fan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"alg/internal/ec"
	"alg/internal/paths"
)

type Kind string

const (
	Auto     Kind = "auto"
	Manual   Kind = "manual"
	CurveRun Kind = "curve"
)

// Mode is what the user asked the fans to do. It is the single thing that is
// persisted, and what gets put back at boot and after resume.
type Mode struct {
	Kind Kind
	// Duty pins fans to a percentage in manual mode. A fan that is absent is
	// left to the firmware.
	Duty    map[int]float64
	Profile string
}

func AutoMode() Mode { return Mode{Kind: Auto} }

func ManualAll(pct float64) Mode {
	duty := map[int]float64{}
	for _, f := range ec.Fans {
		duty[f] = pct
	}
	return Mode{Kind: Manual, Duty: duty}
}

func CurveMode(profile string) Mode { return Mode{Kind: CurveRun, Profile: profile} }

// uniform reports the shared percentage when every fan is pinned to it.
func (m Mode) uniform() (float64, bool) {
	if len(m.Duty) != len(ec.Fans) {
		return 0, false
	}
	first := m.Duty[ec.Fans[0]]
	for _, f := range ec.Fans {
		if m.Duty[f] != first {
			return 0, false
		}
	}
	return first, true
}

func fmtPct(p float64) string { return strconv.FormatFloat(p, 'g', -1, 64) }

// String renders the mode in the on-disk format, which is also what status
// output shows. The first three forms predate the Go rewrite and must keep
// parsing:
//
//	auto (EC default)
//	manual 70%
//	manual fan1=70%
//	manual fan1=50% fan2=70%
//	curve:quiet
func (m Mode) String() string {
	switch m.Kind {
	case Manual:
		if pct, ok := m.uniform(); ok {
			return "manual " + fmtPct(pct) + "%"
		}
		fans := make([]int, 0, len(m.Duty))
		for f := range m.Duty {
			fans = append(fans, f)
		}
		sort.Ints(fans)
		parts := make([]string, len(fans))
		for i, f := range fans {
			parts[i] = fmt.Sprintf("fan%d=%s%%", f, fmtPct(m.Duty[f]))
		}
		return "manual " + strings.Join(parts, " ")
	case CurveRun:
		return "curve:" + m.Profile
	default:
		return "auto (EC default)"
	}
}

var (
	reManualAll = regexp.MustCompile(`^manual\s+([\d.]+)%`)
	reManualOne = regexp.MustCompile(`fan(\d)=([\d.]+)%`)
	reCurve     = regexp.MustCompile(`^curve:(\S+)`)
)

// ParseMode reads a saved mode. Anything unrecognised means the firmware
// should be in charge.
func ParseMode(text string) Mode {
	text = strings.TrimSpace(text)
	if m := reManualAll.FindStringSubmatch(text); m != nil {
		if pct, err := strconv.ParseFloat(m[1], 64); err == nil {
			return ManualAll(pct)
		}
	}
	if strings.HasPrefix(text, "manual") {
		duty := map[int]float64{}
		for _, m := range reManualOne.FindAllStringSubmatch(text, -1) {
			f, _ := strconv.Atoi(m[1])
			pct, err := strconv.ParseFloat(m[2], 64)
			if err == nil && validFan(f) {
				duty[f] = pct
			}
		}
		if len(duty) > 0 {
			return Mode{Kind: Manual, Duty: duty}
		}
	}
	if m := reCurve.FindStringSubmatch(text); m != nil {
		return CurveMode(m[1])
	}
	return AutoMode()
}

func validFan(f int) bool {
	for _, n := range ec.Fans {
		if n == f {
			return true
		}
	}
	return false
}

// ValidFan reports whether f names a fan this machine has.
func ValidFan(f int) bool { return validFan(f) }

// LoadMode reads the persisted mode; ok is false when nothing was saved.
func LoadMode() (Mode, bool) {
	b, err := os.ReadFile(paths.FanMode())
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return AutoMode(), false
	}
	return ParseMode(string(b)), true
}

// SaveMode persists the mode so it survives a reboot.
func SaveMode(m Mode) error {
	return WriteFileAtomic(paths.FanMode(), []byte(m.String()+"\n"))
}

// WriteFileAtomic replaces path in one step, so a crash or power cut mid-write
// can never leave a truncated state file behind.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
