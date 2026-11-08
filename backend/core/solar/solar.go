// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package solar answers one question for a place on Earth and a calendar day: when does the
// sun rise and set there?
//
// It is a pure function of latitude, longitude and a date. It reads no clock and no zone
// database, so a scheduler that dims ten thousand lights at each one's own dusk gets the same
// answer on every replica and in every test.
//
// # Accuracy
//
// The calculation is the standard low-precision sunrise equation (mean solar transit, equation
// of centre, hour angle at -0.833 degrees to allow for refraction and the solar disc). It is
// good to about two minutes between roughly 60 degrees north and south, which is finer than a
// street light's photocell and far finer than any sensible offset. It degrades towards the
// poles and is not for navigation or astronomy.
//
// # Days
//
// A day is the SOLAR day at the place: the date names the sunrise and sunset around local
// solar noon on that date, wherever the place is. A sunset at 19:00 local in Honolulu is
// 05:00 UTC on the NEXT UTC date, and it is still "the sunset of the 21st" there. No time zone
// is involved, which is deliberate: the sun does not know the zone, so a wrong zone cannot
// move a solar event.
//
// # Polar days and nights
//
// Above the Arctic Circle in summer the sun does not set, and in winter it does not rise.
// That is an ANSWER, not an error: Compute reports it in Events.Status and leaves the
// instants zero. A caller must look at Status; it must never use a zero instant as a time.
package solar

import (
	"fmt"
	"math"
	"time"
)

// Status says whether the sun rises and sets on the day asked about.
type Status int

const (
	// Normal: the sun rises and sets. Rise and Set are valid.
	Normal Status = iota
	// PolarDay: the sun stays up all day. Rise and Set are zero.
	PolarDay
	// PolarNight: the sun stays down all day. Rise and Set are zero.
	PolarNight
)

func (s Status) String() string {
	switch s {
	case Normal:
		return "NORMAL"
	case PolarDay:
		return "POLAR_DAY"
	case PolarNight:
		return "POLAR_NIGHT"
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// Events are the solar events of one solar day at one place.
type Events struct {
	Status Status
	// Rise and Set are instants (UTC) and meaningful only when Status is Normal.
	Rise time.Time
	Set  time.Time
}

// ErrOutOfRange is returned for a coordinate that is not on the globe. A scheduler that
// quietly clamped a mistyped latitude would dim lights at an invented dusk.
type ErrOutOfRange struct{ Reason string }

func (e ErrOutOfRange) Error() string { return "solar: " + e.Reason }

const (
	j2000       = 2451545.0
	unixJD      = 2440587.5
	secondsDay  = 86400.0
	horizonDeg  = -0.833
	rad         = math.Pi / 180
	obliquityDg = 23.4397
)

// Compute returns the sunrise and sunset of the solar day named by year, month and day at
// latitude lat (north positive) and longitude lon (east positive), in decimal degrees.
func Compute(year int, month time.Month, day int, lat, lon float64) (Events, error) {
	if math.IsNaN(lat) || lat < -90 || lat > 90 {
		return Events{}, ErrOutOfRange{fmt.Sprintf("latitude %v is outside -90..90", lat)}
	}
	if math.IsNaN(lon) || lon < -180 || lon > 180 {
		return Events{}, ErrOutOfRange{fmt.Sprintf("longitude %v is outside -180..180", lon)}
	}
	// Julian day of noon UTC on the date, then whole days since J2000.
	noon := time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
	jd := float64(noon.Unix())/secondsDay + unixJD
	n := math.Round(jd - j2000)

	// Mean solar time at the place: the transit happens when the sun is on the local
	// meridian, lon/360 of a day EARLIER than at Greenwich for a place to the east.
	jStar := n + 0.0009 - lon/360

	m := math.Mod(357.5291+0.98560028*jStar, 360) * rad
	c := 1.9148*math.Sin(m) + 0.02*math.Sin(2*m) + 0.0003*math.Sin(3*m)
	lambda := math.Mod(m/rad+c+180+102.9372, 360) * rad
	transit := j2000 + jStar + 0.0053*math.Sin(m) - 0.0069*math.Sin(2*lambda)

	sinDec := math.Sin(lambda) * math.Sin(obliquityDg*rad)
	cosDec := math.Cos(math.Asin(sinDec))
	phi := lat * rad
	cosH := (math.Sin(horizonDeg*rad) - math.Sin(phi)*sinDec) / (math.Cos(phi) * cosDec)
	if math.IsNaN(cosH) || math.IsInf(cosH, 0) { // exactly at a pole
		if sinDec*math.Sin(phi) > 0 {
			return Events{Status: PolarDay}, nil
		}
		return Events{Status: PolarNight}, nil
	}
	switch {
	case cosH > 1:
		return Events{Status: PolarNight}, nil
	case cosH < -1:
		return Events{Status: PolarDay}, nil
	}
	h := math.Acos(cosH) / rad / 360 // days from transit to the horizon
	return Events{
		Status: Normal,
		Rise:   fromJD(transit - h),
		Set:    fromJD(transit + h),
	}, nil
}

func fromJD(jd float64) time.Time {
	sec := (jd - unixJD) * secondsDay
	return time.Unix(int64(math.Round(sec)), 0).UTC()
}
