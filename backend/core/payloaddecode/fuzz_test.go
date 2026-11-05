// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"fmt"
	"sync"
	"testing"
)

// fuzzDocs are decoders the input fuzzer drives with arbitrary frames and JSON. Between them
// they reach every native, the JSON path and the map path.
var fuzzDocs = []string{
	frameDoc("t", "double(i16be(frame, 1)) / 10.0", "d", "bit(frame, 3, 0)", "u", "u32le(frame, 4)", "f", "f32be(frame, 8)"),
	`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"map":"lpp.decode(frame)","prefix":"p.","maxKeys":64}]}]}`,
	`{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"h","expr":"slice(frame, 2, size(frame) - 2)"},{"name":"m","expr":"lpp.decode(h)"}],
	  "sections":[{"name":"a","when":"fPort == 1 || size(frame) > 4","outputs":[{"key":"x","expr":"double(bcd(h, 0, 4)) + double(sbits(h, 1, 3, 12))"},{"map":"m","maxKeys":8}]},{"name":"b","ignore":true}]}`,
	`{"kind":"CEL","envVersion":1,"input":"JSON","let":[{"name":"b","expr":"hex.decode(json.p)"}],
	  "sections":[{"name":"s","when":"has(json.p)","outputs":[{"key":"x","expr":"double(u16be(b, 0))"},{"key":"y","expr":"double(json.a.b) * 2.0","when":"has(json.a.b)"},{"key":"z","expr":"math.clamp(double(json.k), -5.0, 5.0)"}]}]}`,
	`{"kind":"CEL","envVersion":1,"input":"DECODED","let":[{"name":"b","expr":"base64.decode(decoded.raw)"}],
	  "sections":[{"name":"s","outputs":[{"key":"x","expr":"double(i32be(b, 0))"},{"key":"y","expr":"double(decoded.t)"}]}]}`,
}

// fuzzPlans is built on first use, not at package initialisation: the test-only natives are
// registered by an init(), and the environment must not be built before they are.
var fuzzPlans = sync.OnceValue(func() []*Plan {
	out := make([]*Plan, len(fuzzDocs))
	for i, js := range fuzzDocs {
		d, err := ParseDocument([]byte(js))
		if err != nil {
			panic(err)
		}
		p, err := Compile(d, nil)
		if err != nil {
			panic(fmt.Sprintf("fuzz doc %d: %v", i, err))
		}
		out[i] = p
	}
	return out
})

// FuzzDecodeInputs: whatever bytes arrive, a decode neither panics nor spends more than its
// static estimate, and decoding twice gives the same answer.
func FuzzDecodeInputs(f *testing.F) {
	trackAll(f)
	f.Add([]byte{1, 0, 0xd7, 1, 1, 0, 0, 0, 0x41, 0xac, 0, 0}, `{"p":"0a0b","a":{"b":1},"k":9}`, int64(2))
	f.Add([]byte{1, 0x67, 1, 0x10, 2, 0x68, 0x65}, `{"p":"00"}`, int64(-1))
	f.Add([]byte{}, `{}`, int64(0))
	f.Add(make([]byte, MaxFrameBytes), `{"raw":"AAAAAA=="}`, int64(1))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, `{"p":123,"a":[],"k":"x","raw":5,"t":null}`, int64(1))
	f.Fuzz(func(t *testing.T, frame []byte, js string, port int64) {
		for i, p := range fuzzPlans() {
			in := Input{Frame: frame, JSON: []byte(js), Decoded: []byte(js)}
			if port >= 0 {
				in.LoRa = &LoRa{FPort: port % 256, FCnt: 1}
			}
			res, derr := p.Decode(in)
			checkInvariants(t, fmt.Sprintf("plan %d", i), p, in, res, derr)
			res2, derr2 := p.Decode(in)
			if (derr == nil) != (derr2 == nil) || (derr != nil && derr.Error() != derr2.Error()) {
				t.Fatalf("plan %d: not deterministic: %v vs %v", i, derr, derr2)
			}
			if derr == nil && fmt.Sprint(res.Readings) != fmt.Sprint(res2.Readings) {
				t.Fatalf("plan %d: readings differ between runs", i)
			}
		}
	})
}

// FuzzCompileDecode: an arbitrary expression either is refused or, if it compiles, obeys the
// cost invariant on every property input. Compile must never panic on any text.
func FuzzCompileDecode(f *testing.F) {
	trackAll(f)
	for _, s := range []string{
		`double(i16be(frame, 1)) / 10.0`, `u8(frame, 0) > 3 ? 1 : 0`, `double(json.a.b)`,
		`size(slice(frame, 0, 4096))`, `bit(frame, 0, 7)`, `u8(hex.decode(json.p), 0)`,
		`math.pow(2.0, 3) + math.sqrt(2.0)`, `bcd(frame, 0, 4)`, `sbits(frame, 1, 3, 12)`,
		`double(u32be(frame, 0)) * 1.0e9 * 1.0e9 * 1.0e9`, `u8(frame, 0) / (u8(frame, 1) - u8(frame, 1))`,
		`[1,2].map(x, x)`, `json.a[json.b]`, `((((((((((((((((((((((((((((((((((1))))))))))))))))))))))))))))))))))`,
		`int(json.k) in [1, 2, 3] ? 1 : 0`, `has(json.a.b.c.d) ? 1 : 0`,
	} {
		f.Add(s)
	}
	inputs := propertyInputs()
	f.Fuzz(func(t *testing.T, expr string) {
		js := fmt.Sprintf(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"v","expr":%q}]}]}`, expr)
		doc, err := ParseDocument([]byte(js))
		if err != nil {
			return
		}
		p, err := Compile(doc, nil)
		if err != nil {
			return
		}
		for k, in := range inputs {
			res, derr := p.Decode(in)
			checkInvariants(t, fmt.Sprintf("input %d expr %q", k, expr), p, in, res, derr)
		}
	})
}
