package config

import (
	"os"
	"path/filepath"
	"testing"

	"alg/internal/paths"
)

func TestShippedConfig(t *testing.T) {
	paths.Config = "../../alg.conf"
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) != 0 {
		t.Errorf("the shipped config produced warnings: %v", s.Warnings)
	}
	if s.Profile != "balanced" || s.Emergency != 95 || s.RampDown != 3 || s.Smoothing != 6 || s.UseGPU != "auto" {
		t.Errorf("unexpected settings: %+v", s)
	}
	if len(s.Curves) != 2 || s.Curves[0].Name != "mine" || s.Curves[1].Name != "quiet" {
		t.Errorf("custom curves: %+v", s.Curves)
	}
	if _, err := s.Curve("quiet"); err != nil {
		t.Error(err)
	}
	if _, err := s.Curve("silent"); err != nil {
		t.Error(err)
	}
	if _, err := s.Curve("nope"); err == nil {
		t.Error("an unknown profile should be an error")
	}
	want := []string{"silent", "balanced", "performance", "max", "mine", "quiet"}
	got := s.ProfileNames()
	if len(got) != len(want) {
		t.Fatalf("ProfileNames = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ProfileNames = %v, want %v", got, want)
		}
	}
}

func TestMissingAndMalformed(t *testing.T) {
	paths.Config = filepath.Join(t.TempDir(), "absent.conf")
	s, err := Load()
	if err != nil || s.Profile != "balanced" || s.PollInterval != 2 {
		t.Fatalf("a missing file should yield the defaults, got %+v, %v", s, err)
	}

	paths.Config = filepath.Join(t.TempDir(), "alg.conf")
	text := "stray line\n[general]\nPROFILE : quiet\npoll_interval = fast\nstep_interval = 9\n" +
		"use_gpu_temp = maybe\n; comment\n[daemon]\nallow_groups = sudo, staff\n"
	if err := os.WriteFile(paths.Config, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Profile != "quiet" {
		t.Errorf("keys are case-insensitive and ':' separates too; profile = %q", s.Profile)
	}
	if s.PollInterval != 2 {
		t.Errorf("a non-numeric value must keep the default, got %g", s.PollInterval)
	}
	if s.Step != s.PollInterval {
		t.Errorf("step_interval must be clamped to poll_interval, got %g", s.Step)
	}
	if s.UseGPU != "auto" {
		t.Errorf("bad use_gpu_temp must fall back to auto, got %q", s.UseGPU)
	}
	if len(s.AllowGroups) != 2 || s.AllowGroups[1] != "staff" {
		t.Errorf("allow_groups = %v", s.AllowGroups)
	}
	if len(s.Warnings) != 3 {
		t.Errorf("want 3 warnings, got %v", s.Warnings)
	}
}
