// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Canonical produces deterministic JSON, independent of encoding/json's map
// iteration or output ordering. Go values first use encoding/json's projection
// (struct tags, omission, MarshalJSON, base64 byte slices and nil semantics).
// Object keys are then explicitly sorted by UTF-8 byte order; arrays retain
// order. Strings use UTF-8 without Unicode normalization or HTML escaping;
// quotes and backslashes are escaped, and every control character uses lowercase
// \u00xx. Invalid UTF-8 follows encoding/json's replacement-character rule.
// Numbers use exact decimal arithmetic on the projected JSON token: no exponent,
// redundant leading/trailing zeros or negative zero, and no decimal point for
// integral values. Floats use their shortest round-trip decimal projection;
// json.Number and RawMessage preserve exact decimal input without float64 loss.
// The writer emits no whitespace. These rules, rather than map serialization
// order or an encoder's indentation defaults, define the digest bytes.
// Non-string map keys (even on nil maps), cycles, unsupported JSON values and
// NaN/Inf are errors. Custom marshalers must themselves be deterministic.
func Canonical(v any) ([]byte, error) {
	if err := checkMapKeys(reflect.ValueOf(v), make(map[canonicalVisit]bool)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var projected any
	if err := decoder.Decode(&projected); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, projected); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Digest returns SHA-256 of Canonical(v), as "sha256:" plus 64 lowercase hex
// characters: exactly 71 bytes, matching the future journal's varchar(71).
func Digest(v any) (string, error) {
	canonical, err := Canonical(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type canonicalVisit struct {
	typ     reflect.Type
	pointer uintptr
	length  int
}

func checkMapKeys(v reflect.Value, seen map[canonicalVisit]bool) error {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Map && v.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("canonical JSON requires string map keys, got %s", v.Type())
	}
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			return checkMapKeys(v.Elem(), seen)
		}
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return nil
		}
		visit := canonicalVisit{typ: v.Type()}
		if v.Kind() == reflect.Map {
			visit.pointer = uintptr(v.UnsafePointer())
		} else {
			visit.pointer = v.Pointer()
		}
		if v.Kind() == reflect.Slice {
			visit.length = v.Len()
		}
		if seen[visit] {
			return nil // json.Marshal reports cycles; don't recurse forever here.
		}
		seen[visit] = true
		switch v.Kind() {
		case reflect.Pointer:
			return checkMapKeys(v.Elem(), seen)
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				if err := checkMapKeys(iter.Value(), seen); err != nil {
					return err
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if err := checkMapKeys(v.Index(i), seen); err != nil {
					return err
				}
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := checkMapKeys(v.Index(i), seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.IsExported() && field.Tag.Get("json") != "-" {
				if err := checkMapKeys(v.Field(i), seen); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func writeCanonical(out *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case string:
		writeString(out, v)
	case json.Number:
		number, err := decimalNumber(string(v))
		if err != nil {
			return err
		}
		out.WriteString(number)
	case []any:
		out.WriteByte('[')
		for i, child := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, child); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeString(out, key)
			out.WriteByte(':')
			if err := writeCanonical(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported projected JSON type %T", v)
	}
	return nil
}

func writeString(out *bytes.Buffer, s string) {
	const digits = "0123456789abcdef"
	out.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case r < 0x20:
			out.WriteString(`\u00`)
			out.WriteByte(digits[byte(r)>>4])
			out.WriteByte(digits[byte(r)&15])
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
}

// maxCanonicalNumberDigits bounds a number's expanded decimal form. JSON permits an exponent
// that would expand to gigabytes; no value a contract carries comes near this.
const maxCanonicalNumberDigits = 1024

// decimalNumber receives only JSON-validated number tokens. Working on digits
// avoids rounding integers above 2^53 or decimal thresholds through float64.
func decimalNumber(s string) (string, error) {
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	exponent := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		var err error
		exponent, err = strconv.Atoi(s[i+1:])
		if err != nil {
			return "", fmt.Errorf("canonical number exponent: %w", err)
		}
		s = s[:i]
	}
	fraction := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		fraction = len(s) - i - 1
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0", nil
	}
	// Bound arithmetic before expanding an exponent into a decimal string.
	maxInt := int(^uint(0) >> 1)
	if exponent < -maxInt+fraction || exponent > maxInt-len(s) {
		return "", fmt.Errorf("canonical number exponent overflows output length")
	}
	scale := fraction - exponent
	trimmed := strings.TrimRight(s, "0")
	scale -= len(s) - len(trimmed)
	s = trimmed
	// The expanded decimal is len(s) digits plus |scale| zeros; refuse one too long to be
	// a real limit, threshold or identifier before allocating it.
	if scale > maxCanonicalNumberDigits || -scale > maxCanonicalNumberDigits || len(s) > maxCanonicalNumberDigits {
		return "", fmt.Errorf("canonical number has more than %d digits once expanded", maxCanonicalNumberDigits)
	}
	switch {
	case scale <= 0:
		s += strings.Repeat("0", -scale)
	case scale < len(s):
		s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	default:
		s = "0." + strings.Repeat("0", scale-len(s)) + s
	}
	if negative {
		s = "-" + s
	}
	return s, nil
}
