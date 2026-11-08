// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"fmt"
	"math"
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
type InstalledPosition struct {
	InstalledLat       sql.NullFloat64 `gorm:"column:installed_lat;type:decimal(10,8);<-:false"`
	InstalledLon       sql.NullFloat64 `gorm:"column:installed_lon;type:decimal(11,8);<-:false"`
	InstalledElev      sql.NullFloat64 `gorm:"column:installed_elev;type:decimal(12,4);<-:false"`
	InstalledAccuracy  sql.NullFloat64 `gorm:"column:installed_accuracy;type:decimal(12,4);<-:false"`
	InstalledSource    sql.NullString  `gorm:"column:installed_source;<-:false"`
	InstalledUpdatedAt sql.NullTime    `gorm:"column:installed_updated_at;<-:false"`
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
	if !p.InstalledLat.Valid || !p.InstalledLon.Valid {
		return geo.Position{}, false
	}
	out := geo.Position{Lat: p.InstalledLat.Float64, Lon: p.InstalledLon.Float64}
	if p.InstalledElev.Valid {
		out.Elevation, out.HasElevation = p.InstalledElev.Float64, true
	}
	return out, true
}

// validate refuses an input that is not a finite in-range coordinate, an elevation or accuracy that
// is not finite (or an accuracy below zero), and an unknown source. It returns the normalized
// source. The check runs before the database is touched.
func (in InstalledPositionInput) validate() (string, error) {
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
	source := InstalledSourceManual
	if in.Source != nil {
		source = strings.ToUpper(strings.TrimSpace(*in.Source))
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

// installedColumns is the column set one write names. A clear writes every one as NULL.
func (in InstalledPositionInput) installedColumns(source string, now time.Time) map[string]any {
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
