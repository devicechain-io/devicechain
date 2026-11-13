// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package payloadencode compiles declarative LoRaWAN application payload encoders.
package payloadencode

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

type document struct {
	FPort     int     `json:"fPort"`
	Confirmed bool    `json:"confirmed"`
	Fields    []field `json:"fields"`
}
type field struct {
	Const  *string   `json:"const,omitempty"`
	Param  *string   `json:"param,omitempty"`
	Type   string    `json:"type,omitempty"`
	Scale  *float64  `json:"scale,omitempty"`
	Offset *float64  `json:"offset,omitempty"`
	Bits   *bitField `json:"bits,omitempty"`
}
type bitField struct {
	Type   string `json:"type"`
	Fields []bit  `json:"fields"`
}
type bit struct {
	Param string `json:"param"`
	Shift *int   `json:"shift"`
	Width int    `json:"width"`
}
type numberType struct {
	size           int
	signed, little bool
}

// Precision limits keep scale/offset arithmetic from swallowing meaningful fractions.
// Integers at 2^53 lose float64 precision; the tighter input cap retains fractional headroom.
const (
	maxExactInteger       = 1 << 53
	maxInputMagnitude     = 1e15
	maxTransformMagnitude = 1e9
	minScaleMagnitude     = 1e-9
	rawIntegerTolerance   = 1e-6
)

// Names match the uplink frame-reader vocabulary.
var types = map[string]numberType{
	"u8": {1, false, false}, "i8": {1, true, false},
	"u16be": {2, false, false}, "u16le": {2, false, true},
	"i16be": {2, true, false}, "i16le": {2, true, true},
	"u32be": {4, false, false}, "u32le": {4, false, true},
	"i32be": {4, true, false}, "i32le": {4, true, true},
}

// Encoder is immutable after Parse and safe for concurrent use.
type Encoder struct {
	doc    document
	length int
	params map[string]string
}

// ParamInfo describes one scalar parameter; Kind is INT, DOUBLE, BOOLEAN or STRING.
type ParamInfo struct{ Name, Kind string }

// Result is the complete application downlink, independent of its network server.
type Result struct {
	FPort     int
	Confirmed bool
	Data      []byte
}

// EncodeError contains only a clipped parameter name and a fixed refusal reason.
type EncodeError struct{ Param, Reason string }

func (e *EncodeError) Error() string { return fmt.Sprintf("parameter %q: %s", clip(e.Param), e.Reason) }
func clip(s string) string {
	if len(s) > 80 {
		s = strings.ToValidUTF8(s[:80], "") + "..."
	}
	return s
}
func refuse(p, r string) error { return &EncodeError{clip(p), r} }

// uniqueKeys walks tokens before decoding so duplicate object keys cannot be overwritten.
func uniqueKeys(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid object key")
			}
			if seen[name] {
				return refuse(name, "duplicate JSON key")
			}
			seen[name] = true
			if err := uniqueKeys(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueKeys(d); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}

