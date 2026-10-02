// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"testing"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventlimit"
)

func TestReadingCeilingKeepsTheRateAndFloorsTheBurstAtOneEvent(t *testing.T) {
	cases := []struct {
		in, want core.TenantCeiling
	}{
		// A tier whose burst is below one full event is floored, so a 256-reading event
		// is shed on rate, never permanently on its size. The rate is untouched (factor 1).
		{core.TenantCeiling{RatePerSecond: 10, Burst: 20}, core.TenantCeiling{RatePerSecond: 10, Burst: eventlimit.MaxReadingsPerEvent}},
		{core.TenantCeiling{RatePerSecond: 1000, Burst: 2000}, core.TenantCeiling{RatePerSecond: 1000, Burst: 2000}},
		{core.TenantCeiling{RatePerSecond: 5, Burst: 256}, core.TenantCeiling{RatePerSecond: 5, Burst: 256}},
		{core.TenantCeiling{RatePerSecond: 5, Burst: 0}, core.TenantCeiling{RatePerSecond: 5, Burst: 256}},
		// The contention floor's hard drop stays a hard drop: no 256 free readings.
		{core.TenantCeiling{RatePerSecond: 0, Burst: 0}, core.TenantCeiling{RatePerSecond: 0, Burst: 0}},
	}
	for _, c := range cases {
		got := ReadingCeiling(func(string) core.TenantCeiling { return c.in })("t")
		if got != c.want {
			t.Errorf("ReadingCeiling(%+v) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// A hard drop through a real limiter admits nothing, not even one full event.
func TestReadingCeilingKeepsAHardDropAHardDrop(t *testing.T) {
	l := core.NewTenantRateLimiter(ReadingCeiling(core.StaticCeiling(0, 0)))
	if l.AllowN("t", 1) {
		t.Fatal("a reading limiter over a zero ceiling admitted a reading")
	}
}

func TestReadingCeilingCarriesTheSource(t *testing.T) {
	for _, src := range []core.CeilingSource{core.CeilingPending, core.CeilingResolved, core.CeilingStatic,
		core.CeilingUnreachable, core.CeilingUnknownTenant} {
		got := ReadingCeiling(func(string) core.TenantCeiling {
			return core.TenantCeiling{RatePerSecond: 1, Burst: 1, Source: src}
		})("t")
		if got.Source != src {
			t.Errorf("Source %v came back as %v", src, got.Source)
		}
	}
}

func TestReadingCeilingRefusesANilResolver(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ReadingCeiling(nil) did not panic")
		}
	}()
	ReadingCeiling(nil)
}

func TestIngestIsDeclaredInReadings(t *testing.T) {
	if Ingest.RateUnit != "readings/sec" {
		t.Errorf("Ingest.RateUnit = %q, want readings/sec", Ingest.RateUnit)
	}
}
