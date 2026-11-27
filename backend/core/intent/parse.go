// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

// Parse reads a v1 spec document strictly and returns it normalized and validated.
//
// It refuses, rather than repairs: a document over MaxSpecBytes; anything but one JSON object;
// invalid UTF-8 or an unpaired surrogate escape (encoding/json would silently replace either
// with U+FFFD); a duplicate key at any level (encoding/json would keep the last); a key this
// schema does not define, including one that differs from a defined key only in case
// (encoding/json would match it); a null; a number that is not an integer; a missing required
// field. The only default applied is reconcile.manualDwellSeconds.
func Parse(doc []byte) (Spec, error) {
	if len(doc) > MaxSpecBytes {
		return Spec{}, invalid("the document is %d bytes; at most %d are accepted", len(doc), MaxSpecBytes)
	}
	if !utf8.Valid(doc) {
		return Spec{}, invalid("the document is not valid UTF-8")
	}
	if err := checkSurrogateEscapes(doc); err != nil {
		return Spec{}, err
	}
	tree, err := decodeStrict(doc)
	if err != nil {
		return Spec{}, err
	}
	root, err := object(tree, "spec", "target", "point", "value", "class", "ceiling", "reconcile")
	if err != nil {
		return Spec{}, err
	}
	var s Spec
	target, err := child(root, "target", "target", "groupToken", "groupVersion")
	if err != nil {
		return Spec{}, err
	}
	if s.Target.GroupToken, err = str(target, "target.groupToken", "groupToken"); err != nil {
		return Spec{}, err
	}
	if s.Target.GroupVersion, err = integer(target, "target.groupVersion", "groupVersion"); err != nil {
		return Spec{}, err
	}
	point, err := child(root, "point", "point", "configKey")
	if err != nil {
		return Spec{}, err
	}
	if s.Point.ConfigKey, err = str(point, "point.configKey", "configKey"); err != nil {
		return Spec{}, err
	}
	value, err := child(root, "value", "value", "valueType", "value", "unset")
	if err != nil {
		return Spec{}, err
	}
	if _, ok := value["unset"]; ok {
		b, isBool := value["unset"].(bool)
		if !isBool {
			return Spec{}, invalid("value.unset must be a boolean")
		}
		s.Value.Unset = b
	}
	if _, ok := value["valueType"]; ok {
		if s.Value.ValueType, err = str(value, "value.valueType", "valueType"); err != nil {
			return Spec{}, err
		}
	}
	if _, ok := value["value"]; ok {
		text, err := str(value, "value.value", "value")
		if err != nil {
			return Spec{}, err
		}
		s.Value.Value = &text
	}
	class, err := str(root, "class", "class")
	if err != nil {
		return Spec{}, err
	}
	s.Class = Class(class)
	ceiling, err := child(root, "ceiling", "ceiling", "maxDevices")
	if err != nil {
		return Spec{}, err
	}
	if s.Ceiling.MaxDevices, err = integer(ceiling, "ceiling.maxDevices", "maxDevices"); err != nil {
		return Spec{}, err
	}
	rec, err := child(root, "reconcile", "reconcile",
		"minIntervalSeconds", "maxLinesPerReconcile", "maxActuationsPerDevicePerDay", "manualDwellSeconds")
	if err != nil {
		return Spec{}, err
	}
	r := &s.Reconcile
	if r.MinIntervalSeconds, err = integer(rec, "reconcile.minIntervalSeconds", "minIntervalSeconds"); err != nil {
		return Spec{}, err
	}
	if r.MaxLinesPerReconcile, err = integer(rec, "reconcile.maxLinesPerReconcile", "maxLinesPerReconcile"); err != nil {
		return Spec{}, err
	}
	if r.MaxActuationsPerDevicePerDay, err = integer(rec, "reconcile.maxActuationsPerDevicePerDay", "maxActuationsPerDevicePerDay"); err != nil {
		return Spec{}, err
	}
	r.ManualDwellSeconds = DefaultManualDwellSeconds
	if _, ok := rec["manualDwellSeconds"]; ok {
		if r.ManualDwellSeconds, err = integer(rec, "reconcile.manualDwellSeconds", "manualDwellSeconds"); err != nil {
			return Spec{}, err
		}
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// object asserts v is an object whose keys are all in allowed, compared byte for byte.
func object(v any, path string, allowed ...string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, invalid("%s must be an object", path)
	}
	for key := range m {
		known := false
		for _, a := range allowed {
			if key == a {
				known = true
				break
			}
		}
		if !known {
			return nil, invalid("%s has unknown field %q", path, key)
		}
	}
	return m, nil
}

func child(parent map[string]any, key, path string, allowed ...string) (map[string]any, error) {
	v, ok := parent[key]
	if !ok {
		return nil, invalid("%s is required", path)
	}
	return object(v, path, allowed...)
}

func str(m map[string]any, path, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", invalid("%s is required", path)
	}
	s, ok := v.(string)
	if !ok {
		return "", invalid("%s must be a string", path)
	}
	return s, nil
}

