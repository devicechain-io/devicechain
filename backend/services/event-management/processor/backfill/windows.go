// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// chunkWindow is one chunk of the measurement hypertable: the unit a backfill works in,
// because it is the unit compression, retention and the rollup refresh all act on.
type chunkWindow struct {
	Start, End time.Time
	// Compressed is whether the chunk has a compressed counterpart. It is also true of a chunk
	// that has since taken uncompressed rows back, which is the conservative reading: inserting
	// into it would decompress.
	Compressed bool
}

// chunkWindows reads the real chunk ranges of measurement_events overlapping [from, to), newest
// first. They come from the catalog and are not computed from the chunk interval: Timescale
// aligns chunks in its own origin, and the interval only governs chunks created after it
// changed.
func chunkWindows(db *gorm.DB, from, to time.Time) ([]chunkWindow, error) {
	type row struct {
		RangeStart   time.Time
		RangeEnd     time.Time
		IsCompressed bool
	}
	var rows []row
	err := db.Raw(`SELECT range_start, range_end, is_compressed FROM timescaledb_information.chunks `+
		`WHERE hypertable_schema = ? AND hypertable_name = ? AND range_end > ? AND range_start < ? `+
		`ORDER BY range_start DESC`, hypertableSchema, "measurement_events", from, to).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("reading the measurement chunks: %w", err)
	}
	out := make([]chunkWindow, len(rows))
	for i, r := range rows {
		out[i] = chunkWindow{Start: r.RangeStart.UTC(), End: r.RangeEnd.UTC(), Compressed: r.IsCompressed}
	}
	return out, nil
}

// hypertableSchema is this service's schema, where its hypertables live.
const hypertableSchema = "event-management"
