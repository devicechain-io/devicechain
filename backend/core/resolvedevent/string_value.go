// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"strings"
	"unicode/utf8"
)

// MaxStringValueBytes bounds a STRING reading's value. It is ONE bound for every side of the seam:
// device-management's resolver drops a value over it, event-management refuses to store one, and
// device-state does not keep one, so the three never disagree about whether a reading exists.
const MaxStringValueBytes = 4096

// StorableStringValue reports whether v can be stored as a STRING reading's value: within
// MaxStringValueBytes, valid UTF-8, and free of NUL bytes. PostgreSQL refuses a NUL byte or an
// invalid UTF-8 sequence (22021) for the whole statement it appears in, which would fail every other
// reading merged with it.
func StorableStringValue(v string) bool {
	return len(v) <= MaxStringValueBytes && utf8.ValidString(v) && strings.IndexByte(v, 0) < 0
}
