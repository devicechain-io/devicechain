// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package solar

import (
	"errors"
	"testing"
	"time"
)

const tolerance = 3 * time.Minute

func near(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	d := got.Sub(want)
	if d < 0 {
		d = -d
	}
	if d > tolerance {
		t.Errorf("%s = %s, want %s +/- %s (off by %s)", name, got.Format(time.RFC3339), want.Format(time.RFC3339), tolerance, d)
	}
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// Reference values are published almanac times for the place and date.
func TestKnownPlaces(t *testing.T) {
	cases := []struct {
		name      string
		y         int
		m         time.Month
		d         int
		lat, lon  float64
		rise, set string
	}{
		{"London midsummer", 2021, 6, 21, 51.5074, -0.1278, "2021-06-21T03:43:00Z", "2021-06-21T20:21:00Z"},
		{"London midwinter", 2021, 12, 21, 51.5074, -0.1278, "2021-12-21T08:04:00Z", "2021-12-21T15:53:00Z"},
		{"New York equinox (EDT)", 2021, 3, 20, 40.7128, -74.0060, "2021-03-20T11:02:00Z", "2021-03-20T23:10:00Z"},
		{"Sydney midsummer", 2021, 12, 21, -33.8688, 151.2093, "2021-12-20T18:41:00Z", "2021-12-21T09:05:00Z"},
		{"Singapore", 2021, 3, 20, 1.3521, 103.8198, "2021-03-19T23:09:00Z", "2021-03-20T11:16:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, err := Compute(c.y, c.m, c.d, c.lat, c.lon)
			if err != nil || ev.Status != Normal {
				t.Fatalf("Compute = %+v, %v", ev, err)
			}
			near(t, "rise", ev.Rise, utc(c.rise))
			near(t, "set", ev.Set, utc(c.set))
		})
	}
}

// A place far west has its sunset on the NEXT UTC date. The day is the SOLAR day, so the
// answer must still belong to the date asked about.
func TestSolarDayIsNotTheUTCDay(t *testing.T) {
	ev, err := Compute(2021, 6, 21, 21.3069, -157.8583) // Honolulu
	if err != nil {
		t.Fatal(err)
	}
	near(t, "set", ev.Set, utc("2021-06-22T05:15:00Z")) // 19:15 HST on the 21st
	if !ev.Set.After(utc("2021-06-22T00:00:00Z")) {
		t.Fatalf("Honolulu sunset %s should fall on the next UTC date", ev.Set)
	}
}

func TestPolar(t *testing.T) {
	ev, _ := Compute(2021, 6, 21, 78.22, 15.65) // Longyearbyen
	if ev.Status != PolarDay || !ev.Rise.IsZero() || !ev.Set.IsZero() {
		t.Fatalf("midsummer at 78N = %+v, want POLAR_DAY with zero instants", ev)
	}
	ev, _ = Compute(2021, 12, 21, 78.22, 15.65)
	if ev.Status != PolarNight {
		t.Fatalf("midwinter at 78N = %+v, want POLAR_NIGHT", ev)
	}
	ev, _ = Compute(2021, 12, 21, -78.22, 15.65)
	if ev.Status != PolarDay {
		t.Fatalf("southern midsummer = %+v, want POLAR_DAY", ev)
	}
}

func TestOutOfRangeIsRefused(t *testing.T) {
	for _, c := range [][2]float64{{91, 0}, {-91, 0}, {0, 181}, {0, -181}} {
		_, err := Compute(2021, 6, 21, c[0], c[1])
		var oor ErrOutOfRange
		if !errors.As(err, &oor) {
			t.Errorf("Compute(%v,%v) err = %v, want ErrOutOfRange", c[0], c[1], err)
		}
	}
}

// Sunset moves later towards the west within one zone; the per-fixture offset depends on it.
func TestSunsetOrderFollowsLongitude(t *testing.T) {
	east, _ := Compute(2021, 9, 22, 50, 10)
	west, _ := Compute(2021, 9, 22, 50, 5)
	if !west.Set.After(east.Set) {
		t.Fatalf("west %s should set after east %s", west.Set, east.Set)
	}
}

func TestStatusString(t *testing.T) {
	if Normal.String() != "NORMAL" || PolarDay.String() != "POLAR_DAY" || PolarNight.String() != "POLAR_NIGHT" {
		t.Fatal("status names changed")
	}
}
