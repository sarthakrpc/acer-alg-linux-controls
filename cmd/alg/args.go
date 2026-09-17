package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"alg/internal/proto"
)

const callTimeout = 10 * time.Second

func newFlags(name, synopsis, desc string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintf(out, "usage: %s %s\n", name, strings.TrimSpace("[options] "+synopsis))
		if desc != "" {
			fmt.Fprintf(out, "\n%s\n", desc)
		}
		hasFlags := false
		fs.VisitAll(func(*flag.Flag) { hasFlags = true })
		if hasFlags {
			fmt.Fprintln(out, "\noptions:")
			fs.PrintDefaults()
		}
	}
	return fs
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// parse accepts options anywhere on the line, as the previous argparse-based
// CLI did ("alg fan set 70 --fan 1"); the flag package on its own stops at
// the first positional argument.
func parse(fs *flag.FlagSet, args []string, minPos, maxPos int) error {
	var opts, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' || isNumber(a) {
			pos = append(pos, a)
			continue
		}
		opts = append(opts, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			opts = append(opts, args[i])
		}
	}

	if err := fs.Parse(append(append(opts, "--"), pos...)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		return usageError{} // the flag package has already explained itself
	}
	if n := fs.NArg(); n < minPos || n > maxPos {
		fs.Usage()
		return usageError{}
	}
	return nil
}

// call makes a single request on a fresh connection.
func call(req proto.Request, out any) error {
	return callTimed(req, callTimeout, out)
}

func callTimed(req proto.Request, timeout time.Duration, out any) error {
	c, err := proto.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(req, timeout, out)
}

func parsePercent(text string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSuffix(text, "%"), 64)
	if err != nil || !(v >= 0 && v <= 100) { // written this way round so NaN fails too
		return 0, usagef("'%s' is not a percentage between 0 and 100", text)
	}
	return v, nil
}
