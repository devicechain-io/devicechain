// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"slices"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

// Reading per-device constants (ADR-082) out of a device's metadata, on the resolver's path.
//
// The document is read ONCE per event and only when the version's formulas read a constant
// (ProfileResolution.DeviceConstants), so the reader is written to cost what it must: a single
// forward scan of the top-level object that compares each key to the few wanted names without
// building a string, steps over every other value without decoding it, and allocates only for
// the constants it finds. encoding/json's token reader did the same job in 27 more allocations
// per event (see BenchmarkResolveMeasurements_CalcConst); the scanner trades that for the
// obligation to agree with it, which TestReadConstantsAgreesWithEncodingJSON holds.
//
// The scanner may assume valid JSON — the column refuses anything else on write — but never
// trusts it: any structure it does not expect ends the read with NO constants, so a damaged
// document makes every constant absent (the formulas that need one skip loudly) and never
// supplies a half of them.

// readConstants returns the constants named in keys that the metadata object holds. Of a key
// repeated in the document the last wins, as JSON decoders have it. A document that is not an
// object, or that cannot be read through to its closing brace, has none.
func readConstants(doc []byte, keys []string) []calcfield.Constant {
	i := skipSpace(doc, 0)
	if i >= len(doc) || doc[i] != '{' {
		return nil
	}
	i = skipSpace(doc, i+1)
	out := make([]calcfield.Constant, 0, len(keys))
	if i < len(doc) && doc[i] == '}' {
		return out
	}
	for {
		kstart := i
		kend, ok := scanString(doc, i)
		if !ok {
			return nil
		}
		i = skipSpace(doc, kend)
		if i >= len(doc) || doc[i] != ':' {
			return nil
		}
		i = skipSpace(doc, i+1)
		vend, ok := scanValue(doc, i)
		if !ok {
			return nil
		}
		if key, found := wantedKey(doc[kstart:kend], keys); found {
			c := constantOf(key, doc[i:vend])
			if at := slices.IndexFunc(out, func(o calcfield.Constant) bool { return o.Key() == key }); at >= 0 {
				out[at] = c
			} else {
				out = append(out, c)
			}
		}
		i = skipSpace(doc, vend)
		if i >= len(doc) {
			return nil
		}
		switch doc[i] {
		case ',':
			// The next member; a closing brace here is not a key, so scanString refuses it.
			i = skipSpace(doc, i+1)
		case '}':
			return out
		default:
			return nil
		}
	}
}

// wantedKey reports whether the quoted JSON string (quotes included) is one of keys, and which.
// An escape-free key is compared in place; one with escapes is decoded first.
func wantedKey(quoted []byte, keys []string) (string, bool) {
	body := quoted[1 : len(quoted)-1]
	if !slices.Contains(body, byte(0x5c)) { // no backslash: the key has no escapes
		for _, k := range keys {
			if string(body) == k {
				return k, true
			}
		}
		return "", false
	}
	var decoded string
	if json.Unmarshal(quoted, &decoded) != nil {
		return "", false
	}
	for _, k := range keys {
		if decoded == k {
			return k, true
		}
	}
	return "", false
}

// constantOf is one metadata value as a constant: a JSON number parsed as a DOUBLE, anything
// else unusable. A string is never coerced: "1.02" is a string, and a calibration factor typed
// as one is a mistake to surface, not to guess at.
func constantOf(key string, raw []byte) calcfield.Constant {
	if len(raw) > 0 && (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')) {
		return calcfield.ConstantFromString(key, string(raw))
	}
	return calcfield.InvalidConstant(key)
}

func skipSpace(doc []byte, i int) int {
	for i < len(doc) {
		switch doc[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString returns the index just past the JSON string that starts at doc[i], or false.
func scanString(doc []byte, i int) (int, bool) {
	if i >= len(doc) || doc[i] != '"' {
		return 0, false
	}
	for i++; i < len(doc); i++ {
		switch doc[i] {
		case byte(0x5c): // a backslash
			i++ // the escaped byte, whatever it is (a \u escape's hex digits are plain bytes)
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

// scanValue returns the index just past the JSON value that starts at doc[i]: a string, an
// object or array (balanced, string-aware) or a bare scalar up to its delimiter.
func scanValue(doc []byte, i int) (int, bool) {
	if i >= len(doc) {
		return 0, false
	}
	switch doc[i] {
	case '"':
		return scanString(doc, i)
	case '{', '[':
		depth := 0
		for ; i < len(doc); i++ {
			switch doc[i] {
			case '"':
				end, ok := scanString(doc, i)
				if !ok {
					return 0, false
				}
				i = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	case ',', '}', ']', ':':
		return 0, false
	}
	start := i
	for ; i < len(doc); i++ {
		switch doc[i] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return i, i > start
		}
	}
	return i, i > start
}
