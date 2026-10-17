// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"math"
	"strconv"
)

// StoredDecimals is how many decimal places measurement storage keeps: a stored value is a
// decimal(20,8), rounded to eight places, and read back as a float64.
const StoredDecimals = 8

// maxExactInt is the largest magnitude a float64 holds every integer up to.
const maxExactInt = 1 << 53

// FormatStored renders a measurement value READ BACK FROM STORAGE as the raw input string
// Sample.Set parses for an input of type t, so a formula evaluated over stored rows (the
// draft-formula history preview, a backfill) goes through the same parse as one evaluated at
// resolve:
//
//   - a DOUBLE is the shortest string that parses back to exactly v (FormatDouble);
//   - an INT is its base-10 integer when v is a whole number a float64 holds exactly, and
//     otherwise v's decimal form, which does not parse as an INT, so a formula reading it
//     skips as invalid_input rather than reading a truncated value;
//   - a BOOLEAN is "1" or "0" for 1 and 0 (as booleans are stored), and otherwise v's
//     decimal form, which likewise does not parse.
//
// 🔴 A STORED VALUE IS NOT THE VALUE THE DEVICE SENT. Storage rounds to StoredDecimals places,
// so a formula over stored inputs can differ from the value computed from the device's own
// string at resolve — V·I with a current of 0.000000015 A is the plain case. A caller must say
// so wherever it shows such a result, and never present it as the value that was or would be
// stored.
func FormatStored(t Type, v float64) string {
	switch t {
	case Integer:
		if v == math.Trunc(v) && math.Abs(v) <= maxExactInt {
			return strconv.FormatInt(int64(v), 10)
		}
	case Boolean:
		switch v {
		case 1:
			return "1"
		case 0:
			return "0"
		}
	}
	return FormatDouble(v)
}
