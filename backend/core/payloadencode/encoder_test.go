// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloadencode

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
)

func TestEncode(t *testing.T) {
	for _, c := range []struct{ field, payload, want string }{
		{`{"param":"x","type":"u16be","scale":0.1}`, `{"x":21.5}`, "00d7"},
		{`{"param":"x","type":"u8"}`, `{"x":255}`, "ff"},
		{`{"param":"x","type":"i8"}`, `{"x":-128}`, "80"},
		{`{"param":"x","type":"u16le"}`, `{"x":4660}`, "3412"},
		{`{"param":"x","type":"u32be"}`, `{"x":305419896}`, "12345678"},
		{`{"param":"x","type":"u32le"}`, `{"x":305419896}`, "78563412"},
		{`{"param":"x","type":"i32be"}`, `{"x":-2147483648}`, "80000000"},
		{`{"param":"x","type":"i32le"}`, `{"x":-2147483648}`, "00000080"},
		{`{"param":"x","type":"i16be"}`, `{"x":-215}`, "ff29"},
		{`{"param":"x","type":"i16le","scale":2,"offset":10}`, `{"x":-420}`, "29ff"},
		{`{"param":"x","type":"bool"}`, `{"x":true}`, "01"},
		{`{"param":"x","type":"bool"}`, `{"x":false}`, "00"},
		{`{"const":"01ff"}`, `{}`, "01ff"},
		{`{"bits":{"type":"u8","fields":[{"param":"x","shift":0,"width":3},{"param":"y","shift":3,"width":1}]}}`, `{"x":5,"y":true}`, "0d"},
	} {
		e, err := Parse([]byte(`{"fPort":10,"confirmed":true,"fields":[` + c.field + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.Encode([]byte(c.payload))
		if err != nil || hex.EncodeToString(r.Data) != c.want || r.FPort != 10 || !r.Confirmed {
			t.Fatalf("%s: %+v %v", c.field, r, err)
		}
	}
}

func TestStaticRefusals(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"fPort":1,"fPort":2,"fields":[{"const":"01"}]}`, `{"fPort":0,"fields":[{"const":"01"}]}`, `{"fPort":224,"fields":[{"const":"01"}]}`,
		`{"fPort":1,"fields":[]}`, `{"fPort":1,"wat":1,"fields":[{"const":"01"}]}`,
		`{"fPort":1,"fields":[{"const":"1"}]}`, `{"fPort":1,"fields":[{"const":"zz"}]}`,
		`{"fPort":1,"fields":[{"const":""}]}`, `{"fPort":1,"fields":[{"const":"01","param":"x"}]}`,
		`{"fPort":1,"fields":[{"param":"x","type":"u8","extra":1}]}`,
		`{"fPort":1,"fields":[{"param":"x","type":"u8","scale":0}]}`,
		`{"fPort":1,"fields":[{"param":"x","type":"u8","scale":1e999}]}`,
		`{"fPort":1,"fields":[{"param":"x","type":"u8","offset":null}]}`,
		`{"fPort":1,"fields":[{"param":"x","type":"u8"},{"param":"x","type":"bool"}]}`,
		`{"fPort":1,"fields":[{"bits":{"type":"u8","fields":[{"param":"x","shift":0,"width":3},{"param":"y","shift":2,"width":1}]}}]}`,
		`{"fPort":1,"fields":[{"bits":{"type":"u8","fields":[{"param":"x","shift":7,"width":2}]}}]}`,
		`{"fPort":1,"fields":[{"const":"` + strings.Repeat("01", 243) + `"}]}`,
		`{"fPort":1,"fields":[` + strings.TrimSuffix(strings.Repeat(`{"const":"01"},`, 33), ",") + `]}`,
	} {
		if e, err := Parse([]byte(raw)); err == nil || e != nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestRuntimeRefusals(t *testing.T) {
	for _, c := range []struct {
		field  string
		values []string
	}{
		{`{"param":"x","type":"u8"}`, []string{`{}`, `{"x":256}`, `{"x":-1}`, `{"x":215.4}`, `{"x":null}`, `{"x":"1"}`, `{"x":true}`, `{"x":NaN}`, `{"x":Infinity}`, `{"x":-Infinity}`, `{"x":1e999}`, `null`, `[]`, `{"x":1} {}`}},
		{`{"param":"x","type":"bool"}`, []string{`{}`, `{"x":1}`, `{"x":null}`}},
		{`{"bits":{"type":"u8","fields":[{"param":"x","shift":0,"width":3}]}}`, []string{`{}`, `{"x":8}`, `{"x":-1}`, `{"x":0.5}`, `{"x":true}`}},
	} {
		e, err := Parse([]byte(`{"fPort":1,"fields":[` + c.field + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range c.values {
			r, err := e.Encode([]byte(p))
			if err == nil || r.Data != nil {
				t.Errorf("accepted %s: %+v %v", p, r, err)
			}
		}
	}
}

func TestSchemaLink(t *testing.T) {
	e, _ := Parse([]byte(`{"fPort":1,"fields":[{"param":"x","type":"u8"}]}`))
	for _, k := range []string{"INT", "DOUBLE"} {
		if err := e.ValidateAgainst([]ParamInfo{{"x", k}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, info := range [][]ParamInfo{nil, {{"x", "STRING"}}, {{"x", "BOOLEAN"}}, {{"x", "INT"}, {"hex", "STRING"}}} {
		if err := e.ValidateAgainst(info); err == nil {
			t.Fatal("accepted invalid linkage", info)
		}
	}
	b, _ := Parse([]byte(`{"fPort":1,"fields":[{"param":"x","type":"bool"}]}`))
	if b.ValidateAgainst([]ParamInfo{{"x", "INT"}}) == nil || b.ValidateAgainst([]ParamInfo{{"x", "BOOLEAN"}}) != nil {
		t.Fatal("bool linkage")
	}
}

func TestFieldGrammar(t *testing.T) {
	for _, field := range []string{
		`{}`, `null`, `[]`, `true`, `{"const":null}`, `{"const":123}`,
		`{"const":"01","type":"u8"}`, `{"const":"01","scale":1}`,
		`{"param":"","type":"u8"}`, `{"param":"x"}`, `{"type":"u8"}`,
		`{"param":"x","type":"u24be"}`, `{"param":42,"type":"u8"}`,
		`{"param":"x","type":"bool","offset":0}`, `{"param":"x","type":"bool","scale":1}`,
		`{"param":"x","type":"u8","offset":1e999}`, `{"param":"x","type":"u8","scale":"1"}`,
		`{"bits":{"type":"u8","fields":[]}}`, `{"bits":{"type":"bool","fields":[{"param":"x","shift":0,"width":1}]}}`,
		`{"bits":{"type":"u8","fields":[{"param":"x","width":1}]}}`,
		`{"bits":{"type":"u8","fields":[{"param":"x","shift":-1,"width":1}]}}`,
		`{"bits":{"type":"u8","fields":[{"param":"x","shift":0,"width":0}]}}`,
		`{"bits":{"type":"u32be","fields":[{"param":"x","shift":0,"width":33}]}}`,
		`{"bits":{"type":"u8","fields":[{"param":"x","shift":0,"width":1,"extra":1}]}}`,
		`{"bits":{"type":"u8","extra":1,"fields":[{"param":"x","shift":0,"width":1}]}}`,
		`{"param":"x","type":"u8","bits":{"type":"u8","fields":[{"param":"y","shift":0,"width":1}]}}`,
		`{"param":"x","type":"u8"},{"bits":{"type":"u8","fields":[{"param":"x","shift":0,"width":1}]}}`,
	} {
		if e, err := Parse([]byte(`{"fPort":1,"fields":[` + field + `]}`)); err == nil || e != nil {
			t.Errorf("accepted %s", field)
		}
	}
	for _, prefix := range []string{`"fPort":1.5`, `"fPort":"1"`, `"fPort":1,"confirmed":1`, `"fPort":1,"confirmed":null`} {
		if e, err := Parse([]byte(`{` + prefix + `,"fields":[{"const":"01"}]}`)); err == nil || e != nil {
			t.Errorf("accepted %s", prefix)
		}
	}
}

func TestEveryTypeRangeAndLengthLimits(t *testing.T) {
	for name, typ := range types {
		min, max := 0.0, math.Exp2(float64(8*typ.size))-1
		if typ.signed {
			min = -math.Exp2(float64(8*typ.size - 1))
			max = -min - 1
		}
		e, err := Parse([]byte(fmt.Sprintf(`{"fPort":1,"fields":[{"const":"01"},{"param":"x","type":%q}]}`, name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []float64{min - 1, max + 1, 215.4} {
			r, err := e.Encode([]byte(fmt.Sprintf(`{"x":%.17g}`, n)))
			if err == nil || r.Data != nil {
				t.Fatalf("%s accepted %v after constant: %+v %v", name, n, r, err)
			}
		}
	}
	e, err := Parse([]byte(`{"fPort":223,"fields":[{"const":"` + strings.Repeat("ab", 242) + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Encode([]byte(`{}`))
	if err != nil || len(r.Data) != 242 || r.FPort != 223 {
		t.Fatalf("maximum: %+v %v", r, err)
	}
}

func TestErrorDoesNotEchoValuesAndClipsNames(t *testing.T) {
	name := strings.Repeat("x", 500)
	e, err := Parse([]byte(fmt.Sprintf(`{"fPort":1,"fields":[{"param":%q,"type":"u8"}]}`, name)))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Encode([]byte(fmt.Sprintf(`{%q:"tenant-secret-value"}`, name)))
	if err == nil || r.Data != nil || strings.Contains(err.Error(), "tenant-secret-value") || len(err.(*EncodeError).Param) > 83 {
		t.Fatalf("unsafe refusal: %v", err)
	}
}

func TestEveryTypeRoundTripDecoderArithmetic(t *testing.T) {
	for name, typ := range types {
		max := int64(uint64(1)<<(8*typ.size)) - 1
		min := int64(0)
		if typ.signed {
			max = (int64(1) << (8*typ.size - 1)) - 1
			min = -max - 1
		}
		e, err := Parse([]byte(fmt.Sprintf(`{"fPort":1,"fields":[{"param":"x","type":%q,"scale":0.1,"offset":-5}]}`, name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range []int64{min, min + 1, 0, 1, max - 1, max} {
			// The uplink decoder's arithmetic: value = raw*scale+offset.
			value := float64(raw)*0.1 - 5
			r, err := e.Encode([]byte(fmt.Sprintf(`{"x":%.17g}`, value)))
			want := appendInt(nil, uint64(raw), typ)
			if err != nil || !bytes.Equal(r.Data, want) {
				t.Fatalf("%s raw %d: %x want %x: %v", name, raw, r.Data, want, err)
			}
		}
	}
}

func TestFuzzishInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	e, _ := Parse([]byte(`{"fPort":1,"fields":[{"const":"01"},{"param":"x","type":"i32le"}]}`))
	for i := 0; i < 2000; i++ {
		b := make([]byte, rng.Intn(256))
		_, _ = rng.Read(b)
		if parsed, err := Parse(b); err == nil {
			r, err := parsed.Encode(b)
			if err != nil && r.Data != nil {
				t.Fatal("bytes on error")
			}
		}
		r, err := e.Encode(b)
		if err != nil && r.Data != nil {
			t.Fatal("bytes on error")
		}
		if err == nil && (len(r.Data) < 1 || len(r.Data) > 242) {
			t.Fatal("length")
		}
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		r, err := e.Encode([]byte(fmt.Sprintf(`{"x":%v}`, v)))
		if err == nil || r.Data != nil {
			t.Fatal("nonfinite accepted")
		}
	}
}

func FuzzEncoder(f *testing.F) {
	f.Add([]byte(`{"fPort":1,"fields":[{"param":"x","type":"u8"}]}`), []byte(`{"x":2}`))
	f.Fuzz(func(t *testing.T, declaration, payload []byte) {
		e, err := Parse(declaration)
		if err != nil {
			return
		}
		r, err := e.Encode(payload)
		if err != nil {
			if r.Data != nil {
				t.Fatal("bytes on error")
			}
			return
		}
		if len(r.Data) < 1 || len(r.Data) > 242 {
			t.Fatal("invalid length")
		}
	})
}

func TestNumberBeyondExactFloatRangeIsRefused(t *testing.T) {
	e, err := Parse([]byte(`{"fPort":1,"fields":[{"param":"x","type":"u8","offset":0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := e.Encode([]byte(`{"x":9007199254740993}`)); err == nil {
		t.Fatalf("a value rounded by float64 must be refused, got %x", r.Data)
	}
}

func TestPrecisionAndRangeRefusals(t *testing.T) {
	for _, c := range []struct{ field, payload, reason string }{
		{`{"param":"x","type":"u8"}`, `{"x":255.4}`, "range"},
		{`{"param":"x","type":"u8"}`, `{"x":255.0000001}`, "integer"},
		{`{"param":"x","type":"u8"}`, `{"x":1.0000001}`, "integer"},
		{`{"param":"x","type":"u8","scale":1,"offset":0}`, `{"x":1.0000001}`, "integer"},
		{`{"param":"x","type":"i8"}`, `{"x":-128.0000001}`, "integer"},
		{`{"param":"x","type":"u8","scale":1e9}`, `{"x":1000000000000001}`, "too large"},
		{`{"param":"x","type":"u8","scale":1e-9}`, `{"x":9007199.254740992}`, "too large"},
		{`{"param":"x","type":"u8"}`, `{"x":9007199254740992}`, "too large"},
	} {
		e, err := Parse([]byte(`{"fPort":1,"fields":[` + c.field + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.Encode([]byte(c.payload))
		if err == nil || r.Data != nil || !strings.Contains(err.Error(), c.reason) {
			t.Fatalf("%s: %+v %v", c.payload, r, err)
		}
	}
}

func TestTransformPrecision(t *testing.T) {
	const precisionError = "scale and offset leave too little float64 precision: raise scale or lower offset"
	for _, c := range []struct {
		name, field, wantError string
	}{
		{"small scale large offset", `{"param":"x","type":"u32be","scale":1e-9,"offset":1e9}`, precisionError},
		{"negative scale and offset", `{"param":"x","type":"i32le","scale":-1e-9,"offset":-1e9}`, precisionError},
		{"signed normal transform", `{"param":"x","type":"i16be","scale":0.1,"offset":-40}`, ""},
		{"unsigned normal transform", `{"param":"x","type":"u16le","scale":0.01,"offset":1000}`, ""},
		{"default offset", `{"param":"x","type":"u32be","scale":1e-9}`, ""},
		{"default scale", `{"param":"x","type":"i32le","offset":1e9}`, ""},
		{"no transform", `{"param":"x","type":"u32be"}`, ""},
		{"identity transform", `{"param":"x","type":"u32be","scale":1,"offset":0}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, err := Parse([]byte(`{"fPort":1,"fields":[` + c.field + `]}`))
			if c.wantError != "" {
				var refusal *EncodeError
				if !errors.As(err, &refusal) || e != nil || refusal.Param != "x" || refusal.Reason != c.wantError || !strings.Contains(err.Error(), `parameter "x"`) {
					t.Fatalf("got encoder %v, error %v; want %q", e, err, c.wantError)
				}
			} else if err != nil || e == nil {
				t.Fatalf("expected valid transform: %v", err)
			}
		})
	}
}

func TestTransformPrecisionBoundary(t *testing.T) {
	// At scale 1e-9, the full 32-bit extent is below 8. Immediately
	// below offset 8 the ULP is 2^-50 (< 1e-15); at 8 it doubles
	// to 2^-49 (> 1e-15). These adjacent float64 offsets straddle the rule.
	for _, typ := range []string{"u32be", "i32le"} {
		for _, c := range []struct {
			offset float64
			valid  bool
		}{
			{math.Nextafter(8, 0), true},
			{8, false},
		} {
			raw := fmt.Sprintf(`{"fPort":1,"fields":[{"param":"setpoint","type":%q,"scale":1e-9,"offset":%.17g}]}`, typ, c.offset)
			e, err := Parse([]byte(raw))
			if c.valid {
				if err != nil || e == nil {
					t.Fatalf("%s offset %.17g should pass: %v", typ, c.offset, err)
				}
			} else {
				var refusal *EncodeError
				if e != nil || !errors.As(err, &refusal) || refusal.Param != "setpoint" {
					t.Fatalf("%s offset %.17g should refuse setpoint: %v", typ, c.offset, err)
				}
			}
		}
	}
}

func TestRefusedTransformCollapsesAdjacentRawSteps(t *testing.T) {
	e, err := Parse([]byte(`{"fPort":1,"fields":[{"param":"setpoint","type":"u32be"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// Install the refused transform directly to demonstrate what Encode would
	// do without Parse's guard. Production callers cannot mutate this document.
	scale, offset := 1e-9, 1e9
	e.doc.Fields[0].Scale = &scale
	e.doc.Fields[0].Offset = &offset
	var previous []byte
	for raw := 0; raw <= 1; raw++ {
		value := float64(raw)*scale + offset
		if value != offset {
			t.Fatalf("expected raw %d to collapse to offset, got %.17g", raw, value)
		}
		r, err := e.Encode([]byte(fmt.Sprintf(`{"setpoint":%.17g}`, value)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(r.Data, []byte{0, 0, 0, 0}) {
			t.Fatalf("raw %d encoded as %x; want raw 0", raw, r.Data)
		}
		if raw == 1 && !bytes.Equal(previous, r.Data) {
			t.Fatalf("adjacent raw steps should encode to identical bytes: %x vs %x", previous, r.Data)
		}
		previous = r.Data
	}
}

func TestTransformBounds(t *testing.T) {
	for _, member := range []string{`"offset":1000000001`, `"offset":-1000000001`, `"scale":1000000001`, `"scale":-1000000001`, `"scale":1e-10`, `"scale":-1e-10`, `"offset":1e15`} {
		// In particular, the old large offset could swallow the fraction in x=1000000000000000.01.
		if _, err := Parse([]byte(`{"fPort":1,"fields":[{"param":"x","type":"u8",` + member + `}]}`)); err == nil {
			t.Fatal("accepted", member)
		}
	}
	for _, member := range []string{`"offset":1e9`, `"offset":-1e9`, `"scale":1e9`, `"scale":-1e9`, `"scale":1e-9`, `"scale":-1e-9`} {
		if _, err := Parse([]byte(`{"fPort":1,"fields":[{"param":"x","type":"u8",` + member + `}]}`)); err != nil {
			t.Fatal(member, err)
		}
	}
}

func TestUnusedAndDuplicatePayloadKeys(t *testing.T) {
	e, _ := Parse([]byte(`{"fPort":1,"fields":[{"param":"x","type":"u8"}]}`))
	for _, payload := range []string{`{"x":1,"ignored":12}`, `{"x":1,"x":2}`, `{"x":1,"\u0078":2}`} {
		r, err := e.Encode([]byte(payload))
		if err == nil || r.Data != nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	name := strings.Repeat("k", 500)
	_, err := e.Encode([]byte(fmt.Sprintf(`{"x":1,%q:12}`, name)))
	if err == nil || !strings.Contains(err.Error(), name[:80]) || strings.Contains(err.Error(), name) {
		t.Fatalf("unclipped key: %v", err)
	}
}

func TestBoundaryValueWithFloatErrorIsEncoded(t *testing.T) {
	e, err := Parse([]byte(`{"fPort":1,"fields":[{"param":"x","type":"u8","scale":0.7}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Encode([]byte(`{"x":178.5}`))
	if err != nil || hex.EncodeToString(r.Data) != "ff" {
		t.Fatalf("got %x %v", r.Data, err)
	}
	if _, err := e.Encode([]byte(`{"x":179.2}`)); err == nil {
		t.Fatal("above the range must be refused")
	}
}
