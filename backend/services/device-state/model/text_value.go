// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"unicode/utf8"
)

// DataTypeString is the declared data type of a reading whose value is text. A reading of this
// type is kept in text_value and never in the numeric column, even when its text happens to
// parse as a number.
const DataTypeString = "STRING"

// MaxTextValueBytes bounds a STRING reading's value, the same bound event-management applies to
// the history row: a value over it is not stored there either, so the two never disagree about
// whether a reading exists.
const MaxTextValueBytes = 4096

// StorableTextValue reports whether v can be kept as a STRING reading's value. It is the
// admission test, applied before the value reaches a batch statement, because PostgreSQL refuses
// a NUL byte or an invalid UTF-8 sequence (22021) for the whole statement, which would fail every
// other reading merged with it, numeric ones included.
func StorableTextValue(v string) bool {
	return len(v) <= MaxTextValueBytes && utf8.ValidString(v) && strings.IndexByte(v, 0) < 0
}
