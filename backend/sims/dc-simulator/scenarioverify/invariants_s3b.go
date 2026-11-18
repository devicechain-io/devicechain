// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const (
	InvAttributeThresholdMoves = "attribute-threshold-moves"
	InvDownlinkAcknowledged    = "downlink-acknowledged"
	InvBackfillCompletes       = "backfill-completes"
)

func init() {
	Controls[InvAttributeThresholdMoves] = sim.HarborpulseDefectAttributeNotUpdated
	Controls[InvDownlinkAcknowledged] = sim.HarborpulseDefectDownlinkNack
	Controls[InvBackfillCompletes] = sim.HarborpulseDefectBackfillWrongWindow
}

// attributeThresholdMoves judges settled regions of STORED wet well readings against the alarm
// list, including the band between old and new bounds on both sides of the write. A transition
// gets twelve seconds to propagate; it cannot count as evidence on either side of a crossing.
func attributeThresholdMoves(o Observations) error {
	var change *sim.ThresholdChange
	for i := range o.S3.Changes {
		c := &o.S3.Changes[i]
		if c.Day == o.Day && c.Device == sim.HarborpulseAttributePump {
			change = c
		}
	}
	if change == nil || change.New != sim.HarborpulseNewThreshold || change.Old != sim.HarborpulseOldThreshold(sim.HarborpulseAttributePump) || !inWindow(change.At, o.at(sim.HarborpulseAttributeChangeHour), o.at(sim.HarborpulseAttributeChangeHour).Add(20*time.Second)) {
		return fmt.Errorf("missing threshold change beat")
	}
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		rs := readingsOf(o, p, sim.HarborpulseWetWellKey, o.at(17), o.at(21.5))
		bandBefore, bandAfter, high, low := 0, 0, 0, 0
		for _, poll := range o.Polls {
			if !inWindow(poll, o.at(17), o.at(21.5)) || (poll.After(change.At) && poll.Sub(change.At) < 20*time.Second) {
				continue
			}
			threshold := sim.HarborpulseOldThreshold(p)
			after := poll.After(change.At)
			if p == sim.HarborpulseAttributePump && after {
				threshold = change.New
			}
			min, max, n := math.Inf(1), math.Inf(-1), 0
			first, last := time.Time{}, time.Time{}
			for _, r := range rs {
				if inWindow(r.At, poll.Add(-17*time.Second), poll) {
					if first.IsZero() || r.At.Before(first) {
						first = r.At
					}
					if r.At.After(last) {
						last = r.At
					}
					min = math.Min(min, r.Value)
					max = math.Max(max, r.Value)
					n++
				}
			}
			if n < 3 || last.Sub(first) < 10*time.Second || poll.Sub(last) > 7*time.Second {
				continue
			}
			want := min > threshold+0.03
			if !want && max >= threshold-0.03 {
				continue
			}
			active := false
			for _, a := range o.Alarms {
				if a.Device == p && a.Key == sim.HarborpulseAttributeAlarm && a.At.Equal(poll) && a.State == sim.AlarmStateActiveWire {
					active = true
				}
			}
			if active != want {
				return fmt.Errorf("%s: shared threshold %.2f, stored wet well %.2f..%.2f at %s, active=%v want %v", p, threshold, min, max, poll.Format(time.RFC3339), active, want)
			}
			if want {
				high++
			} else {
				low++
			}
			if p == sim.HarborpulseAttributePump && min > change.Old+0.03 && max < change.New-0.03 {
				if after {
					bandAfter++
				} else {
					bandBefore++
				}
			}
		}
		if high == 0 || low == 0 {
			return fmt.Errorf("%s: no stored evidence both above and below its own shared threshold", p)
		}
		if p == sim.HarborpulseAttributePump && (bandBefore == 0 || bandAfter == 0) {
			return fmt.Errorf("%s: no old/new threshold band evidence before (%d) and after (%d) change", p, bandBefore, bandAfter)
		}
	}
	return nil
}

