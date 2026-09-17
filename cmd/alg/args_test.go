package main

import (
	"io"
	"reflect"
	"testing"
)

// Options must be accepted anywhere on the line, as the argparse-based CLI
// this replaces allowed.
func TestParseAcceptsOptionsAnywhere(t *testing.T) {
	cases := []struct {
		args    []string
		wantFan int
		wantPos []string
	}{
		{[]string{"70", "--fan", "1"}, 1, []string{"70"}},
		{[]string{"--fan", "2", "70"}, 2, []string{"70"}},
		{[]string{"70", "--fan=2"}, 2, []string{"70"}},
		{[]string{"-fan", "1", "70"}, 1, []string{"70"}},
		{[]string{"70"}, 0, []string{"70"}},
		{[]string{"-5"}, 0, []string{"-5"}}, // a negative number is a value, not an option
		{[]string{"--quiet", "70", "--fan", "1"}, 1, []string{"70"}},
	}
	for _, tc := range cases {
		fs := newFlags("test", "", "")
		fs.SetOutput(io.Discard)
		fan := fs.Int("fan", 0, "")
		fs.Bool("quiet", false, "")
		if err := parse(fs, tc.args, 1, 1); err != nil {
			t.Errorf("%v: %v", tc.args, err)
			continue
		}
		if *fan != tc.wantFan || !reflect.DeepEqual(fs.Args(), tc.wantPos) {
			t.Errorf("%v: fan=%d args=%v, want fan=%d args=%v", tc.args, *fan, fs.Args(), tc.wantFan, tc.wantPos)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, args := range [][]string{{}, {"70", "80"}, {"70", "--bogus"}, {"70", "--fan", "x"}} {
		fs := newFlags("test", "", "")
		fs.SetOutput(io.Discard)
		fs.Int("fan", 0, "")
		if err := parse(fs, args, 1, 1); err == nil {
			t.Errorf("%v should be rejected", args)
		}
	}
}

func TestParsePercent(t *testing.T) {
	for text, want := range map[string]float64{"0": 0, "70": 70, "12.5": 12.5, "100%": 100} {
		if got, err := parsePercent(text); err != nil || got != want {
			t.Errorf("parsePercent(%q) = %g, %v", text, got, err)
		}
	}
	for _, bad := range []string{"", "abc", "-1", "101", "NaN"} {
		if _, err := parsePercent(bad); err == nil {
			t.Errorf("parsePercent(%q) should fail", bad)
		}
	}
}
