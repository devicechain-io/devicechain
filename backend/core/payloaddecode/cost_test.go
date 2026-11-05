// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// The composition: every let + every section guard + the most expensive section's rows.
func TestCostComposition(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME",
	 "let":[{"name":"a","expr":"u8(frame, 0)"},{"name":"b","expr":"size(slice(frame, 0, 100))"}],
	 "sections":[
	  {"name":"one","when":"a == 1","outputs":[{"key":"x","expr":"double(u8(frame,1))"}]},
	  {"name":"two","when":"a == 2 && b > 3","outputs":[
	     {"key":"x","expr":"double(u16be(frame,1)) * 2.0","when":"b > 1"},
	     {"key":"y","expr":"double(size(slice(frame, 0, 500)))"}]},
	  {"name":"three","ignore":true}]}`
	p := mustCompile(t, doc, nil)
	var lets, whens, worst uint64
	for _, l := range p.lets {
		lets += l.expr.cost
	}
	for _, s := range p.sections {
		var secRows uint64
		if s.when != nil {
			whens += s.when.cost
		}
		for _, r := range s.rows {
			if r.when != nil {
				secRows += r.when.cost
			}
			secRows += r.value.cost
		}
		worst = max(worst, secRows)
	}
	if want := lets + whens + worst; p.EstimatedCost() != want {
		t.Fatalf("estimated %d, composition says %d (lets %d, whens %d, worst %d)", p.EstimatedCost(), want, lets, whens, worst)
	}
	// The worst section is "two": the guard of every section is charged, not just the match.
	if whens == 0 || worst < 500 {
		t.Fatalf("unexpected parts: whens %d worst %d", whens, worst)
	}
	// Actual cost never exceeds it, on any section.
	for _, f := range []string{"0102", "0201f4", "0203" + strings.Repeat("00", 600), "09"} {
		if r, err := p.Decode(frameOf(f)); err == nil && r.Cost > p.EstimatedCost() {
			t.Fatalf("%s: actual %d above estimate %d", f, r.Cost, p.EstimatedCost())
		}
	}
}

// A map row is charged its maxKeys on top of its source.
func TestMapRowChargesMaxKeys(t *testing.T) {
	mk := func(n int) *Plan {
		return mustCompile(t, fmt.Sprintf(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"map":"lpp.decode(frame)","maxKeys":%d}]}]}`, n), nil)
	}
	if d := mk(40).EstimatedCost() - mk(8).EstimatedCost(); d != 32 {
		t.Fatalf("maxKeys 40 vs 8 should differ by 32, got %d", d)
	}
}

// The runtime CostLimit is the authority: a function that spends more than its static
// estimate is stopped as cost_exceeded.
func TestRuntimeCostLimitIsEnforced(t *testing.T) {
	p := mustCompile(t, frameDoc("v", "test.underRuntime(frame)"), nil)
	_, err := p.Decode(frameOf("0102"))
	if err == nil || err.Reason != ReasonCostExceeded {
		t.Fatalf("want cost_exceeded, got %v", err)
	}
	if err.Path != "sections[0].outputs[0].expr" {
		t.Fatalf("path %q", err.Path)
	}
}

func TestCeilingsAreEnforcedAndNamed(t *testing.T) {
	// Per expression: three full-frame reads in one expression.
	one := "double(size(slice(frame, 0, 4096)))"
	big := one + " + " + one + " + " + one
	_, err := Compile(mustDoc(t, frameDoc("v", big)), nil)
	var ce *CostError
	if !errors.As(err, &ce) || ce.Path != "sections[0].outputs[0].expr" || ce.Ceiling != ExprCostCeiling {
		t.Fatalf("want a CostError naming the expression, got %v", err)
	}
	// A maximal single read fits the per-expression ceiling.
	if _, err := Compile(mustDoc(t, frameDoc("v", "double(size(slice(frame, 0, 4096)))")), nil); err != nil {
		t.Fatalf("a full 4 KiB read must fit: %v", err)
	}
	// Per decoder: the sum of lets.
	var lets []string
	for i := 0; i < 13; i++ {
		lets = append(lets, fmt.Sprintf(`{"name":"l%d","expr":"size(slice(frame, 0, 4096))"}`, i))
	}
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[` + strings.Join(lets, ",") + `],"sections":[{"name":"s","outputs":[{"key":"v","expr":"1"}]}]}`
	_, err = Compile(mustDoc(t, doc), nil)
	if !errors.As(err, &ce) || ce.Path != "" || ce.Ceiling != DecoderCostCeiling {
		t.Fatalf("want a decoder-wide CostError, got %v", err)
	}
}

// ResultSize propagates through nested calls and let bindings: slicing 8 bytes then decoding
// them costs ~8, not ~4096.
func TestResultSizePropagates(t *testing.T) {
	nested := mustCompile(t, frameDoc("v", "double(size(slice(slice(frame, 0, 8), 0, 4)))"), nil)
	if c := nested.sections[0].rows[0].value.cost; c > 40 {
		t.Fatalf("nested slices should cost about 12, got %d", c)
	}
	viaLet := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"b","expr":"slice(frame, 0, 7)"}],
	  "sections":[{"name":"s","outputs":[{"map":"lpp.decode(b)","maxKeys":4}]}]}`, nil)
	if c := viaLet.sections[0].rows[0].value.cost; c > 40 {
		t.Fatalf("lpp.decode of an 8-byte let should cost about 10, got %d", c)
	}
	// Without a literal length the bound is the source's: still sound, just larger.
	dyn := mustCompile(t, frameDoc("v", "double(size(slice(frame, 1, size(frame) - 1)))"), nil)
	if c := dyn.sections[0].rows[0].value.cost; c < 4096 {
		t.Fatalf("a computed length must be charged the source's maximum, got %d", c)
	}
	// And the actual cost of an 8-byte run stays under the let-based estimate.
	r := decodeOK(t, viaLet, frameOf("0167011002686500"))
	if r.Cost > viaLet.EstimatedCost() {
		t.Fatalf("actual %d above estimate %d", r.Cost, viaLet.EstimatedCost())
	}
}

