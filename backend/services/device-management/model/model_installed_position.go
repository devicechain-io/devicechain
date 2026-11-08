// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/geo"
	"github.com/devicechain-io/dc-microservice/integrity"
)

// Where a fixed asset was installed (a streetlight, a parking spot, a cabinet). It is
// installation data, not a report: nothing here is ever written by ingest, only by an operator
// (setInstalledPosition), an import row, or a survey tool.
//
// The coordinate contract is the platform's: WGS84 decimal degrees, elevation and accuracy in
// metres, the same deterministic range rejection the reported position is held to. The column
// widths are latest_locations', so a surveyed point and a reported fix of the same place round-trip
// at the same precision.

// Where an installed position came from.
const (
	InstalledSourceSurvey   = "SURVEY"
	InstalledSourceImport   = "IMPORT"
	InstalledSourceManual   = "MANUAL"
	InstalledSourceInferred = "INFERRED"
)

// ErrInvalidInstalledPosition refuses a position that is not a finite in-range WGS84 coordinate
// (or whose source is not one of the four), before anything is written.
var ErrInvalidInstalledPosition = integrity.NewRefusal(integrity.ClassInvalid,
	"the installed position is not a valid WGS84 coordinate")

// InstalledPosition is the stored installed position of a device or an asset, embedded in both.
//
// 🔴 EVERY FIELD IS `<-:false`, WHICH IS HOW THE ROW'S OTHER WRITERS ARE KEPT OFF IT. Device and
// Asset updates load the row and Save it whole; written whole, an edit racing a setInstalledPosition
// would put the position it loaded back. A field gorm may not write is simply not in that Save's
// column list, so the only writer is the one statement in setInstalledPosition, which names the
// columns itself. (gateway_id is protected from the same race by an explicit Omit list; this needs no
// list that a new Save site could forget.)
//
// Nullable, no default: NULL is "no installed position". The pair latitude/longitude is written and
// cleared together; the other fields may be NULL beside a position.
//
// 🔑 POINTERS WITH omitempty, NOT sql.Null*. The device row is cached JSON-encoded and charged by
// its encoded length against the per-device cache's byte bound (TestDeviceByTokenEntryCost...). An
// sql.Null* encodes as {"Float64":0,"Valid":false} even when NULL, which made every device pay for
// six absent columns; a nil pointer with omitempty encodes as nothing, so a device with no
// installed position costs exactly what it did before the columns existed.
type InstalledPosition struct {
	InstalledLat       *float64   `gorm:"column:installed_lat;type:decimal(10,8);<-:false" json:",omitempty"`
	InstalledLon       *float64   `gorm:"column:installed_lon;type:decimal(11,8);<-:false" json:",omitempty"`
	InstalledElev      *float64   `gorm:"column:installed_elev;type:decimal(12,4);<-:false" json:",omitempty"`
	InstalledAccuracy  *float64   `gorm:"column:installed_accuracy;type:decimal(12,4);<-:false" json:",omitempty"`
	InstalledSource    *string    `gorm:"column:installed_source;<-:false" json:",omitempty"`
	InstalledUpdatedAt *time.Time `gorm:"column:installed_updated_at;<-:false" json:",omitempty"`
}

// InstalledPositionInput is what an operator, an import row or a tool supplies.
type InstalledPositionInput struct {
	Latitude  float64
	Longitude float64
	Elevation *float64
	Accuracy  *float64
	// Source defaults to MANUAL when omitted.
	Source *string
}

// Position returns the stored installed position as a geo.Position, and false when there is none.
// A row holding only one of the pair is treated as having none: half a coordinate is not a place.
func (p InstalledPosition) Position() (geo.Position, bool) {
	if p.InstalledLat == nil || p.InstalledLon == nil {
		return geo.Position{}, false
	}
	out := geo.Position{Lat: *p.InstalledLat, Lon: *p.InstalledLon}
	if p.InstalledElev != nil {
		out.Elevation, out.HasElevation = *p.InstalledElev, true
	}
	return out, true
}

