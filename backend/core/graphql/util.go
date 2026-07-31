// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	_ "embed"
	"time"

	"gorm.io/datatypes"
)

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
