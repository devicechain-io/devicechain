// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import "bytes"

// objectKeysLinearMax is the key count up to which canonicalKeys compares an object's
// keys pairwise. Above it the check hashes, so an object with thousands
// of keys costs linear time rather than quadratic.
const objectKeysLinearMax = 16

// canonicalKeys reports whether every object in doc, at any depth, has keys that
// encoding/json treats the same whether the document is decoded directly or decoded
// into a map and re-marshalled first. doc MUST already be valid JSON (the caller has
// unmarshalled it), which lets this scan skip the validation a tokenizer would do.
//
// It answers false, and the caller takes the reference decode, when an object has:
//   - two keys that are equal ignoring ASCII case. That covers a repeated key, which a
//     direct decode merges where the map round trip keeps the last, and two spellings
//     of one struct field, which the round trip applies in sorted order and a direct
//     decode in wire order;
//   - a key containing a backslash or a byte at or above 0x80. Such a key is only
//     equal to another after unescaping or invalid-UTF-8 repair, which this scan does
//     not do, so it declines rather than guesses.
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
			start := starts[len(starts)-1]
			starts = starts[:len(starts)-1]
			if !distinctKeys(keys[start:]) {
				return false
			}
			keys = keys[:start]
			prev = 0
		case ']':
			starts = starts[:len(starts)-1]
			prev = 0
		case ',', ':':
			prev = c
		case '"':
			j := i + 1
			isKey := len(starts) > 0 && starts[len(starts)-1] >= 0 && (prev == '{' || prev == ',')
			if isKey {
				for ; doc[j] != '"'; j++ {
					if doc[j] == 0x5c || doc[j] >= 0x80 { // 0x5c is a backslash
						return false
					}
				}
				keys = append(keys, doc[i+1:j])
			} else {
				for ; doc[j] != '"'; j++ {
					if doc[j] == 0x5c { // a backslash: skip the escaped byte, which may be a quote
						j++
					}
				}
			}
			i = j
			prev = 0
		}
	}
	return true
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
