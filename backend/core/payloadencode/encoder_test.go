// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloadencode

import (
	"bytes"
	"encoding/hex"
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
		{`{"param":"x","type":"bool"}`, `{"x":true,"ignored":12}`, "01"},
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
		`null`, `{}`, `{"fPort":0,"fields":[{"const":"01"}]}`, `{"fPort":224,"fields":[{"const":"01"}]}`,
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
	e, err := Parse([]byte(`{"fPort":1,"fields":[{"param":"x","type":"u8","offset":9007199254740992}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := e.Encode([]byte(`{"x":9007199254740993}`)); err == nil {
		t.Fatalf("a value rounded by float64 must be refused, got %x", r.Data)
	}
}
