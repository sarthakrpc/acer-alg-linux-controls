package main

import (
	"fmt"
	"strings"
	"time"

	"alg/internal/proto"
)

// resumeMain asks the daemon to reapply the saved settings.
//
// The EC forgets both the backlight and the fan mode across suspend, and
// nothing running in userspace can prevent that, so restoring has to be
// driven from outside: the systemd sleep hook calls this on wake. Boot needs
// no equivalent - the daemon restores as it starts.
func resumeMain(args []string) error {
	fs := newFlags("alg resume", "", "Reapply saved fan and backlight settings (after resume).")
	retries := fs.Int("retries", 5, "how many times to retry while the EC wakes up")
	quiet := fs.Bool("quiet", false, "print nothing on success")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}

	var res proto.ResumeResult
	// Each retry costs the daemon well under a second; leave it room to
	// finish rather than hanging up on it mid-restore.
	timeout := callTimeout + time.Duration(*retries)*time.Second
	if err := callTimed(proto.Request{Op: proto.OpResume, Retries: *retries}, timeout, &res); err != nil {
		return err
	}
	if !*quiet {
		fmt.Println("alg: restored " + strings.Join(res.Notes, "; "))
	}
	return nil
}