func integer(m map[string]any, path, key string) (int64, error) {
	v, ok := m[key]
	if !ok {
		return 0, invalid("%s is required", path)
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, invalid("%s must be a number", path)
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil {
		return 0, invalid("%s must be an integer, got %s", path, n)
	}
	return i, nil
}

// maxSpecDepth bounds nesting. A v1 spec is two levels deep; the bound only stops a hostile
// document from recursing.
const maxSpecDepth = 8

// decodeStrict decodes exactly one JSON value into maps, slices, strings, json.Number and bools,
// refusing duplicate object keys, nulls and trailing data.
func decodeStrict(doc []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("the document holds more than one JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxSpecDepth {
		return nil, invalid("the document nests deeper than %d levels", maxSpecDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, invalid("the document is not JSON: %v", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := map[string]any{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, invalid("the document is not JSON: %v", err)
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, invalid("the document is not JSON")
				}
				if _, dup := m[key]; dup {
					return nil, invalid("field %q appears more than once", key)
				}
				val, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				m[key] = val
			}
			if _, err := dec.Token(); err != nil {
				return nil, invalid("the document is not JSON: %v", err)
			}
			return m, nil
		case '[':
			var a []any
			for dec.More() {
				val, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, invalid("the document is not JSON: %v", err)
			}
			return a, nil
		}
		return nil, invalid("the document is not JSON")
	case nil:
		return nil, invalid("null is not a value; omit the field instead")
	default:
		return t, nil // string, json.Number, bool
	}
}

// checkSurrogateEscapes refuses a \uD800-\uDFFF escape that is not a high surrogate immediately
// followed by a low one. encoding/json decodes an unpaired one to U+FFFD without an error, which
// would make the spec say something other than what was written.
func checkSurrogateEscapes(doc []byte) error {
	inString := false
	for i := 0; i < len(doc); i++ {
		c := doc[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			if i+1 >= len(doc) {
				return nil // malformed; the decoder reports it
			}
			if doc[i+1] != 'u' {
				i++ // a two-byte escape such as \" or \\
				continue
			}
			r, ok := hex4(doc, i+2)
			if !ok {
				return nil // malformed; the decoder reports it
			}
			switch {
			case r >= 0xDC00 && r <= 0xDFFF:
				return invalid("the document holds an unpaired surrogate escape")
			case r >= 0xD800 && r <= 0xDBFF:
				if i+7 >= len(doc) || doc[i+6] != '\\' || doc[i+7] != 'u' {
					return invalid("the document holds an unpaired surrogate escape")
				}
				low, ok := hex4(doc, i+8)
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return invalid("the document holds an unpaired surrogate escape")
				}
				i += 11
			default:
				i += 5
			}
		}
	}
	return nil
}

// hex4 decodes the four hex digits of a \uXXXX escape starting at doc[at]. It reads exactly four
// bytes and accepts only 0-9, a-f and A-F (no sign, no prefix, no space), so the result is at most
// 0xFFFF by construction: no wider integer is ever narrowed to a rune.
func hex4(doc []byte, at int) (rune, bool) {
	if at < 0 || at > len(doc)-4 {
		return 0, false
	}
	var r rune
	for _, c := range doc[at : at+4] {
		var d byte
		switch {
		case '0' <= c && c <= '9':
			d = c - '0'
		case 'a' <= c && c <= 'f':
			d = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(d)
	}
	return r, true
}