func strict(raw []byte, dst any) error {
	tokens := json.NewDecoder(bytes.NewReader(raw))
	tokens.UseNumber()
	if err := uniqueKeys(tokens); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON document")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

// Parse validates grammar, parameter uniqueness, bit layout and static length.
func Parse(raw []byte) (*Encoder, error) {
	// Null is never an alternative spelling of an omitted member.
	var obj any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("invalid encoder JSON")
	}
	var noNull func(any) bool
	noNull = func(v any) bool {
		switch x := v.(type) {
		case nil:
			return false
		case map[string]any:
			for _, v := range x {
				if !noNull(v) {
					return false
				}
			}
		case []any:
			for _, v := range x {
				if !noNull(v) {
					return false
				}
			}
		}
		return true
	}
	if !noNull(obj) {
		return nil, fmt.Errorf("encoder members cannot be null")
	}
	e := &Encoder{params: map[string]string{}}
	if err := strict(raw, &e.doc); err != nil {
		return nil, err
	}
	if e.doc.FPort < 1 || e.doc.FPort > 223 || len(e.doc.Fields) < 1 || len(e.doc.Fields) > 32 {
		return nil, fmt.Errorf("encoder needs fPort 1..223 and 1..32 fields")
	}
	add := func(p, k string) error {
		if p == "" {
			return refuse(p, "invalid parameter name")
		}
		if _, ok := e.params[p]; ok {
			return refuse(p, "duplicate encoder parameter")
		}
		e.params[p] = k
		return nil
	}
	for _, f := range e.doc.Fields {
		switch {
		case f.Const != nil:
			if f.Param != nil || f.Type != "" || f.Scale != nil || f.Offset != nil || f.Bits != nil {
				return nil, fmt.Errorf("const field has incompatible members")
			}
			b, err := hex.DecodeString(*f.Const)
			if err != nil {
				return nil, fmt.Errorf("constant must be even-length hex")
			}
			e.length += len(b)
		case f.Bits != nil:
			if f.Param != nil || f.Type != "" || f.Scale != nil || f.Offset != nil {
				return nil, fmt.Errorf("bits field has incompatible members")
			}
			t, ok := types[f.Bits.Type]
			if !ok {
				return nil, fmt.Errorf("unknown bits type")
			}
			if len(f.Bits.Fields) < 1 || len(f.Bits.Fields) > 32 {
				return nil, fmt.Errorf("bits needs 1..32 fields")
			}
			var used uint64
			for _, b := range f.Bits.Fields {
				if b.Shift == nil || *b.Shift < 0 || b.Width < 1 || b.Width > 32 || *b.Shift > t.size*8-b.Width {
					return nil, refuse(b.Param, "bits do not fit type")
				}
				mask := ((uint64(1) << b.Width) - 1) << *b.Shift
				if used&mask != 0 {
					return nil, refuse(b.Param, "overlapping bits")
				}
				used |= mask
				k := "number"
				if b.Width == 1 {
					k = "bit"
				}
				if err := add(b.Param, k); err != nil {
					return nil, err
				}
			}
			e.length += t.size
		case f.Param != nil:
			k := "number"
			t, ok := types[f.Type]
			if f.Type == "bool" {
				k = "bool"
				t.size = 1
			} else if !ok {
				return nil, fmt.Errorf("unknown numeric type")
			}
			if f.Type == "bool" && (f.Scale != nil || f.Offset != nil) {
				return nil, fmt.Errorf("bool cannot have scale or offset")
			}
			if f.Scale != nil && (!finite(*f.Scale) || math.Abs(*f.Scale) < minScaleMagnitude || math.Abs(*f.Scale) > maxTransformMagnitude) {
				return nil, fmt.Errorf("scale magnitude must be finite and between 1e-9 and 1e9")
			}
			if f.Offset != nil && (!finite(*f.Offset) || math.Abs(*f.Offset) > maxTransformMagnitude) {
				return nil, fmt.Errorf("offset magnitude must be finite and at most 1e9")
			}
			if f.Scale != nil || f.Offset != nil {
				scale, offset := 1.0, 0.0
				if f.Scale != nil {
					scale = math.Abs(*f.Scale)
				}
				if f.Offset != nil {
					offset = math.Abs(*f.Offset)
				}
				// Use the full bit width for both signed and unsigned fields. Keep
				// float64 spacing within the raw-step tolerance used by Encode.
				m := math.Max(offset, scale*math.Exp2(float64(8*t.size)))
				if math.Nextafter(m, math.Inf(1))-m > scale*rawIntegerTolerance {
					return nil, refuse(*f.Param, "scale and offset leave too little float64 precision: raise scale or lower offset")
				}
			}
			if err := add(*f.Param, k); err != nil {
				return nil, err
			}
			e.length += t.size
		default:
			return nil, fmt.Errorf("field needs const, param or bits")
		}
	}
	if e.length < 1 || e.length > 242 {
		return nil, fmt.Errorf("encoded length must be 1..242 bytes")
	}
	return e, nil
}