func downlinkAcknowledged(o Observations) error {
	var commands []Command
	for _, c := range o.Commands {
		if c.Device == sim.HarborpulseSamplingGauge && c.Name == sim.HarborpulseSamplingCommand && inWindow(c.Queued, o.at(16.5), o.at(17)) {
			commands = append(commands, c)
		}
	}
	if len(commands) != 1 || commands[0].Status != "SUCCESSFUL" || commands[0].Responded.IsZero() {
		return fmt.Errorf("sampling command: want exactly one SUCCESSFUL acknowledged command, got %+v", commands)
	}
	cmd := commands[0]
	queued := 0
	for _, q := range o.S3.Sink.QueueItems {
		if q.Tenant == o.S3.Tenant && q.DevEUI == sim.HarborpulseSamplingDevEUI && inWindow(q.At, cmd.Queued, o.end()) {
			queued++
			if q.At.After(cmd.Responded) || q.FPort != sim.HarborpulseSamplingPort || q.Hex != "000f" || !q.Confirmed || q.ID == "" {
				return fmt.Errorf("sampling queue item has wrong port, bytes or confirmation: %+v", q)
			}
		}
	}
	if queued != 1 {
		return fmt.Errorf("sampling queue: got %d items, want exactly one", queued)
	}
	rs := readingsOf(o, sim.HarborpulseSamplingGauge, sim.HarborpulseWaterLevelKey, o.at(WatchFrom), o.end())
	sort.Slice(rs, func(i, j int) bool { return rs[i].At.Before(rs[j].At) })
	var before, after []float64
	for i := 1; i < len(rs); i++ {
		gap := rs[i].At.Sub(rs[i-1].At).Seconds()
		if rs[i].At.Before(cmd.Queued) {
			before = append(before, gap)
		}
		if rs[i-1].At.After(cmd.Responded.Add(responseWithin)) {
			after = append(after, gap)
		}
	}
	if len(before) < 3 || len(after) < 4 {
		return fmt.Errorf("sampling cadence has too few stored intervals before/after acknowledgement")
	}
	sort.Float64s(before)
	baseline := before[len(before)/2]
	if baseline >= sim.HarborpulseSamplingSeconds {
		return fmt.Errorf("original cadence %.2fs cannot demonstrate a move to %ds", baseline, sim.HarborpulseSamplingSeconds)
	}
	for _, gap := range after {
		if gap < sim.HarborpulseSamplingSeconds-0.5 || gap > sim.HarborpulseSamplingSeconds+baseline+2 {
			return fmt.Errorf("stored sampling interval %.2fs after acknowledgement, want %ds", gap, sim.HarborpulseSamplingSeconds)
		}
	}
	return nil
}

// CalculatedJob is the platform's job record, never a simulator completion counter.
type CalculatedJob struct {
	Token, Status, ProfileToken string
	From, To                    time.Time
	Samples, Written            float64
}

func backfillCompletes(o Observations) error {
	from, to := o.at(sim.HarborpulseGapFrom), o.at(sim.HarborpulseGapTo)
	before := 0
	for _, r := range o.S3.GapBefore {
		if inWindow(r.At, from, to.Add(-time.Microsecond)) {
			if r.Name == sim.HarborpulseWellFillKey {
				return fmt.Errorf("gap already had calculated values before backfill")
			}
			if r.Name == sim.HarborpulseWetWellKey {
				before++
			}
		}
	}
	if before < 3 {
		return fmt.Errorf("backfill gap has only %d stored input readings before the job", before)
	}
	jobs := 0
	for _, j := range o.S3.Jobs {
		if j.ProfileToken == sim.HarborpulsePumpProfile && j.From.Equal(from) && j.To.Equal(to) {
			jobs++
			if j.Status != "SUCCEEDED" || j.Samples <= 0 || j.Written <= 0 {
				return fmt.Errorf("backfill %s: status %s, samples %.0f, written %.0f", j.Token, j.Status, j.Samples, j.Written)
			}
		}
	}
	if jobs != 1 {
		return fmt.Errorf("backfill: want one successful job over the missing span, got %d", jobs)
	}
	wet := readingsOf(o, sim.HarborpulseBasinPump, sim.HarborpulseWetWellKey, from, to.Add(-time.Microsecond))
	fill := readingsOf(o, sim.HarborpulseBasinPump, sim.HarborpulseWellFillKey, from, to.Add(-time.Microsecond))
	values := map[int64]float64{}
	for _, r := range fill {
		values[r.At.UnixMicro()] = r.Value
	}
	if len(wet) < before {
		return fmt.Errorf("backfill lost stored wet well inputs")
	}
	for _, r := range wet {
		v, ok := values[r.At.UnixMicro()]
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v-r.Value/sim.HarborpulseWellDepthM*100) > 0.01 {
			return fmt.Errorf("backfill missing or wrong calculated value at %s", r.At.Format(time.RFC3339Nano))
		}
	}
	return nil
}