// A hex.decode of an unsized dynamic string is charged its cap, and a longer text is refused
// by the native before it does any work.
func TestTextNativesCapBeforeWork(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":"u8(hex.decode(json.p), 0)"}]}]}`
	p := mustCompile(t, doc, nil)
	if c := p.sections[0].rows[0].value.cost; c < MaxTextBytes {
		t.Fatalf("an unsized string must be charged the cap, got %d", c)
	}
	if r := decodeOK(t, p, Input{JSON: []byte(`{"p":"0a0b"}`)}); readingsMap(r)["v"] != "10" {
		t.Fatalf("%v", r.Readings)
	}
	long := strings.Repeat("00", MaxTextBytes/2+1)
	_, err := p.Decode(Input{JSON: []byte(`{"p":"` + long + `"}`)})
	if err == nil || err.Reason != ReasonEvalError || !strings.Contains(err.Message, "above the cap") {
		t.Fatalf("want the native to refuse, got %v", err)
	}
	b64 := mustCompile(t, strings.Replace(doc, "hex.decode", "base64.decode", 1), nil)
	_, err = b64.Decode(Input{JSON: []byte(`{"p":"` + strings.Repeat("AAAA", 1500) + `"}`)})
	if err == nil || !strings.Contains(err.Message, "above the cap") {
		t.Fatalf("want base64 to refuse a text decoding past 4096 bytes, got %v", err)
	}
}

func TestMaxReadings(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[
	  {"name":"a","when":"fPort == 1","outputs":[{"key":"x","expr":"1"},{"key":"y","expr":"2"}]},
	  {"name":"b","when":"fPort == 2","outputs":[{"key":"x","expr":"1"},{"map":"lpp.decode(frame)","maxKeys":8},{"map":"lpp.decode(frame)","prefix":"q.","maxKeys":5}]},
	  {"name":"c","ignore":true}]}`
	if got := mustCompile(t, doc, nil).MaxReadings(); got != 14 {
		t.Fatalf("maxReadings = %d, want max(2, 1+8+5) = 14", got)
	}
}

// A9 (B0b half): the same input gives byte-identical readings every time, with map outputs in
// sorted key order whatever the map's iteration order.
func TestDeterministicMapOrder(t *testing.T) {
	p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[
	  {"key":"first","expr":"u8(frame, 0)"},{"map":"lpp.decode(frame)","prefix":"p.","maxKeys":32}]}]}`, nil)
	in := frameOf("0167011002686503670102" + "0468ff" + "05670103" + "0668aa" + "07670104" + "0868bb" + "09670105" + "0a68cc")
	var want string
	for i := 0; i < 1000; i++ {
		r := decodeOK(t, p, in)
		var b strings.Builder
		for _, rd := range r.Readings {
			b.WriteString(rd.Key + "=" + rd.Value + ";")
		}
		if i == 0 {
			want = b.String()
			continue
		}
		if b.String() != want {
			t.Fatalf("run %d differs:\n%s\n%s", i, b.String(), want)
		}
	}
	if !strings.HasPrefix(want, "first=1;p.humidity_10=") {
		t.Fatalf("map keys must follow the scalar, sorted: %s", want)
	}
}

func TestConcurrentDecode(t *testing.T) {
	p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"JSON","let":[{"name":"t","expr":"double(json.t)"}],
	  "sections":[{"name":"s","outputs":[{"key":"x","expr":"t * 2.0"}]}]}`, nil)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r, err := p.Decode(Input{JSON: []byte(fmt.Sprintf(`{"t":%d}`, g*1000+i))})
				if err != nil || readingsMap(r)["x"] != fmt.Sprint((g*1000+i)*2) {
					t.Errorf("g%d i%d: %v %v", g, i, r, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A8: an input over a cap is refused before any evaluation: no cost spent, no allocation
// beyond the error itself.
func TestOversizeFrameRefusedBeforeEval(t *testing.T) {
	// A let that always runs, so a decode that got as far as evaluating spends cost.
	p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"n","expr":"size(frame)"}],
	  "sections":[{"name":"s","outputs":[{"key":"v","expr":"double(n)"}]}]}`, nil)
	big := make([]byte, MaxFrameBytes+1)
	_, err := p.Decode(Input{Frame: big})
	if err == nil || err.Reason != ReasonInputTooLarge || err.Cost != 0 {
		t.Fatalf("want input_too_large with no cost spent, got %+v", err)
	}
	allocs := testing.AllocsPerRun(50, func() { _, _ = p.Decode(Input{Frame: big}) })
	if allocs > 4 {
		t.Fatalf("a refused frame should allocate only its error, got %v allocs", allocs)
	}
}