// validate refuses an input that is not a finite in-range coordinate, an elevation or accuracy that
// is not finite (or an accuracy below zero), and an unknown source. It returns the normalized
// source. The check runs before the database is touched.
func (in InstalledPositionInput) validate() (string, error) {
	source := InstalledSourceManual
	if in.Source != nil {
		source = strings.ToUpper(strings.TrimSpace(*in.Source))
	}
	// The range is checked on the value that will be STORED, rounded to the column's scale: an
	// elevation of 99999999.99995 is in range as typed but rounds to 1e8, which numeric(12,4)
	// cannot hold. (rounded leaves NaN and the infinities as they are, so they are still refused.)
	in = in.rounded()
	pos := geo.Position{Lat: in.Latitude, Lon: in.Longitude}
	if err := pos.Validate(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidInstalledPosition, err)
	}
	if in.Elevation != nil && (math.IsNaN(*in.Elevation) || math.IsInf(*in.Elevation, 0) || math.Abs(*in.Elevation) >= 1e8) {
		return "", fmt.Errorf("%w: elevation must be a finite number of metres", ErrInvalidInstalledPosition)
	}
	if in.Accuracy != nil && (math.IsNaN(*in.Accuracy) || math.IsInf(*in.Accuracy, 0) || *in.Accuracy < 0 || *in.Accuracy >= 1e8) {
		return "", fmt.Errorf("%w: accuracy must be a finite, non-negative number of metres", ErrInvalidInstalledPosition)
	}
	switch source {
	case InstalledSourceSurvey, InstalledSourceImport, InstalledSourceManual, InstalledSourceInferred:
		return source, nil
	}
	return "", fmt.Errorf("%w: source %q is not SURVEY, IMPORT, MANUAL or INFERRED", ErrInvalidInstalledPosition, derefString(in.Source))
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// The scales of the installed columns: decimal(10,8)/decimal(11,8) for the coordinates and
// decimal(12,4) for elevation and accuracy.
const (
	coordinateScale = 8
	metresScale     = 4
)

// roundToScale rounds x to scale decimal places the way Postgres rounds a value into a numeric
// column: from the float's shortest decimal spelling, half away from zero, in exact decimal
// arithmetic. Multiplying the float by 10^scale and calling math.Round is NOT the same thing:
// 40.000000095 is stored as 40.00000010, but 40.000000095*1e8 is 4000000009.4999995 in binary
// and rounds down. Every value is rounded HERE before it is written, so what is stored, what a
// write answers and what an import re-run compares are the one value.
func roundToScale(x float64, scale int) float64 {
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(x, 'f', -1, 64))
	if !ok {
		return x // not reached for a finite x, and validate refuses the others
	}
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	r.Mul(r, new(big.Rat).SetInt(pow))
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	// Half away from zero: twice the remainder reaching the denominator moves q one step out.
	if new(big.Int).Mul(new(big.Int).Abs(m), big.NewInt(2)).Cmp(r.Denom()) >= 0 {
		if r.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	out, _ := new(big.Rat).SetFrac(q, pow).Float64()
	return out
}

// rounded returns the input with every value rounded to its column's scale.
func (in InstalledPositionInput) rounded() InstalledPositionInput {
	out := in
	out.Latitude = roundToScale(in.Latitude, coordinateScale)
	out.Longitude = roundToScale(in.Longitude, coordinateScale)
	if in.Elevation != nil {
		v := roundToScale(*in.Elevation, metresScale)
		out.Elevation = &v
	}
	if in.Accuracy != nil {
		v := roundToScale(*in.Accuracy, metresScale)
		out.Accuracy = &v
	}
	return out
}

// written is the stored position a write of in produces, built from what was written rather than
// read back: a reread after the commit could return ANOTHER writer's position to a caller that
// holds device:write but no authority to read positions.
func (in InstalledPositionInput) written(source string, now time.Time) *InstalledPosition {
	in = in.rounded()
	lat, lon := in.Latitude, in.Longitude
	return &InstalledPosition{
		InstalledLat:       &lat,
		InstalledLon:       &lon,
		InstalledElev:      in.Elevation, // rounded returned fresh copies
		InstalledAccuracy:  in.Accuracy,
		InstalledSource:    &source,
		InstalledUpdatedAt: &now,
	}
}

// installedColumns is the column set one write names, rounded to the columns' scales. A clear
// writes every one as NULL.
func (in InstalledPositionInput) installedColumns(source string, now time.Time) map[string]any {
	in = in.rounded()
	cols := map[string]any{
		"installed_lat":        in.Latitude,
		"installed_lon":        in.Longitude,
		"installed_elev":       nil,
		"installed_accuracy":   nil,
		"installed_source":     source,
		"installed_updated_at": now,
	}
	if in.Elevation != nil {
		cols["installed_elev"] = *in.Elevation
	}
	if in.Accuracy != nil {
		cols["installed_accuracy"] = *in.Accuracy
	}
	return cols
}

func clearedInstalledColumns() map[string]any {
	return map[string]any{
		"installed_lat": nil, "installed_lon": nil, "installed_elev": nil,
		"installed_accuracy": nil, "installed_source": nil, "installed_updated_at": nil,
	}
}
