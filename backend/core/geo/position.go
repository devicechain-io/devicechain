// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/golang/geo/s2"
)

// EarthRadiusM is the mean Earth radius the spherical distance uses. Distances are on a sphere, not
// the WGS84 ellipsoid: the error against the ellipsoid is under 0.5 percent, which is far inside
// the tolerance of anything a radius search is asked.
const EarthRadiusM = 6371008.8

// Position is a WGS84 position in decimal degrees. Elevation is carried but no query reads it.
type Position struct {
	Lat          float64
	Lon          float64
	Elevation    float64
	HasElevation bool
}

// ErrInvalidPosition is returned for a position that is not a finite WGS84 coordinate.
var ErrInvalidPosition = errors.New("the position is not a finite WGS84 coordinate")

// Validate refuses what is not a finite in-range coordinate. Out-of-range degrees are refused rather
// than wrapped, because the spherical conversion wraps them to the far side of the planet.
func (p Position) Validate() error {
	if math.IsNaN(p.Lat) || math.IsInf(p.Lat, 0) || math.IsNaN(p.Lon) || math.IsInf(p.Lon, 0) {
		return fmt.Errorf("%w: not finite", ErrInvalidPosition)
	}
	if p.Lat < -90 || p.Lat > 90 || p.Lon < -180 || p.Lon > 180 {
		return fmt.Errorf("%w: (%v, %v) is outside lat [-90, 90] lon [-180, 180]", ErrInvalidPosition, p.Lat, p.Lon)
	}
	return nil
}

func (p Position) point() s2.Point { return PointFromDegrees(p.Lat, p.Lon) }

// DistanceM is the spherical great-circle distance in metres between two positions.
//
// Boundary semantics: a near query includes a position at exactly the radius (distance <= radius).
// The comparison is on the float64 result of this function, so "exactly" means exactly as this
// function computes it; there is no tolerance band here, unlike polygon containment.
func DistanceM(a, b Position) (float64, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	if err := b.Validate(); err != nil {
		return 0, err
	}
	return float64(a.point().Distance(b.point())) * EarthRadiusM, nil
}

// Contains answers whether the position is inside ANY polygon of the set, boundary included. It is
// the one exact containment predicate: the detection engine's geofence delegates to the same
// Polygon.ContainsPoint, so the indexed answer and the evaluator's cannot differ.
func Contains(set []*Polygon, p Position) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	pt := p.point()
	for _, poly := range set {
		if poly.ContainsPoint(pt) {
			return true, nil
		}
	}
	return false, nil
}

// Mode selects which positions of an entity may answer a question.
type Mode int

const (
	// ModeUnset is the zero value and is never valid: a caller that did not choose a mode must not
	// silently get one.
	ModeUnset Mode = iota
	// ModeInstalled reads the installed position only.
	ModeInstalled
	// ModeReported reads the latest reported fix only, whatever its age.
	ModeReported
	// ModeEffective reads a FRESH reported fix, else the installed position.
	ModeEffective
)

func (m Mode) String() string {
	switch m {
	case ModeInstalled:
		return "INSTALLED"
	case ModeReported:
		return "REPORTED"
	case ModeEffective:
		return "EFFECTIVE"
	}
	return fmt.Sprintf("Mode(%d)", int(m))
}

// ParseMode maps the wire spelling to a Mode and refuses anything else.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "INSTALLED":
		return ModeInstalled, nil
	case "REPORTED":
		return ModeReported, nil
	case "EFFECTIVE":
		return ModeEffective, nil
	}
	return ModeUnset, fmt.Errorf("unknown position mode %q (want INSTALLED, REPORTED or EFFECTIVE)", s)
}

// Source says where an answered position came from.
type Source int

const (
	SourceNone Source = iota
	SourceInstalled
	SourceReported
)

func (s Source) String() string {
	switch s {
	case SourceInstalled:
		return "INSTALLED"
	case SourceReported:
		return "REPORTED"
	}
	return "NONE"
}

// Fix is a reported position with the time it was observed.
type Fix struct {
	Position
	At time.Time
}

// fresh reports whether the fix is usable under the bound at the captured query time. A zero
// bound is NOT "unbounded": a profile that declares no freshness declares no usable reported
// position for the effective rule. A fix dated after the query time is not fresh either: asked
// "as of at", it had not been reported yet.
func (f *Fix) fresh(freshness time.Duration, at time.Time) bool {
	if f == nil || freshness <= 0 || f.At.IsZero() || f.At.After(at) {
		return false
	}
	return at.Sub(f.At) <= freshness
}

// Effective resolves an entity's position for the requested mode at a captured query time.
//
// 🔴 INSTALLED NEVER READS reported, AND THAT IS STRUCTURAL: the INSTALLED branch is a call to
// installedOnly, a function that does not receive the reported fix, so no edit to its body can
// make it fall back to a live position. A caller that holds only the installed-position authority
// must never be handed a live fix by a rule that happened to prefer one.
//
// An unusable position (not a finite in-range coordinate) is treated as absent, so EFFECTIVE falls
// through from a corrupt fix to the installed position rather than answering from it. The bool is
// false when nothing answers. An invalid mode panics: it is a programming error, and a plausible
// "none" would be indistinguishable from an entity with no position.
func Effective(reported *Fix, freshness time.Duration, installed *Position, at time.Time, mode Mode) (Position, Source, bool) {
	switch mode {
	case ModeInstalled:
		return installedOnly(installed)
	case ModeReported:
		if reported != nil && reported.Validate() == nil {
			return reported.Position, SourceReported, true
		}
		return Position{}, SourceNone, false
	case ModeEffective:
		if reported.fresh(freshness, at) && reported.Validate() == nil {
			return reported.Position, SourceReported, true
		}
		return installedOnly(installed)
	}
	panic(fmt.Sprintf("geo.Effective: invalid mode %v", mode))
}

func installedOnly(installed *Position) (Position, Source, bool) {
	if installed != nil && installed.Validate() == nil {
		return *installed, SourceInstalled, true
	}
	return Position{}, SourceNone, false
}
