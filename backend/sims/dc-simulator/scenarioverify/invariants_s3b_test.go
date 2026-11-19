// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/scenariosink"
	"github.com/devicechain-io/dc-simulator/sim"
)

func (d *day) cleanS3b() {
	d.s3.Changes = []sim.ThresholdChange{{Day: 0, Device: sim.HarborpulseAttributePump, At: at(sim.HarborpulseAttributeChangeHour), Old: 1, New: 1.8}}
	// Continue the pumps' stored trajectories with refill/drain cycles after the initial
	// commanded response. This leaves the first command's measured effect intact.
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		levels := map[int64]float64{}
		for i, r := range d.readings[p] {
			if r.Name == sim.HarborpulseWetWellKey && r.At.After(at(18.6)) {
				phase := int(r.At.Sub(at(18.6))/time.Second) % 90
				v := 0.7
				if phase < 30 {
					v = 1.4
				} else if phase < 60 {
					v = 2.1
				}
				d.readings[p][i].Value = v
				levels[r.At.UnixMicro()] = v
			}
		}
		for i, r := range d.readings[p] {
			if r.Name == sim.HarborpulseWellFillKey {
				if v, ok := levels[r.At.UnixMicro()]; ok {
					d.readings[p][i].Value = v / 4 * 100
				}
			}
		}
	}
	p := sim.HarborpulseAttributePump
	d.attributeAlarms(true)
	d.commands = append(d.commands, Command{Device: sim.HarborpulseSamplingGauge, Name: sim.HarborpulseSamplingCommand, Status: "SUCCESSFUL", Queued: at(16.5), Responded: at(16.6)})
	d.s3.Sink.QueueItems = []scenariosink.QueuedDownlink{{At: at(16.51), Tenant: testTenant, ID: "queue-id", DevEUI: sim.HarborpulseSamplingDevEUI, FPort: 12, Hex: "000f", Confirmed: true}}
	var kept []Reading
	for _, r := range d.readings[sim.HarborpulseSamplingGauge] {
		if r.At.Before(at(16.6)) {
			kept = append(kept, r)
		}
	}
	for t := at(16.6); t.Before(at(WatchTo)); t = t.Add(15 * time.Second) {
		v, _ := sim.HarborpulseLoraLevelAt(1, sim.HarborpulseSamplingGauge, t.Sub(testClock.StartedAt), testClock.DayLength)
		kept = append(kept, Reading{sim.HarborpulseWaterLevelKey, t, v})
	}
	d.readings[sim.HarborpulseSamplingGauge] = kept
	d.s3.Jobs = []CalculatedJob{{Token: "backfill-id", Status: "SUCCEEDED", ProfileToken: sim.HarborpulsePumpProfile, From: at(sim.HarborpulseGapFrom), To: at(sim.HarborpulseGapTo), Samples: 6, Written: 6}}
	for t := at(sim.HarborpulseGapFrom); t.Before(at(sim.HarborpulseGapTo)); t = t.Add(5 * time.Second) {
		r := Reading{sim.HarborpulseWetWellKey, t, 0.8}
		d.s3.GapBefore = append(d.s3.GapBefore, r)
		d.readings[p] = append(d.readings[p], r, Reading{sim.HarborpulseWellFillKey, t, 20})
	}
}

