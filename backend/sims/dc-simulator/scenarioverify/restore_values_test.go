// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"github.com/devicechain-io/dc-simulator/sim"
	"testing"
	"time"
)

func TestRestoreEveryMetricValueAndMultiplicityInItsPartition(t *testing.T) {
	for _, refused := range []bool{false, true} {
		for _, metric := range []string{sim.HarborpulseChargerPowerKey, sim.HarborpulseSessionKey, sim.HarborpulseConnectorKey, sim.HarborpulseMaxKwKey} {
			for _, defect := range []string{"missing", "value", "duplicate", "timestamp"} {
				if refused && defect == "timestamp" {
					continue
				} // moving to a new instant also affects the never-refused partition
				t.Run(metric+"/"+defect+"/"+map[bool]string{false: "accepted", true: "refused"}[refused], func(t *testing.T) {
					want := InvBackfillStored
					if refused {
						want = InvBurstCaughtUp
					}
					// Corruption affects platform rows only; declarations remain immutable.
					evaluateOneRed(t, want, "", func(d *day) {
						b := &d.backfill[0]
						partition := restoreRefused(d.observe(), b.Device)
						for i, row := range b.Rows {
							if row.Name != metric || partition[row.At.UnixMicro()] != refused {
								continue
							}
							switch defect {
							case "missing":
								b.Rows = append(b.Rows[:i], b.Rows[i+1:]...)
							case "value":
								b.Rows[i].Value += 99
							case "duplicate":
								b.Rows = append(b.Rows, row)
							case "timestamp":
								b.Rows[i].At = row.At.Add(time.Microsecond)
							}
							return
						}
						t.Fatal("fixture has no metric in partition")
					})
				})
			}
		}
	}
}

func TestS5CoverageAnchorsPlatformStartAndObservationEnd(t *testing.T) {
	s := cleanDay().s5
	s.LiveSampled = true
	s.SampleFrom = at(26).Add(10 * time.Second) // cannot hide a missed beginning
	s.SampleTo = at(26).Add(20 * time.Second)
	for now := s.SampleFrom; !now.After(s.SampleTo); now = now.Add(time.Second) {
		s.Samples = append(s.Samples, OTASample{At: now, InFlight: 2})
	}
	if err := S5Coverage(s); err == nil {
		t.Fatal("missing rollout beginning claimed coverage")
	}
	s.Rollout.StartedAt = s.SampleFrom.Format(time.RFC3339Nano)
	if err := S5Coverage(s); err != nil {
		t.Fatal(err)
	}
	s.SampleTo = s.SampleTo.Add(10 * time.Second)
	if err := S5Coverage(s); err == nil {
		t.Fatal("missing observation tail claimed coverage")
	}
}
