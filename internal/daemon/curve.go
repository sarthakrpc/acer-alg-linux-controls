package daemon

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"alg/internal/config"
	"alg/internal/ec"
	"alg/internal/fan"
)

const (
	reassertAfter = 15 * time.Second // re-send the duty even when unchanged
	emergencyBand = 8.0              // degrees below the trip point to stand down
)

func seconds(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

// runCurve follows a fan curve until ctx is cancelled. It does not touch the
// fans on the way out: whoever stopped it decides what they do next.
func (c *Controller) runCurve(ctx context.Context, done chan<- struct{}, name string, curve *fan.Curve, s config.Settings) {
	defer close(done)

	interval, step := seconds(s.PollInterval), seconds(s.Step)
	// Exponential smoothing on the temperature feeding the curve. Core
	// temperatures are spiky; without this a momentary blip drags the target
	// up and the fans then sawtooth their way back down.
	alpha := 1.0
	if s.Smoothing > 0 {
		alpha = 1 - math.Exp(-s.PollInterval/s.Smoothing)
	}

	currentPct := func() float64 {
		duty, err := c.fans.Duty(ec.Fans[0])
		if err != nil {
			return 0
		}
		return float64(fan.RawToPct(duty))
	}

	var (
		// Start from whatever the fans are already doing, so starting the
		// curve does not itself cause a jolt.
		applied     = currentPct()
		target      float64
		haveTarget  bool
		smoothed    float64
		haveSmooth  bool
		lastSample  time.Time
		lastWrite   time.Time
		lastRaw     = -1
		inEmergency bool
		failures    int
	)

	write := func(raw int, now time.Time) {
		var err error
		for _, f := range ec.Fans {
			if e := c.fans.SetDuty(f, raw); e != nil {
				err = e
			}
		}
		if err != nil {
			// Leave lastRaw alone so the next pass tries again. The EC can
			// drop out briefly around suspend; that is not worth dying for.
			if failures++; failures == 1 || failures%60 == 0 {
				log.Printf("fan write failed (%d in a row): %v", failures, err)
			}
			return
		}
		failures, lastRaw, lastWrite = 0, raw, now
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		now := time.Now()

		if lastSample.IsZero() || now.Sub(lastSample) >= interval-step/4 {
			lastSample = now
			temp, src := c.sensors.Hottest()
			if haveSmooth {
				smoothed += alpha * (temp - smoothed)
			} else {
				smoothed, haveSmooth = temp, true
			}

			// The safety check reads the raw sensor - smoothing must never
			// sit in front of it.
			switch {
			case temp >= s.Emergency || (inEmergency && temp > s.Emergency-emergencyBand):
				if !inEmergency {
					inEmergency = true
					log.Printf("%.0fC on %s >= %.0fC, forcing 100%%", temp, src, s.Emergency)
					c.note.Store(fmt.Sprintf("EMERGENCY -> 100%% (%.0fC)", temp))
				}
				// Straight to full, no ramp: this is the one case where
				// being gentle with the bearings is the wrong trade. Handing
				// control to the firmware instead would be worse - its curve
				// is *less* aggressive than ours up here.
				write(255, now)
				applied = 100
			default:
				if inEmergency {
					inEmergency = false
					log.Printf("back to %.0fC, resuming the curve", temp)
					c.note.Store("")
					applied = currentPct()
				}
				wanted := curve.PercentFor(smoothed)
				// Only lower the target once it has clearly dropped, so the
				// fans do not hunt back and forth across a curve knee.
				if !haveTarget || wanted > target || target-wanted >= s.Hysteresis {
					target, haveTarget = wanted, true
				}
			}
		}

		if !inEmergency && haveTarget {
			// Move toward the target at a bounded rate rather than jumping.
			// Slamming to 100% the moment a core spikes is jarring, and the
			// spike is usually over before the extra airflow does anything.
			switch {
			case applied < target && s.RampUp > 0:
				applied = math.Min(target, applied+s.RampUp*s.Step)
			case applied > target && s.RampDown > 0:
				applied = math.Max(target, applied-s.RampDown*s.Step)
			default:
				applied = target
			}
			// Re-assert periodically even when settled, so a write lost to a
			// collision with firmware EC traffic cannot leave the fans stuck.
			if raw := fan.PctToRaw(applied); raw != lastRaw || now.Sub(lastWrite) >= reassertAfter {
				write(raw, now)
			}
		}

		// While ramping, wake every step. Once settled there is nothing to
		// do until the next sample is due, so sleep right through to it.
		wait := step
		if inEmergency || !haveTarget || applied == target {
			wait = max(10*time.Millisecond, interval-time.Since(lastSample))
		}
		timer.Reset(wait)
	}
}
