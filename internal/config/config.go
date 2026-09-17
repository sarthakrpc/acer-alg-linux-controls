// Package config reads /etc/alg.conf.
//
// The format is the INI dialect the file has always used: [sections],
// "key = value" (or "key: value") pairs, and whole-line comments starting
// with # or ;. Keys are case-insensitive.
package config

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"alg/internal/fan"
	"alg/internal/paths"
)

const (
	rampUpDefault    = 10.0 // percentage points per second, rising
	rampDownDefault  = 3.0  // ...and falling, deliberately gentler
	stepDefault      = 0.5  // how often the duty is nudged
	emergencyDefault = 95.0 // TjMax on this CPU is 100C
	smoothingDefault = 6.0  // temperature EMA time constant, seconds
)

// CustomCurve is a [curve.<name>] section.
type CustomCurve struct {
	Name   string
	Points string
}

type Settings struct {
	Profile      string
	PollInterval float64
	Emergency    float64
	MinDuty      float64
	Hysteresis   float64
	RampUp       float64
	RampDown     float64
	Step         float64
	Smoothing    float64
	UseGPU       string
	AllowGroups  []string
	Curves       []CustomCurve

	// Warnings lists lines and values that were ignored while loading.
	Warnings []string
}

func Defaults() *Settings {
	return &Settings{
		Profile:      "balanced",
		PollInterval: 2.0,
		Emergency:    emergencyDefault,
		MinDuty:      10,
		Hysteresis:   3,
		RampUp:       rampUpDefault,
		RampDown:     rampDownDefault,
		Step:         stepDefault,
		Smoothing:    smoothingDefault,
		UseGPU:       "auto",
		AllowGroups:  []string{"sudo", "wheel", "alg"},
	}
}

type section struct {
	name string
	keys map[string]string
}

func parse(path string) ([]section, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var (
		sections []section
		warnings []string
		current  = -1
	)
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			sections = append(sections, section{
				name: strings.TrimSpace(line[1 : len(line)-1]),
				keys: map[string]string{},
			})
			current = len(sections) - 1
			continue
		}
		i := strings.IndexAny(line, "=:")
		if i <= 0 || current < 0 {
			warnings = append(warnings, fmt.Sprintf("%s:%d: ignored '%s'", path, n, line))
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		sections[current].keys[key] = strings.TrimSpace(line[i+1:])
	}
	return sections, warnings, sc.Err()
}

// Load reads the config file. A missing file is not an error: the defaults
// are a complete configuration.
func Load() (*Settings, error) {
	s := Defaults()
	sections, warnings, err := parse(paths.Config)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	s.Warnings = warnings

	for _, sec := range sections {
		switch {
		case sec.name == "general":
			s.general(sec.keys)
		case sec.name == "daemon":
			if v, ok := sec.keys["allow_groups"]; ok {
				s.AllowGroups = splitList(v)
			}
		case strings.HasPrefix(sec.name, "curve."):
			if pts, ok := sec.keys["points"]; ok {
				s.Curves = append(s.Curves, CustomCurve{sec.name[len("curve."):], pts})
			}
		}
	}

	s.PollInterval = math.Max(0.2, s.PollInterval)
	// Duty is nudged on this cadence; sensors are still only read every
	// PollInterval, so smooth ramping costs no extra sensor traffic.
	s.Step = math.Max(0.1, math.Min(s.Step, s.PollInterval))
	if s.UseGPU != "auto" && s.UseGPU != "on" && s.UseGPU != "off" {
		s.Warnings = append(s.Warnings, "use_gpu_temp must be auto, on or off; using auto")
		s.UseGPU = "auto"
	}
	return s, nil
}

func (s *Settings) general(keys map[string]string) {
	num := func(key string, dst *float64) {
		text, ok := keys[key]
		if !ok {
			return
		}
		v, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s = %s is not a number; keeping %g", key, text, *dst))
			return
		}
		*dst = v
	}
	if v, ok := keys["profile"]; ok && v != "" {
		s.Profile = v
	}
	if v, ok := keys["use_gpu_temp"]; ok {
		s.UseGPU = strings.ToLower(v)
	}
	num("poll_interval", &s.PollInterval)
	num("emergency_temp", &s.Emergency)
	num("min_duty", &s.MinDuty)
	num("hysteresis", &s.Hysteresis)
	num("ramp_up", &s.RampUp)
	num("ramp_down", &s.RampDown)
	num("step_interval", &s.Step)
	num("temp_smoothing", &s.Smoothing)
}

func splitList(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}

// Curve resolves a profile name. A [curve.<name>] section wins over a
// built-in of the same name.
func (s *Settings) Curve(name string) (*fan.Curve, error) {
	for _, c := range s.Curves {
		if c.Name == name {
			pts, err := fan.ParsePoints(c.Points)
			if err != nil {
				return nil, fmt.Errorf("profile '%s': %w", name, err)
			}
			return fan.NewCurve(pts, s.MinDuty)
		}
	}
	if pts, ok := fan.BuiltinPoints(name); ok {
		return fan.NewCurve(pts, s.MinDuty)
	}
	return nil, fmt.Errorf("unknown profile '%s' (available: %s)",
		name, strings.Join(s.ProfileNames(), ", "))
}

// ProfileNames lists the built-ins followed by any custom curves.
func (s *Settings) ProfileNames() []string {
	names := fan.BuiltinNames()
	for _, c := range s.Curves {
		if _, builtin := fan.BuiltinPoints(c.Name); !builtin {
			names = append(names, c.Name)
		}
	}
	return names
}