func TestEachS3bControlTurnsExactlyItsInvariantRed(t *testing.T) {
	controls := []struct {
		defect, want, because string
		mutate                func(*day)
	}{
		{sim.HarborpulseDefectAttributeNotUpdated, InvAttributeThresholdMoves, "shared threshold", func(d *day) {
			d.attributeAlarms(false)
		}},
		{sim.HarborpulseDefectDownlinkNack, InvDownlinkAcknowledged, "SUCCESSFUL", func(d *day) {
			for i := range d.commands {
				if d.commands[i].Name == sim.HarborpulseSamplingCommand {
					d.commands[i].Status = "FAILED"
				}
			}
			before := len(d.readings[sim.HarborpulseSamplingGauge])
			var rs []Reading
			for tick := at(WatchFrom).Add(time.Second); tick.Before(at(WatchTo)); tick = tick.Add(5 * time.Second) {
				value, _ := sim.HarborpulseLoraLevelAt(1, sim.HarborpulseSamplingGauge, tick.Sub(testClock.StartedAt), testClock.DayLength)
				rs = append(rs, Reading{sim.HarborpulseWaterLevelKey, tick, value})
			}
			d.readings[sim.HarborpulseSamplingGauge] = rs
			// Every additional uplink still decodes successfully; the profile's health grows too.
			h := d.s3.Health[sim.HarborpulseLoraGaugeProfile]
			h.Decoded += float64(len(rs) - before)
			d.s3.Health[sim.HarborpulseLoraGaugeProfile] = h
		}},
		{sim.HarborpulseDefectBackfillWrongWindow, InvBackfillCompletes, "missing span", func(d *day) {
			// s3bBeats requests [gapFrom - 2 minutes, gapFrom - 1 minute), not a
			// shifted half-hour of sim time. The restored constant lets that whole minute
			// fill (twelve five-second samples), leaving the intended gap untouched.
			d.s3.Jobs[0].From = at(sim.HarborpulseGapFrom).Add(-2 * time.Minute)
			d.s3.Jobs[0].To = at(sim.HarborpulseGapFrom).Add(-time.Minute)
			d.s3.Jobs[0].Samples, d.s3.Jobs[0].Written = 12, 12
			var kept []Reading
			for _, r := range d.readings[sim.HarborpulseBasinPump] {
				if r.Name != sim.HarborpulseWellFillKey || r.At.Before(at(sim.HarborpulseGapFrom)) || !r.At.Before(at(sim.HarborpulseGapTo)) {
					kept = append(kept, r)
				}
			}
			d.readings[sim.HarborpulseBasinPump] = kept
		}},
	}
	for _, c := range controls {
		t.Run(c.defect, func(t *testing.T) {
			if Controls[c.want] != c.defect {
				t.Fatal("control not registered")
			}
			evaluateOneRed(t, c.want, c.because, c.mutate)
			d := cleanDay()
			c.mutate(d)
			switch c.defect {
			case sim.HarborpulseDefectDownlinkNack:
				clean := cleanDay()
				delta := len(d.readings[sim.HarborpulseSamplingGauge]) - len(clean.readings[sim.HarborpulseSamplingGauge])
				if delta <= 0 || d.s3.Health[sim.HarborpulseLoraGaugeProfile].Decoded != clean.s3.Health[sim.HarborpulseLoraGaugeProfile].Decoded+float64(delta) {
					t.Fatal("NACK must keep faster uplinks and increase decoded health by the extra uplinks")
				}
			case sim.HarborpulseDefectBackfillWrongWindow:
				j := d.s3.Jobs[0]
				if !j.From.Equal(at(12)) || !j.To.Equal(at(13)) || j.Samples != 12 || j.Written != 12 || j.Status != "SUCCEEDED" {
					t.Fatalf("wrong-window job does not model the sim's earlier minute: %+v", j)
				}
				from, to := at(sim.HarborpulseGapFrom), at(sim.HarborpulseGapTo).Add(-time.Microsecond)
				if len(readingsOf(d.observe(), sim.HarborpulseBasinPump, sim.HarborpulseWellFillKey, from, to)) != 0 ||
					len(readingsOf(d.observe(), sim.HarborpulseBasinPump, sim.HarborpulseWetWellKey, from, to)) != len(d.s3.GapBefore) {
					t.Fatal("wrong-window job must leave the intended gap's inputs and no calculated rows")
				}
			}
		})
	}
}

func TestS3bGuardsRejectIncompleteEvidence(t *testing.T) {
	t.Run("backfill-status", func(t *testing.T) {
		o := cleanDay().observe()
		o.S3.Jobs[0].Status = "RUNNING"
		if backfillCompletes(o) == nil {
			t.Fatal("running job accepted")
		}
	})
	t.Run("queue-after-ack", func(t *testing.T) {
		o := cleanDay().observe()
		o.S3.Sink.QueueItems[0].At = at(18)
		if downlinkAcknowledged(o) == nil {
			t.Fatal("queue arriving after command acknowledgement accepted")
		}
	})

	t.Run("threshold", func(t *testing.T) {
		o := cleanDay().observe()
		o.S3.Changes = nil
		if attributeThresholdMoves(o) == nil {
			t.Fatal("missing change accepted")
		}
	})
	t.Run("queue-bytes", func(t *testing.T) {
		o := cleanDay().observe()
		o.S3.Sink.QueueItems[0].Hex = "0005"
		if downlinkAcknowledged(o) == nil {
			t.Fatal("wrong bytes accepted")
		}
	})
	t.Run("cadence", func(t *testing.T) {
		o := cleanDay().observe()
		for i := range o.Readings[sim.HarborpulseSamplingGauge] {
			r := &o.Readings[sim.HarborpulseSamplingGauge][i]
			if r.At.After(at(17)) {
				r.At = r.At.Add(time.Duration(i%2) * time.Second)
			}
		}
		if downlinkAcknowledged(o) == nil {
			t.Fatal("wrong cadence accepted")
		}
	})
	t.Run("backfill-rows", func(t *testing.T) {
		o := cleanDay().observe()
		for i := range o.Readings[sim.HarborpulseBasinPump] {
			r := &o.Readings[sim.HarborpulseBasinPump][i]
			if r.Name == sim.HarborpulseWellFillKey && inWindow(r.At, at(14), at(14.5)) {
				r.Value++
			}
		}
		if backfillCompletes(o) == nil {
			t.Fatal("wrong calculated value accepted")
		}
	})
}

