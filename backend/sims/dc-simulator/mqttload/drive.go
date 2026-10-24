// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// DriveSpec is the load a Drive applies.
type DriveSpec struct {
	// Rate is the total events per second across the fleet; each device publishes at
	// Rate/devices, staggered so the fleet's load is smooth rather than a burst per interval.
	Rate float64
	// Duration is how long to publish. The drive then waits for the publishes still in flight
	// (up to AckTimeout) so the accounting is complete.
	Duration time.Duration
	// MetricKey is the measurement every event carries; PadBytes adds an inert padding member
	// of that size to the payload (see sim.MeasurementPayload).
	MetricKey string
	PadBytes  int
	// ReportInterval is how often OnInterval is called and an Interval recorded; 0 disables.
	ReportInterval time.Duration
	OnInterval     func(Interval)
}

// Validate rejects a spec that cannot be run as asked.
func (s DriveSpec) Validate(devices int) error {
	if s.Rate <= 0 || math.IsNaN(s.Rate) || math.IsInf(s.Rate, 0) {
		return fmt.Errorf("rate %v must be a positive number of events per second", s.Rate)
	}
	if s.Duration <= 0 {
		return fmt.Errorf("duration %s must be positive", s.Duration)
	}
	if s.MetricKey == "" {
		return fmt.Errorf("a metric key is required")
	}
	if s.PadBytes < 0 {
		return fmt.Errorf("pad bytes %d is negative", s.PadBytes)
	}
	if s.ReportInterval < 0 {
		return fmt.Errorf("report interval %s is negative", s.ReportInterval)
	}
	if per := s.perDeviceInterval(devices); per < time.Millisecond {
		return fmt.Errorf("rate %.0f/s over %d devices is one event every %s per device: below the 1ms the sender can keep time at; add devices", s.Rate, devices, per)
	}
	return nil
}

func (s DriveSpec) perDeviceInterval(devices int) time.Duration {
	return time.Duration(float64(devices) / s.Rate * float64(time.Second))
}

// DriveResult is what a Drive applied. Start and End bracket the PUBLISHING (the window the
// storage reconciliation reads), and Drained is when the last in-flight publish ended.
type DriveResult struct {
	Start, End, Drained time.Time
	Intervals           []Interval
	TargetRate          float64
}

// Drive publishes spec.Rate events per second across the fleet for spec.Duration, open loop:
// every device ticks on its own schedule whatever the broker is doing, which is how a real
// fleet behaves and the only way to find where a broker stops keeping up. A tick that finds the
// session down, or MaxInflight publishes already waiting, is counted and dropped, not queued.
//
// Every publish's fate is filed in led as it ends: Accepted for a real PUBACK, Ambiguous for
// anything else. led may be nil.
func (f *Fleet) Drive(ctx context.Context, spec DriveSpec, led *sim.IdentityLedger) (DriveResult, error) {
	if err := spec.Validate(len(f.sessions)); err != nil {
		return DriveResult{}, err
	}
	interval := spec.perDeviceInterval(len(f.sessions))

	var wg sync.WaitGroup
	start := time.Now()
	stopAt := start.Add(spec.Duration)
	sendCtx, stopSending := context.WithDeadline(ctx, stopAt)
	defer stopSending()

	for _, s := range f.sessions {
		wg.Add(1)
		go func(s *session) {
			defer wg.Done()
			// Stagger the first tick across one interval so the fleet is spread evenly.
			offset := time.Duration(float64(interval) * float64(s.idx) / float64(len(f.sessions)))
			s.drive(sendCtx, ctx, spec, interval, start.Add(offset), led)
		}(s)
	}

	res := DriveResult{Start: start, TargetRate: spec.Rate}
	repDone := make(chan struct{})
	var repMu sync.Mutex
	if spec.ReportInterval > 0 {
		go func() {
			defer close(repDone)
			prev := f.Counters.Snapshot()
			t := time.NewTicker(spec.ReportInterval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					iv, cur := f.interval(prev, time.Since(start))
					prev = cur
					repMu.Lock()
					res.Intervals = append(res.Intervals, iv)
					repMu.Unlock()
					if spec.OnInterval != nil {
						spec.OnInterval(iv)
					}
				case <-sendCtx.Done():
					return
				}
			}
		}()
	} else {
		close(repDone)
	}

	<-sendCtx.Done()
	res.End = time.Now()
	if res.End.After(stopAt) {
		res.End = stopAt
	}
	wg.Wait() // every device has drained its in-flight publishes
	<-repDone
	res.Drained = time.Now()
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("drive aborted: %w", err)
	}
	return res, nil
}

