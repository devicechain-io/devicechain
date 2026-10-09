// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import "bytes"

// objectKeysLinearMax is the key count up to which canonicalKeys compares an object's
// keys pairwise. Above it the check hashes, so an object with thousands
// of keys costs linear time rather than quadratic.
const objectKeysLinearMax = 16

// canonicalKeys reports whether doc can be decoded directly with the same result as
// decoding it into a map and re-marshalling first. It is the first thing Decode runs, so
// it does not assume doc is valid JSON: a document it cannot follow answers false, and
// the reference decode then produces the error.
//
// It answers false, and the caller takes the reference decode, when:
//   - an object, at any depth, has two keys equal ignoring ASCII case. That covers a
//     repeated key, which a direct decode merges where the map round trip keeps the last,
//     and two spellings of one struct field, which the round trip applies in sorted order
//     and a direct decode in wire order;
//   - an object key contains a backslash or a byte at or above 0x80. Such a key is only
//     equal to another after unescaping or invalid-UTF-8 repair, which this scan does
//     not do, so it declines rather than guesses;
//   - a number, anywhere, has a non-negative exponent or is 309 or more bytes long. The map
//     round trip reads every number as a float64, so one that overflows it (1e400)
//     rejects the whole document even inside a field the typed structs ignore, and a
//     direct decode would skip it. A number with neither feature is below 1e309 in
//     magnitude, which float64 holds; a negative exponent underflows to zero without error.
//
// A false answer costs speed only: it sends the event down the original path.
func canonicalKeys(doc []byte) bool {
	var (
		keyBuf   [64][]byte
		startBuf [16]int
		keys     = keyBuf[:0]
		starts   = startBuf[:0] // per open container: -1 for an array, else where its keys begin in keys
		prev     byte           // last structural byte seen: one of { [ , : or 0 for a scalar
	)
	for i := 0; i < len(doc); i++ {
		switch c := doc[i]; c {
		case '{':
			starts = append(starts, len(keys))
			prev = '{'
		case '[':
			starts = append(starts, -1)
			prev = '['
		case '}':
			if len(starts) == 0 || starts[len(starts)-1] < 0 {
				return false
			}
			start := starts[len(starts)-1]
			starts = starts[:len(starts)-1]
			if !distinctKeys(keys[start:]) {
				return false
			}
			keys = keys[:start]
			prev = 0
		case ']':
			if len(starts) == 0 || starts[len(starts)-1] >= 0 {
				return false
			}
			starts = starts[:len(starts)-1]
			prev = 0
		case ',', ':':
			prev = c
		case '"':
			j := i + 1
			isKey := len(starts) > 0 && starts[len(starts)-1] >= 0 && (prev == '{' || prev == ',')
			for ; j < len(doc) && doc[j] != '"'; j++ {
				if doc[j] == 0x5c { // a backslash: an escaped key is declined, any other string skips the escaped byte, which may be a quote
					if isKey {
						return false
					}
					j++
				} else if isKey && doc[j] >= 0x80 {
					return false
				}
			}
			if j >= len(doc) {
				return false
			}
			if isKey {
				keys = append(keys, doc[i+1:j])
			}
			i = j
			prev = 0
		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			j := i + 1
			for ; j < len(doc); j++ {
				d := doc[j]
				if (d == 'e' || d == 'E') && (j+1 >= len(doc) || doc[j+1] != '-') {
					return false // a positive exponent can overflow float64; a negative one only underflows to zero
				}
				if d == ',' || d == ']' || d == '}' || d <= ' ' { // a comma, a closer or whitespace ends the number
					break
				}
			}
			if j-i >= 309 {
				return false
			}
			i = j - 1
			prev = 0
		}
	}
	return len(starts) == 0
}

// distinctKeys reports whether no two of keys (all ASCII) are equal ignoring case.
func distinctKeys(keys [][]byte) bool {
	if len(keys) <= objectKeysLinearMax {
		for a := 1; a < len(keys); a++ {
			for b := 0; b < a; b++ {
				if bytes.EqualFold(keys[a], keys[b]) {
					return false
				}
			}
		}
		return true
	}
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		folded := string(bytes.ToLower(k))
		if _, dup := seen[folded]; dup {
			return false
		}
		seen[folded] = struct{}{}
	}
	return true
}