func (d *day) attributeAlarms(updated bool) {
	var kept []lifecycle
	for _, l := range d.alarms {
		if l.key != sim.HarborpulseAttributeAlarm {
			kept = append(kept, l)
		}
	}
	d.alarms = kept
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		rs := readingsOf(Observations{Readings: d.readings}, p, sim.HarborpulseWetWellKey, at(17), at(21.5))
		active := false
		var steps []step
		start := 0.0
		serial := 0
		for t := at(17); t.Before(at(21.5)); t = t.Add(5 * time.Second) {
			level := -1.0
			latest := time.Time{}
			for _, r := range rs {
				if !r.At.After(t) && r.At.After(latest) {
					level = r.Value
					latest = r.At
				}
			}
			threshold := sim.HarborpulseOldThreshold(p)
			if updated && p == sim.HarborpulseAttributePump && !t.Before(at(18.5)) {
				threshold = 1.8
			}
			on := level > threshold
			if on && !active {
				start = 6 + t.Sub(testClock.StartedAt).Minutes()
				steps = []step{{start, minor}}
			}
			if !on && active {
				d.alarms = append(d.alarms, lifecycle{fmt.Sprintf("attr-%s-%d", p, serial), p, sim.HarborpulseAttributeAlarm, steps, 6 + t.Sub(testClock.StartedAt).Minutes()})
				serial++
			}
			active = on
		}
		if active {
			d.alarms = append(d.alarms, lifecycle{fmt.Sprintf("attr-%s-%d", p, serial), p, sim.HarborpulseAttributeAlarm, steps, 21.5})
		}
	}
}

func TestBackfillCompletesWithMicrosecondJobBounds(t *testing.T) {
	o := cleanDay().observe()
	o.Clock.StartedAt = o.Clock.StartedAt.Add(789 * time.Nanosecond)
	o.S3.Jobs[0].From = o.at(sim.HarborpulseGapFrom).Truncate(time.Microsecond)
	o.S3.Jobs[0].To = o.at(sim.HarborpulseGapTo).Truncate(time.Microsecond)
	if err := backfillCompletes(o); err != nil {
		t.Fatal(err)
	}
}

// Backfill ranges are half-open. The clean fixture must not pretend the job wrote its upper
// boundary, or retain counters for rows outside the requested range.
func TestBackfillFixtureUsesHalfOpenWindow(t *testing.T) {
	d := cleanDay()
	j := d.s3.Jobs[0]
	o := Observations{Readings: d.readings}
	wet := readingsOf(o, sim.HarborpulseBasinPump, sim.HarborpulseWetWellKey, j.From, j.To.Add(-time.Microsecond))
	fill := readingsOf(o, sim.HarborpulseBasinPump, sim.HarborpulseWellFillKey, j.From, j.To.Add(-time.Microsecond))
	if len(d.s3.GapBefore) != len(wet) || j.Samples != float64(len(wet)) || j.Written != float64(len(fill)) {
		t.Fatalf("backfill counters/input snapshot exceed half-open range: %+v, wet=%d fill=%d before=%d", j, len(wet), len(fill), len(d.s3.GapBefore))
	}
	if len(readingsOf(o, sim.HarborpulseBasinPump, sim.HarborpulseWellFillKey, j.To, j.To)) != 0 {
		t.Fatal("backfill wrote its excluded upper boundary")
	}
}
