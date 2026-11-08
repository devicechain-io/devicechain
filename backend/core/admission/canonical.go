// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

const (
	// MaxParameterBytes bounds the parameters document. A proposal is a statement of intent,
	// not a payload store, and the canonicaliser holds the whole document in memory.
	MaxParameterBytes = 64 << 10
	// maxParameterDepth bounds nesting so a hostile document cannot drive the recursion deep.
	maxParameterDepth = 32
)

var (
	errDuplicateKey = errors.New("duplicate object key")
	errTrailingData = errors.New("trailing data after the JSON value")
	errInvalidUTF8  = errors.New("not valid UTF-8")
	errTooDeep      = errors.New("nested too deeply")
	errTooLarge     = errors.New("larger than the parameter limit")
	errSurrogate    = errors.New("unpaired UTF-16 surrogate escape")
)

// checkSurrogates refuses a \u escape naming half of a surrogate pair without the other half.
// encoding/json replaces such an escape with U+FFFD, so without this check "\ud800" and
// "\ufffd" would share one canonical form and one digest. A backslash outside a string is
// invalid JSON and is left to the decoder; inside one, every escape is two bytes or \uXXXX, so
// a linear scan that steps over each escape cannot mistake an escaped backslash for the start
// of one.
func checkSurrogates(raw []byte) error {
	hex4 := func(i int) (rune, bool) {
		if i+6 > len(raw) || raw[i] != '\\' || raw[i+1] != 'u' {
			return 0, false
		}
		var r rune
		for _, c := range raw[i+2 : i+6] {
			switch {
			case c >= '0' && c <= '9':
				r = r<<4 | rune(c-'0')
			case c >= 'a' && c <= 'f':
				r = r<<4 | rune(c-'a'+10)
			case c >= 'A' && c <= 'F':
				r = r<<4 | rune(c-'A'+10)
			default:
				return 0, false
			}
		}
		return r, true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		r, ok := hex4(i)
		if !ok {
			i++ // a two-byte escape (or invalid JSON, which the decoder refuses)
			continue
		}
		switch {
		case r >= 0xD800 && r <= 0xDBFF:
			lo, ok := hex4(i + 6)
			if !ok || lo < 0xDC00 || lo > 0xDFFF {
				return errSurrogate
			}
			i += 11
		case r >= 0xDC00 && r <= 0xDFFF:
			return errSurrogate
		default:
			i += 5
		}
	}
	return nil
}

// canonicalJSON re-encodes one JSON document in a single spelling (see the package doc). An
// absent document (nil or empty) is the JSON null, so "no parameters" has one digest whether
// the field was omitted or written as null.
func canonicalJSON(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("null"), nil
	}
	if len(raw) > MaxParameterBytes {
		return nil, errTooLarge
	}
	if !utf8.Valid(raw) {
		return nil, errInvalidUTF8
	}
	if err := checkSurrogates(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	out, err := canonicalValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errTrailingData
	}
	return out, nil
}

func canonicalValue(dec *json.Decoder, depth int) ([]byte, error) {
	if depth > maxParameterDepth {
		return nil, errTooDeep
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return canonicalObject(dec, depth)
		case '[':
			return canonicalArray(dec, depth)
		default:
			return nil, fmt.Errorf("unexpected %q", rune(t))
		}
	case string:
		return json.Marshal(t)
	case json.Number:
		// The decoder has already checked the literal is a JSON number; its text is kept as
		// written so no precision is lost and no float formatting is involved.
		return []byte(t.String()), nil
	case bool:
		if t {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case nil:
		return []byte("null"), nil
	default:
		return nil, fmt.Errorf("unexpected token %T", tok)
	}
}

func canonicalObject(dec *json.Decoder, depth int) ([]byte, error) {
	members := map[string][]byte{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("object key is %T, not a string", kt)
		}
		if _, dup := members[key]; dup {
			return nil, fmt.Errorf("%w %q", errDuplicateKey, key)
		}
		v, err := canonicalValue(dec, depth+1)
		if err != nil {
			return nil, err
		}
		members[key] = v
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, err
	}
	keys := make([]string, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(members[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func canonicalArray(dec *json.Decoder, depth int) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i := 0; dec.More(); i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		v, err := canonicalValue(dec, depth+1)
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	if _, err := dec.Token(); err != nil { // the closing ']'
		return nil, err
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}