// Native error text carries only the function name and the offset.
func TestErrorTextHygiene(t *testing.T) {
	p := mustCompile(t, frameDoc("v", "double(u32be(frame, 2))"), nil)
	_, err := p.Decode(frameOf("deadbeef"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "eval_error: sections[0].outputs[0].expr: u32be: reads 4 byte(s) at offset 2 of a 4-byte frame (offset 2)" {
		t.Fatalf("unexpected text: %q", err.Error())
	}
}

func TestPanicIsRecovered(t *testing.T) {
	p := mustCompile(t, frameDoc("v", "double(u8(frame, 0))"), nil)
	p.sections[0].rows[0].value = nil // a corrupted plan
	_, err := p.Decode(frameOf("01"))
	if err == nil || err.Reason != ReasonEvalError {
		t.Fatalf("a panic inside decode must become an eval_error, got %v", err)
	}
}

// Parsing a JSON input is real CPU the expression estimate cannot see, so it is priced by size:
// the actual run charges it, CostFor prices it by the real input, EstimatedCost by the maximum.
func TestJSONParseIsCharged(t *testing.T) {
	p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":"double(json.a)"}]}]}`, nil)
	small := Input{JSON: []byte(`{"a":1}`)}
	pad := `{"a":1,"pad":"` + strings.Repeat("x", 50_000) + `"}`
	big := Input{JSON: []byte(pad)}
	rs, rb := decodeOK(t, p, small), decodeOK(t, p, big)
	if rb.Cost < rs.Cost+ParseCost(50_000)-ParseCost(10) {
		t.Fatalf("a 50 KB input must cost its parse: small %d, big %d", rs.Cost, rb.Cost)
	}
	if rb.Cost > p.CostFor(big) || p.CostFor(big) > p.EstimatedCost() || rs.Cost > p.CostFor(small) {
		t.Fatalf("actual %d / CostFor %d / EstimatedCost %d out of order", rb.Cost, p.CostFor(big), p.EstimatedCost())
	}
	if ParseCost(MaxJSONBytes) != MaxJSONBytes {
		t.Fatalf("ParseCost(64 KiB) = %d", ParseCost(MaxJSONBytes))
	}
	// A frame decoder reads no JSON: its parse term is zero.
	fp := mustCompile(t, frameDoc("v", "u8(frame, 0)"), nil)
	if fp.EstimatedCost() != fp.CostFor(Input{JSON: make([]byte, 60_000)}) {
		t.Fatal("a decoder that never reads json must not be charged for it")
	}
}

// Which expressions run under the runtime tracker: anything calling a size-dependent native,
// and anything the static estimate does not show to be cheap. A plain tree of O(1) operations
// is charged at its estimate and not watched.
func TestTrackedExpressionPolicy(t *testing.T) {
	cases := []struct {
		expr    string
		tracked bool
	}{
		{"double(i16be(frame, 1)) / 10.0", false},
		{"u8(frame, 0) * 2 + bit(frame, 1, 2)", false},
		{"bcd(frame, 0, 8)", false},
		{"double(json.a.b) + double(link.rssi)", false},
		{"size(slice(frame, 0, 4))", true},
		{"u8(hex.decode(\"0a\"), 0)", true},
		{"u8(base64.decode(\"AQ==\"), 0)", true},
		{"size(frame) + " + strings.Repeat("u8(frame, 0) + ", 30) + "1", true}, // cheap per node, expensive in total
	}
	for _, c := range cases {
		p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"v","expr":`+fmt.Sprintf("%q", c.expr)+`}]}]}`, nil)
		if got := p.sections[0].rows[0].value.tracked; got != c.tracked {
			t.Errorf("%s: tracked = %v, want %v (cost %d)", c.expr, got, c.tracked, p.sections[0].rows[0].value.cost)
		}
	}
	m := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"m","expr":"lpp.decode(frame)"}],"sections":[{"name":"s","outputs":[{"key":"v","expr":"m.temperature_1"}]}]}`, nil)
	if !m.lets[0].expr.tracked {
		t.Error("lpp.decode must run under the tracker")
	}
	// Every size-dependent native is declared so; the rest are not.
	want := map[string]bool{"slice": true, "hex.decode": true, "base64.decode": true, "lpp.decode": true}
	for _, f := range ownedFuncs {
		if strings.HasPrefix(f.name, "test.") {
			continue
		}
		if f.sizeDependent != want[f.name] {
			t.Errorf("%s: sizeDependent = %v, want %v", f.name, f.sizeDependent, want[f.name])
		}
	}
}
