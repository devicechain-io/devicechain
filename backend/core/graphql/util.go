// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	_ "embed"
	"strconv"
	"time"

	"gorm.io/datatypes"
)

// AsUintIds converts GraphQL ID arguments to the uint primary keys the API layer
// takes. It lives here because every service that exposes an `...ById(ids: [ID!]!)`
// query needs exactly this conversion, and the three that had it had each grown
// their own copy with the same two defects:
//
//   - Base 0 instead of 10. ParseUint with base 0 infers the base from the literal's
//     prefix, so a zero-padded "017" is read as OCTAL and silently resolves to row
//     15, and "0x2"/"0b101"/"1_0" are all accepted as ids. A client that formats ids
//     to a fixed width therefore reads the wrong record and gets no error.
//   - A 64-bit parse assigned into uint. uint is 32 bits on some builds, where
//     "4294967297" truncates to 1 — again a wrong record, again successfully.
//
// Parsing at strconv.IntSize fixes the second structurally rather than by a range
// check after the fact: IntSize is the width of uint on the build in question, so
// the parse rejects precisely the values the conversion could not represent, and
// nothing narrower. Hard-coding 32 would instead refuse legitimate ids above ~4
// billion on the 64-bit builds we actually ship.
func AsUintIds(val []string) ([]uint, error) {
	ids := make([]uint, 0, len(val))
	for _, sid := range val {
		id, err := strconv.ParseUint(sid, 10, strconv.IntSize)
		if err != nil {
			return nil, err
		}
		ids = append(ids, uint(id))
	}
	return ids, nil
}

// Converts a sql nullstring to a string pointer.
func NullStr(value sql.NullString) *string {
	if value.Valid {
		return &value.String
	}
	return nil
}

// Converts a sql nullbool to a bool pointer.
func NullBool(value sql.NullBool) *bool {
	if value.Valid {
		return &value.Bool
	}
	return nil
}

// Format time as a string.
//
// 🔴 RFC3339Nano, not RFC3339, and that is a correctness property rather than a
// cosmetic one. Every timestamp in the system is stored at sub-second resolution —
// occurred_time arrives as RFC3339 (which time.Parse accepts with a fractional part),
// crosses the pipeline as RFC3339Nano in both proto marshallers, and lands in a
// timestamptz column with microsecond precision. Formatting the READ with RFC3339
// truncated all of that to whole seconds, so the API published a coarser clock than
// the one it recorded: two readings 200ms apart came back with identical timestamps,
// and a client that read a value and wrote it back silently moved it.
//
// Nano drops trailing zeros, so a whole-second time formats identically to before and
// the change only ever ADDS digits that were already in the database.
//
// This is also load-bearing for optimistic concurrency: the UpdatedAt string a client
// echoes back as a CAS precondition is produced here, and the model-side comparison
// must format with the SAME layout or every precondition fails. See UpdateConnector,
// UpdateDashboard and UpdateAIProvider — they were matched to this deliberately, and
// a change here without a change there breaks all three.
func FormatTime(input time.Time) *string {
	if input.IsZero() {
		return nil
	}
	val := input.Format(time.RFC3339Nano)
	return &val
}

// Converts a sql nullstring to a string pointer.
func MetadataStr(value *datatypes.JSON) *string {
	if value == nil {
		return nil
	}
	str := value.String()
	return &str
}