// ValidateAgainst links encoder parameters to the command's scalar schema.
func (e *Encoder) ValidateAgainst(params []ParamInfo) error {
	kinds := map[string]string{}
	for _, p := range params {
		switch p.Name {
		case "fPort", "hex", "base64", "confirmed":
			return refuse(p.Name, "reserved raw parameter cannot be declared with an encoder")
		}
		kinds[p.Name] = p.Kind
	}
	for p, k := range e.params {
		dt := kinds[p]
		valid := dt == "INT" || dt == "DOUBLE"
		if k == "bool" {
			valid = dt == "BOOLEAN"
		}
		if k == "bit" {
			valid = valid || dt == "BOOLEAN"
		}
		if !valid {
			return refuse(p, "missing or incompatible parameter schema")
		}
	}
	return nil
}
func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func value(obj map[string]json.RawMessage, p string, boolean bool) (float64, error) {
	r, ok := obj[p]
	if !ok {
		return 0, refuse(p, "required encoder parameter is missing")
	}
	if boolean {
		if bytes.Equal(r, []byte("true")) {
			return 1, nil
		}
		if bytes.Equal(r, []byte("false")) {
			return 0, nil
		}
	}
	var n float64
	if bytes.Equal(r, []byte("null")) || json.Unmarshal(r, &n) != nil || !finite(n) {
		return 0, refuse(p, "expected a finite number")
	}
	if math.Abs(n) >= maxExactInteger || math.Abs(n) > maxInputMagnitude {
		return 0, refuse(p, "number is too large to encode exactly")
	}
	return n, nil
}
func appendInt(dst []byte, n uint64, t numberType) []byte {
	for i := 0; i < t.size; i++ {
		shift := 8 * (t.size - 1 - i)
		if t.little {
			shift = 8 * i
		}
		dst = append(dst, byte(n>>shift))
	}
	return dst
}

// Encode refuses missing, wrongly typed, fractional or out-of-range values. Defaults
// are never injected, and errors always return a zero Result with no bytes.
func (e *Encoder) Encode(payload []byte) (Result, error) {
	var obj map[string]json.RawMessage
	if err := strict(payload, &obj); err != nil {
		if e, ok := err.(*EncodeError); ok {
			return Result{}, e
		}
		return Result{}, refuse("(payload)", "expected a JSON object")
	}
	if obj == nil {
		return Result{}, refuse("(payload)", "expected a JSON object")
	}
	for key := range obj {
		if _, ok := e.params[key]; !ok {
			return Result{}, refuse(key, "unused payload key")
		}
	}
	data := make([]byte, 0, e.length)
	for _, f := range e.doc.Fields {
		if f.Const != nil {
			b, _ := hex.DecodeString(*f.Const)
			data = append(data, b...)
			continue
		}
		if f.Bits != nil {
			var n uint64
			for _, b := range f.Bits.Fields {
				v, err := value(obj, b.Param, b.Width == 1)
				if err != nil {
					return Result{}, err
				}
				if math.Trunc(v) != v || v < 0 || v > float64((uint64(1)<<b.Width)-1) {
					return Result{}, refuse(b.Param, "bit value must be an integer within width")
				}
				n |= uint64(v) << *b.Shift
			}
			data = appendInt(data, n, types[f.Bits.Type])
			continue
		}
		p := *f.Param
		if f.Type == "bool" {
			r := bytes.TrimSpace(obj[p])
			if !bytes.Equal(r, []byte("true")) && !bytes.Equal(r, []byte("false")) {
				return Result{}, refuse(p, "expected a boolean")
			}
			v := byte(0)
			if bytes.Equal(r, []byte("true")) {
				v = 1
			}
			data = append(data, v)
			continue
		}
		v, err := value(obj, p, false)
		if err != nil {
			return Result{}, err
		}
		scale, offset := 1.0, 0.0
		if f.Scale != nil {
			scale = *f.Scale
		}
		if f.Offset != nil {
			offset = *f.Offset
		}
		raw := (v - offset) / scale
		if !finite(raw) || math.Abs(raw) >= maxExactInteger {
			return Result{}, refuse(p, "raw value is too large to encode exactly")
		}
		t := types[f.Type]
		min, max := 0.0, math.Exp2(float64(t.size*8))-1
		if t.signed {
			min = -math.Exp2(float64(t.size*8 - 1))
			max = math.Exp2(float64(t.size*8-1)) - 1
		}
		// The range is checked before rounding (255.4 must not round into u8) but with the same
		// tolerance as integrality, so float error at an exact boundary (178.5/0.7) is not a refusal.
		if raw < min-rawIntegerTolerance || raw > max+rawIntegerTolerance {
			return Result{}, refuse(p, "value is outside the encoded type range")
		}
		n := math.Round(raw)
		if (scale == 1 && offset == 0 && math.Trunc(v) != v) || math.Abs(raw-n) > rawIntegerTolerance {
			return Result{}, refuse(p, "raw value must be an integer; no rounding or truncation")
		}
		data = appendInt(data, uint64(int64(n)), t)
	}
	return Result{e.doc.FPort, e.doc.Confirmed, data}, nil
}