// interval cuts the slice of the run since prev and starts a fresh latency window.
func (f *Fleet) interval(prev Snapshot, at time.Duration) (Interval, Snapshot) {
	cur := f.Counters.Snapshot()
	h := f.ivLat.Swap(&LatencyHist{})
	return Interval{
		AtSeconds:   at.Seconds(),
		Sent:        cur.Sent - prev.Sent,
		Acked:       cur.Acked - prev.Acked,
		Unconfirmed: cur.Unconfirmed - prev.Unconfirmed,
		NotSent:     (cur.NotSentDisconnected + cur.SkippedInflight) - (prev.NotSentDisconnected + prev.SkippedInflight),
		Up:          cur.Up,
		Inflight:    cur.Inflight,
		Losses:      cur.ConnectionLosses - prev.ConnectionLosses,
		P99Ms:       h.Percentiles().P99,
	}, cur
}

// drive is one device's open-loop sender and reaper, a single goroutine so its in-flight list
// needs no lock. sendCtx ends the sending; abort (the caller's context) ends everything, and
// whatever is still in flight then is settled as unknown.
func (s *session) drive(sendCtx, abort context.Context, spec DriveSpec, interval time.Duration, first time.Time, led *sim.IdentityLedger) {
	var q []*pending
	next := first
	k := 0
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	settle := func(p *pending, r result) {
		led.Record(s.settle(r), s.dev.Token, "Measurement", p.stamp)
	}
	// reap ends every publish at the head of the queue that has ended, in order, and reports
	// whether the new head is a token that closed without a PUBACK (poll it by the clock).
	reap := func(now time.Time) (polling bool) {
		for len(q) > 0 {
			done, r, poll := s.check(q[0], now)
			if !done {
				return poll
			}
			settle(q[0], r)
			q = q[1:]
		}
		return false
	}
	sending := true
	for sending || len(q) > 0 {
		now := time.Now()
		polling := reap(now)

		// The next wake-up: the next tick (while sending), the head's deadline, or a short
		// poll when the head's token closed without a PUBACK.
		var wake time.Time
		if sending {
			wake = next
		}
		var headDone <-chan struct{}
		if len(q) > 0 {
			if dl := q[0].deadline; wake.IsZero() || dl.Before(wake) {
				wake = dl
			}
			if polling {
				if p := now.Add(unconfirmedPoll); p.Before(wake) {
					wake = p
				}
			} else {
				headDone = q[0].tok.Done()
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(max(time.Until(wake), 0))

		var sendDone <-chan struct{}
		if sending {
			sendDone = sendCtx.Done()
		}
		select {
		case <-abort.Done():
			for _, p := range q {
				settle(p, result{err: abort.Err(), latency: time.Since(p.sentAt)})
			}
			return
		case <-sendDone:
			sending = false
			continue
		case <-headDone:
			continue
		case <-timer.C:
		}
		if !sending {
			continue
		}
		now = time.Now()
		if now.Before(next) {
			continue
		}
		// A tick: late by more than a whole interval means the sender fell behind; the missed
		// ticks are dropped so they cannot arrive as a burst.
		if late := now.Sub(next); late >= interval {
			missed := int64(late / interval)
			s.f.Counters.Overruns.Add(missed)
			next = next.Add(time.Duration(missed) * interval)
			k += int(missed)
		}
		k++
		next = next.Add(interval)
		if len(q) >= s.f.cfg.MaxInflight {
			s.f.Counters.SkippedInflight.Add(1)
			continue
		}
		stamp := sim.EventTimestamp()
		payload, err := sim.MeasurementPayload(map[string]float64{spec.MetricKey: value(s.idx, k)}, stamp, spec.PadBytes)
		var body []byte
		if err == nil {
			body, err = sim.MQTTEventBody(s.dev, "Measurement", stamp, payload)
		}
		if err != nil {
			s.noteErr(err)
			continue
		}
		p, err := s.send(body, stamp, now)
		if err != nil {
			s.f.Counters.NotSentDisconnected.Add(1)
			continue
		}
		q = append(q, p)
	}
}

// value is the reading device idx reports on its k-th tick: a smooth bounded wave, the same
// shape devicepulse emits, so a persisted row means the same on either transport.
func value(idx, k int) float64 {
	return 50 + 20*math.Sin(float64(k)*0.3+float64(idx))
}
