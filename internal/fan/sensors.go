package fan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"alg/internal/ec"
)

// Sensors gathers the cheap, always-available temperature sources.
//
// The discrete GPU is only queried when it is already awake - polling
// nvidia-smi would otherwise pull it out of runtime suspend and cost battery
// life for nothing.
type Sensors struct {
	bus      ec.Bus
	useGPU   string // auto | on | off
	cpuInput string
	zones    []string
	gpuPCI   string

	mu       sync.Mutex // the curve loop and status requests share the cache
	gpuAt    time.Time
	gpuTemp  float64
	gpuValid bool
}

func NewSensors(bus ec.Bus, useGPU string) *Sensors {
	return &Sensors{
		bus:      bus,
		useGPU:   useGPU,
		cpuInput: findCoretempPackage(),
		zones:    findZones("SEN2", "SEN3", "TCPU"),
		gpuPCI:   findNvidia(),
	}
}

func (s *Sensors) SetUseGPU(mode string) {
	s.mu.Lock()
	s.useGPU = mode
	s.mu.Unlock()
}

func readTrim(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

func findCoretempPackage() string {
	hwmons, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, hw := range hwmons {
		if name, ok := readTrim(filepath.Join(hw, "name")); !ok || name != "coretemp" {
			continue
		}
		labels, _ := filepath.Glob(filepath.Join(hw, "temp*_label"))
		sort.Strings(labels)
		for _, label := range labels {
			if text, ok := readTrim(label); ok && strings.Contains(text, "Package") {
				return strings.TrimSuffix(label, "_label") + "_input"
			}
		}
	}
	return ""
}

func findZones(types ...string) []string {
	var found []string
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	sort.Strings(zones)
	for _, z := range zones {
		typ, ok := readTrim(filepath.Join(z, "type"))
		if !ok {
			continue
		}
		for _, want := range types {
			if typ == want {
				found = append(found, filepath.Join(z, "temp"))
			}
		}
	}
	return found
}

func findNvidia() string {
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, dev := range devs {
		if vendor, ok := readTrim(filepath.Join(dev, "vendor")); !ok || vendor != "0x10de" {
			continue
		}
		if class, ok := readTrim(filepath.Join(dev, "class")); ok && strings.HasPrefix(class, "0x0300") {
			return dev
		}
	}
	return ""
}

func (s *Sensors) gpu() (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.useGPU == "off" || s.gpuPCI == "" {
		return 0, false
	}
	if s.useGPU == "auto" {
		if status, ok := readTrim(filepath.Join(s.gpuPCI, "power/runtime_status")); !ok || status != "active" {
			return 0, false
		}
	}
	if !s.gpuAt.IsZero() && time.Since(s.gpuAt) < 4*time.Second {
		return s.gpuTemp, s.gpuValid
	}
	s.gpuAt = time.Now()
	s.gpuValid = false
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=temperature.gpu", "--format=csv,noheader").Output()
	if err == nil {
		line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		if v, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			s.gpuTemp, s.gpuValid = float64(v), true
		}
	}
	return s.gpuTemp, s.gpuValid
}

func readMilli(path string) (float64, bool) {
	text, ok := readTrim(path)
	if !ok {
		return 0, false
	}
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return float64(v) / 1000, true
}

// ReadAll returns every plausible temperature, keyed by source.
func (s *Sensors) ReadAll() map[string]float64 {
	temps := map[string]float64{}
	if s.cpuInput != "" {
		if v, ok := readMilli(s.cpuInput); ok {
			temps["cpu"] = v
		}
	}
	for i, z := range s.zones {
		if v, ok := readMilli(z); ok {
			temps["zone"+strconv.Itoa(i)] = v
		}
	}
	if b, err := s.bus.Read(ec.RegTemp, 1); err == nil {
		temps["ec"] = float64(b[0])
	}
	if v, ok := s.gpu(); ok {
		temps["gpu"] = v
	}
	// Drop obviously bogus readings from unpopulated sensors.
	for k, v := range temps {
		if v <= 5 || v >= 125 {
			delete(temps, k)
		}
	}
	return temps
}

// Hottest picks the highest reading out of temps.
func Hottest(temps map[string]float64) (float64, string) {
	if len(temps) == 0 {
		return 100, "none" // fail hot, never fail silent
	}
	best, source := -1.0, ""
	for k, v := range temps {
		if v > best || (v == best && k < source) {
			best, source = v, k
		}
	}
	return best, source
}

func (s *Sensors) Hottest() (float64, string) {
	return Hottest(s.ReadAll())
}
