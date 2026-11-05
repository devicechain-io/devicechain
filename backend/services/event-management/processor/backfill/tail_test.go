// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
)

// The floor is the LATER of the retention floor and the event-time floor (plus slack): with no
// retention it is the event-time floor, pinned here; with a retention longer than the platform
// accepts it must not reach back to retention (5 years would be ~1,800 refreshes a pass); with a
// shorter one nothing older is kept, so it is retention.
func TestTheTailFloorIsTheLaterOfRetentionAndTheEventTimeFloor(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	chunk := 24 * time.Hour
	maxAgeFloor := now.Add(-eventtime.MaxAge).Add(-chunk).Add(-tailFloorSlack)
	for _, c := range []struct {
		name string
		days int
		want time.Time
	}{
		{"retention off", 0, maxAgeFloor},
		{"five years of retention", 5 * 365, maxAgeFloor},
		{"thirty days of retention", 30, now.Add(-30 * 24 * time.Hour)},
	} {
		s := &TailSweeper{cfg: Config{ChunkInterval: chunk, RetentionDays: c.days}}
		if got := s.floor(now); !got.Equal(c.want) {
			t.Errorf("%s: floor = %s, want %s", c.name, got, c.want)
		}
	}
	if !maxAgeFloor.Before(now.Add(-eventtime.MaxAge)) {
		t.Fatal("the slack must put the floor below the event-time limit")
	}
}
