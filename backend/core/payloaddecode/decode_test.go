// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/cel-go/common/types"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

func TestBasicFrameDecode(t *testing.T) {
	p := mustCompile(t, frameDoc("temperature", "double(i16be(frame, 1)) / 10.0", "door", "bit(frame, 3, 0)"), nil)
	r := decodeOK(t, p, frameOf("0100d701"))
	got := readingsMap(r)
	if got["temperature"] != "21.5" || got["door"] != "1" {
		t.Fatalf("got %v", got)
	}
	if r.Cost == 0 || r.Cost > p.EstimatedCost() {
		t.Fatalf("cost %d vs estimate %d", r.Cost, p.EstimatedCost())
	}
}

func TestReaders(t *testing.T) {
	cases := []struct {
		expr string
		hex  string
		want string
	}{
		{"u8(frame, 0)", "ff", "255"},
		{"i8(frame, 0)", "ff", "-1"},
		{"u16be(frame, 0)", "0102", "258"},
		{"u16le(frame, 0)", "0102", "513"},
		{"i16be(frame, 0)", "ff38", "-200"},
		{"i16le(frame, 0)", "38ff", "-200"},
		{"u24be(frame, 0)", "010203", "66051"},
		{"u24le(frame, 0)", "010203", "197121"},
		{"i24be(frame, 0)", "ffffff", "-1"},
		{"i24le(frame, 0)", "ffffff", "-1"},
		{"u32be(frame, 0)", "ffffffff", "4294967295"},
		{"i32be(frame, 0)", "ffffffff", "-1"},
		{"i32le(frame, 0)", "00000080", "-2147483648"},
		{"u32le(frame, 0)", "01000000", "1"},
		{"f32be(frame, 0)", "41ac0000", "21.5"},
		{"f32le(frame, 0)", "0000ac41", "21.5"},
		{"f64be(frame, 0)", "4035800000000000", "21.5"},
		{"f64le(frame, 0)", "0000000000803540", "21.5"},
		{"bit(frame, 0, 7)", "80", "1"},
		{"bit(frame, 0, 0)", "80", "0"},
		{"bits(frame, 0, 4, 4)", "a5", "10"},
		{"bits(frame, 0, 4, 8)", "f00f", "255"},
		{"sbits(frame, 0, 4, 4)", "a5", "-6"},
		{"bcd(frame, 0, 2)", "1234", "1234"},
		{"size(slice(frame, 1, 2))", "01020304", "2"},
		{"u8(hex.decode(\"0a0b\"), 1)", "00", "11"},
		{"u8(base64.decode(\"AQID\"), 2)", "00", "3"},
		{"int(math.max(1, u8(frame, 0)))", "07", "7"},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			p := mustCompile(t, frameDoc("v", c.expr), nil)
			got := readingsMap(decodeOK(t, p, frameOf(c.hex)))["v"]
			if got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestReadersOutOfRange(t *testing.T) {
	for _, e := range []string{"u8(frame, 4)", "u16be(frame, 3)", "i32le(frame, 1)", "u8(frame, -1)", "f64be(frame, 0)", "bits(frame, 3, 4, 8)", "bcd(frame, 2, 4)", "size(slice(frame, 1, 9))"} {
		p := mustCompile(t, frameDoc("v", "double("+e+")"), nil)
		_, err := p.Decode(frameOf("01020304"))
		if err == nil || err.Reason != ReasonEvalError {
			t.Fatalf("%s: want eval_error, got %v", e, err)
		}
		if !strings.Contains(err.Message, strings.SplitN(e, "(", 2)[0]) && !strings.HasPrefix(e, "size") {
			t.Fatalf("%s: error does not name the function: %q", e, err.Message)
		}
	}
	// The offset travels.
	p := mustCompile(t, frameDoc("v", "double(u16be(frame, 3))"), nil)
	_, err := p.Decode(frameOf("01020304"))
	if err == nil || err.Offset != 3 || err.Path != "sections[0].outputs[0].expr" {
		t.Fatalf("offset/path not reported: %+v", err)
	}
}

func TestNonFiniteFloatIsAnError(t *testing.T) {
	p := mustCompile(t, frameDoc("v", "f32be(frame, 0)"), nil)
	if _, err := p.Decode(frameOf("7fc00000")); err == nil || err.Reason != ReasonEvalError {
		t.Fatalf("NaN must fail the decode, got %v", err)
	}
	if _, err := p.Decode(frameOf("7f800000")); err == nil {
		t.Fatal("Inf must fail the decode")
	}
}

func TestLPP(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[
	  {"map":"lpp.decode(frame)","prefix":"lpp.","maxKeys":16}]}]}`
	p := mustCompile(t, doc, nil)
	// ch1 temperature 27.2, ch2 humidity 50.5, ch3 accelerometer 1 / -1 / 0.5, ch4 gps.
	r := decodeOK(t, p, frameOf("01670110"+"026865"+"037103e8fc1801f4"+"048801e240"+"0f4240"+"0003e8"))
	got := readingsMap(r)
	want := map[string]string{
		"lpp.temperature_1": "27.2", "lpp.humidity_2": "50.5",
		"lpp.accelerometer_x_3": "1", "lpp.accelerometer_y_3": "-1", "lpp.accelerometer_z_3": "0.5",
		"lpp.gps_lat_4": "12.3456", "lpp.gps_lon_4": "100", "lpp.gps_alt_4": "10",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	for _, bad := range []string{"0167", "01670110" + "0299", "01ff01", "01670110" + "01670110"} {
		if _, err := p.Decode(frameOf(bad)); err == nil {
			t.Fatalf("%s must fail", bad)
		}
	}
}

func TestJSONDecoder(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"JSON",
	  "let":[{"name":"t","expr":"has(json.sensor) ? double(json.sensor.temp) : 0.0"}],
	  "sections":[
	   {"name":"ignore","when":"json.type == \"heartbeat\"","ignore":true},
	   {"name":"t","when":"json.type in [\"uplink\", \"report\"]","outputs":[
	    {"key":"temperature","expr":"t * 1.8 + 32.0"},
	    {"key":"battery","expr":"double(json.bat)","when":"has(json.bat)"}]}]}`
	p := mustCompile(t, doc, nil)
	r := decodeOK(t, p, Input{JSON: []byte(`{"type":"uplink","sensor":{"temp":20},"bat":3.3}`)})
	if got := readingsMap(r); got["temperature"] != "68" || got["battery"] != "3.3" {
		t.Fatalf("%v", got)
	}
	r = decodeOK(t, p, Input{JSON: []byte(`{"type":"report","sensor":{"temp":20}}`)})
	if got := readingsMap(r); got["temperature"] != "68" || len(got) != 1 {
		t.Fatalf("false row guard must skip silently: %v", got)
	}
	r = decodeOK(t, p, Input{JSON: []byte(`{"type":"heartbeat"}`)})
	if !r.Ignored || len(r.Readings) != 0 {
		t.Fatalf("ignore section: %+v", r)
	}
	if _, err := p.Decode(Input{JSON: []byte(`{"sensor":{"temp":1}}`)}); err == nil || err.Reason != ReasonEvalError {
		t.Fatalf("a missing key read without has() fails the decode, got %v", err)
	}
	if _, err := p.Decode(Input{JSON: []byte(`{"type":"other"}`)}); err == nil || err.Reason != ReasonNoSection {
		t.Fatalf("want no_section, got %v", err)
	}
}

func TestDecodedInputAndLink(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"DECODED","sections":[{"name":"s","outputs":[
	  {"key":"temp","expr":"double(decoded.t)"},{"key":"rs","expr":"link.rssi","when":"\"rssi\" in link"}]}]}`
	_ = doc
	doc = strings.Replace(doc, `,"when":"\"rssi\" in link"`, `,"when":"has(link.rssi)"`, 1)
	p := mustCompile(t, doc, nil)
	r := decodeOK(t, p, Input{Decoded: []byte(`{"t":5}`), Link: map[string]float64{"rssi": -90}})
	if got := readingsMap(r); got["temp"] != "5" || got["rs"] != "-90" {
		t.Fatalf("%v", got)
	}
}

func TestMissingInput(t *testing.T) {
	p := mustCompile(t, frameDoc("v", "double(u8(frame, 0))"), nil)
	if _, err := p.Decode(Input{JSON: []byte(`{}`)}); err == nil || err.Reason != ReasonMissingInput {
		t.Fatalf("want missing_input, got %v", err)
	}
	if _, err := p.Decode(Input{Frame: []byte{}}); err == nil || err.Reason != ReasonEvalError {
		t.Fatalf("an empty frame is present: want eval_error, got %v", err)
	}
}

func TestNoOutputsAndNoSection(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[
	  {"name":"a","when":"fPort == 2","outputs":[{"key":"v","expr":"u8(frame, 0)","when":"false"}]}]}`
	p := mustCompile(t, doc, nil)
	in := frameOf("01")
	in.LoRa = &LoRa{FPort: 2, FCnt: 1}
	if _, err := p.Decode(in); err == nil || err.Reason != ReasonNoOutputs {
		t.Fatalf("want no_outputs, got %v", err)
	}
	in.LoRa.FPort = 3
	if _, err := p.Decode(in); err == nil || err.Reason != ReasonNoSection {
		t.Fatalf("want no_section, got %v", err)
	}
	// Not LoRaWAN: fPort is -1.
	if _, err := p.Decode(frameOf("01")); err == nil || err.Reason != ReasonNoSection {
		t.Fatalf("want no_section, got %v", err)
	}
}

func TestTypeAwareFormatting(t *testing.T) {
	m := tm("i", calcfield.Integer, "d", calcfield.Double, "b", calcfield.Boolean)
	p := mustCompile(t, frameDoc("i", "u8(frame, 0)", "d", "u8(frame, 0)", "b", "bit(frame, 1, 0)"), m)
	got := readingsMap(decodeOK(t, p, frameOf("0701")))
	if got["i"] != "7" || got["d"] != "7" || got["b"] != "1" {
		t.Fatalf("%v", got)
	}
	// 1e13 into an INT metric renders as an integer, never 1e+13.
	p = mustCompile(t, frameDoc("i", "double(u32be(frame, 0)) * 10000.0 * 10.0"), m)
	got = readingsMap(decodeOK(t, p, frameOf("3b9aca00")))
	if got["i"] != "100000000000000" {
		t.Fatalf("INT must not use exponent form: %v", got)
	}
	// A non-integral double into an INT metric is invalid_output.
	p = mustCompile(t, frameDoc("i", "double(u8(frame, 0)) / 2.0"), m)
	if _, err := p.Decode(frameOf("03")); err == nil || err.Reason != ReasonInvalidOutput {
		t.Fatalf("want invalid_output, got %v", err)
	}
	// A BOOLEAN metric takes 0/1 only.
	p = mustCompile(t, frameDoc("b", "u8(frame, 0)"), m)
	if _, err := p.Decode(frameOf("02")); err == nil || err.Reason != ReasonInvalidOutput {
		t.Fatalf("want invalid_output, got %v", err)
	}
	// A DOUBLE metric uses the canonical shortest form.
	p = mustCompile(t, frameDoc("d", "double(u8(frame, 0)) / 3.0"), m)
	if got := readingsMap(decodeOK(t, p, frameOf("01")))["d"]; got != "0.3333333333333333" {
		t.Fatalf("got %s", got)
	}
}

func TestOutputValidationHook(t *testing.T) {
	m := tm("temp", calcfield.Double)
	m.extra = func(k, v string) error {
		if v == "99" {
			return errors.New("above the metric maximum")
		}
		return nil
	}
	p := mustCompile(t, frameDoc("temp", "u8(frame, 0)"), m)
	if r := decodeOK(t, p, frameOf("01")); readingsMap(r)["temp"] != "1" {
		t.Fatal("valid output must pass the hook")
	}
	_, err := p.Decode(frameOf("63"))
	if err == nil || err.Reason != ReasonInvalidOutput || !strings.Contains(err.Message, "above the metric maximum") {
		t.Fatalf("want invalid_output carrying the hook's error, got %v", err)
	}
}

func TestStrictKeysAndConflicts(t *testing.T) {
	m := tm("temp", calcfield.Double, "lpp.temperature_1", calcfield.Double, "lpp.humidity_2", calcfield.Double)
	ce := compileErr(t, frameDoc("undeclared", "u8(frame, 0)"), m)
	if !strings.Contains(ce.Error(), "not a declared measured metric") {
		t.Fatalf("%v", ce)
	}
	compileErr(t, frameDoc("lorawan.rssi", "u8(frame, 0)"), nil)
	compileErr(t, frameDoc("temp", "u8(frame, 0)", "temp", "u8(frame, 1)"), nil)

	// A map key that is not declared is skipped and counted, not fatal; a scalar wins a collision.
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[
	  {"key":"lpp.temperature_1","expr":"1.0"},
	  {"map":"lpp.decode(frame)","prefix":"lpp.","maxKeys":8}]}]}`
	p := mustCompile(t, doc, m)
	r := decodeOK(t, p, frameOf("01670110"+"026865"+"037103e8fc1801f4"))
	got := readingsMap(r)
	if got["lpp.temperature_1"] != "1" || got["lpp.humidity_2"] != "50.5" || len(got) != 2 {
		t.Fatalf("scalar must win the collision, undeclared keys must be skipped: %v", got)
	}
	skips := map[string]string{}
	for _, s := range r.Skipped {
		skips[s.Key] = s.Reason
	}
	if skips["lpp.temperature_1"] != "collision" || skips["lpp.accelerometer_x_3"] != "undeclared" {
		t.Fatalf("skips not counted: %v", r.Skipped)
	}
}

// The scalar wins whatever the row order: a map row BEFORE the scalar that writes the same key.
func TestScalarWinsRegardlessOfRowOrder(t *testing.T) {
	m := tm("lpp.temperature_1", calcfield.Double)
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[
	  {"map":"lpp.decode(frame)","prefix":"lpp.","maxKeys":8},
	  {"key":"lpp.temperature_1","expr":"1.0"}]}]}`
	r := decodeOK(t, mustCompile(t, doc, m), frameOf("01670110"))
	if got := readingsMap(r); got["lpp.temperature_1"] != "1" || len(r.Readings) != 1 {
		t.Fatalf("the scalar must win: %v", r.Readings)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "collision" {
		t.Fatalf("the skipped map key must be counted: %v", r.Skipped)
	}
}

func TestMapOverflowFailsNeverTruncates(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[
	  {"map":"lpp.decode(frame)","maxKeys":2}]}]}`
	p := mustCompile(t, doc, nil)
	if _, err := p.Decode(frameOf("01670110" + "026865" + "03670101")); err == nil || err.Reason != ReasonMapOverflow {
		t.Fatalf("want map_overflow, got %v", err)
	}
	if r := decodeOK(t, p, frameOf("01670110"+"0268"+"65")); len(r.Readings) != 2 {
		t.Fatalf("%v", r.Readings)
	}
}

func TestKindAndEnvVersion(t *testing.T) {
	d := mustDoc(t, frameDoc("v", "u8(frame, 0)"))
	d.Kind = KindWASM
	if _, err := Compile(d, nil); !errors.Is(err, ErrUnsupportedDecoderKind) {
		t.Fatalf("WASM must be refused loudly, got %v", err)
	}
	d.Kind, d.EnvVersion = KindCEL, 2
	if _, err := Compile(d, nil); !errors.Is(err, ErrEnvUnsupported) {
		t.Fatalf("env 2 must be refused as unsupported, got %v", err)
	}
}

func TestParseDocumentFailsClosed(t *testing.T) {
	for _, js := range []string{
		`{"kind":"CEL","bogus":1}`, `{"kind":"CEL"} {}`, `not json`, strings.Repeat(" ", MaxDocumentBytes+1),
	} {
		if _, err := ParseDocument([]byte(js)); err == nil {
			t.Fatalf("%.40q must be refused", js)
		}
	}
}

func TestVectors(t *testing.T) {
	doc := `{"kind":"CEL","envVersion":1,"input":"FRAME",
	 "sections":[{"name":"s","outputs":[{"key":"t","expr":"double(i16be(frame, 0)) / 10.0"}]}],
	 "vectors":[
	  {"name":"ok","input":{"frameHex":"00d7"},"expect":{"t":21.5}},
	  {"name":"wrong","input":{"frameHex":"00d7"},"expect":{"t":21.6}},
	  {"name":"extra key expected","input":{"frameHex":"00d7"},"expect":{"t":21.5,"u":1}},
	  {"name":"short frame","input":{"frameHex":"00"},"expect":{"t":1}},
	  {"name":"unexpected key","input":{"frameHex":"00d7"},"expect":{}}]}`
	p := mustCompile(t, doc, nil)
	res := p.RunVectors()
	want := []bool{true, false, false, false, false}
	for i, r := range res {
		if r.OK != want[i] {
			t.Fatalf("vector %q: ok=%v err=%q", r.Name, r.OK, r.Error)
		}
	}
	if !strings.Contains(res[1].Error, "expected 21.6, got 21.5") {
		t.Fatalf("%q", res[1].Error)
	}
}

func TestInputCapsBeforeWork(t *testing.T) {
	p := mustCompile(t, frameDoc("v", "double(u8(frame, 0))"), nil)
	if _, err := p.Decode(Input{Frame: make([]byte, MaxFrameBytes+1)}); err == nil || err.Reason != ReasonInputTooLarge {
		t.Fatalf("4097 bytes must be refused: %v", err)
	}
	if _, err := p.Decode(Input{Frame: make([]byte, MaxFrameBytes)}); err != nil {
		t.Fatalf("4096 bytes is allowed: %v", err)
	}
	jp := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":"double(json.a)"}]}]}`, nil)
	big := []byte(`{"a":1,"pad":"` + strings.Repeat("x", MaxJSONBytes) + `"}`)
	if _, err := jp.Decode(Input{JSON: big}); err == nil || err.Reason != ReasonInputTooLarge {
		t.Fatalf("oversized JSON must be refused: %v", err)
	}
	deep := []byte(strings.Repeat(`{"a":`, MaxJSONDepth+1) + "1" + strings.Repeat("}", MaxJSONDepth+1))
	if _, err := jp.Decode(Input{JSON: deep}); err == nil || err.Reason != ReasonInputMalformed {
		t.Fatalf("deep JSON must be refused: %v", err)
	}
	// Brackets inside strings are not nesting.
	ok := []byte(`{"a":1,"s":"` + strings.Repeat("[", 100) + `"}`)
	if _, err := jp.Decode(Input{JSON: ok}); err != nil {
		t.Fatalf("brackets in a string are not depth: %v", err)
	}
	if _, err := jp.Decode(Input{JSON: []byte(`[1]`)}); err == nil || err.Reason != ReasonInputMalformed {
		t.Fatalf("a non-object must be refused: %v", err)
	}
}

func TestFunctionsListed(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Functions() {
		if f.Name == "" || f.Signature == "" || f.Doc == "" || seen[f.Name] {
			t.Fatalf("bad or duplicate function doc %+v", f)
		}
		seen[f.Name] = true
	}
	for _, n := range []string{"u8", "i32le", "f64be", "bit", "bits", "sbits", "bcd", "slice", "hex.decode", "base64.decode", "lpp.decode", "math.clamp"} {
		if !seen[n] {
			t.Fatalf("%s missing from the function list", n)
		}
	}
}

// Review findings (second reviewer), each pinned.

func TestBitsOffsetsCannotOverflowOrScan(t *testing.T) {
	ff := make([]byte, MaxFrameBytes)
	for i := range ff {
		ff[i] = 0xff
	}
	in := Input{Frame: ff}
	get := func(expr string) (string, *DecodeError) {
		p := mustCompile(t, frameDoc("v", expr), nil)
		r, err := p.Decode(in)
		if err != nil {
			return "", err
		}
		return readingsMap(r)["v"], nil
	}
	if v, err := get("bits(frame, 0, 32760, 1)"); err != nil || v != "1" {
		t.Fatalf("the last bit of a 4096-byte frame: %q %v", v, err)
	}
	if v, err := get("bits(frame, 0, 4093, 3)"); err != nil || v != "7" { // bit offset 4093 = byte 511
		t.Fatalf("%q %v", v, err)
	}
	for _, e := range []string{
		"bits(frame, 0, 32761, 8)", "bits(frame, 0, 32768, 1)", "bits(frame, 4095, 8, 1)",
		"bits(frame, 0, 9223372036854775807, 1)", "bits(frame, 9223372036854775807, 9223372036854775807, 32)",
		"sbits(frame, 4094, 9, 32)",
	} {
		if _, err := get(e); err == nil || err.Reason != ReasonEvalError || err.Message == internalErrorMessage {
			t.Errorf("%s must be an out-of-range error (not a recovered panic), got %v", e, err)
		}
	}
	// The cost of a bit read does not depend on how far it reaches.
	p := mustCompile(t, frameDoc("v", "bits(frame, 0, 32760, 1)"), nil)
	if r := decodeOK(t, p, in); r.Cost > p.EstimatedCost() || r.Cost > 10 {
		t.Fatalf("cost %d", r.Cost)
	}
}

func TestUnicodeTextLiteralIsChargedInBytes(t *testing.T) {
	for _, fn := range []string{"hex.decode", "base64.decode"} {
		p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"b","expr":"`+fn+`(\"éé\")"}],"sections":[{"name":"s","outputs":[{"key":"v","expr":"1"}]}]}`, nil)
		_, err := p.Decode(frameOf("00"))
		if err == nil || err.Reason != ReasonEvalError {
			t.Fatalf("%s: want the native's own error, not a cost overrun: %v", fn, err)
		}
	}
}

// CEL absorbs an error that a determined && or || does not need, whichever side it is on.
// The decoder keeps that rule (guarded reads such as `has(json.x) && double(json.x) > 1.0`
// depend on it), so the absorbed read is not a failure. Pinned so a change is deliberate.
func TestAbsorbedErrorsAreNotFailures(t *testing.T) {
	p := mustCompile(t, frameDoc("v", "(u8(frame, 999) == 0 || true) ? 1 : 0"), nil)
	if got := readingsMap(decodeOK(t, p, frameOf("01")))["v"]; got != "1" {
		t.Fatalf("got %s", got)
	}
	// Unabsorbed, the same read fails the decode.
	p = mustCompile(t, frameDoc("v", "(u8(frame, 999) == 0) ? 1 : 0"), nil)
	if _, err := p.Decode(frameOf("01")); err == nil || err.Reason != ReasonEvalError {
		t.Fatalf("got %v", err)
	}
}

func TestLosingMapEntryIsNeverValidated(t *testing.T) {
	m := tm("x", calcfield.Double)
	doc := `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[
	  {"map":"json","maxKeys":1},{"key":"x","expr":"1.0"}]}]}`
	r := decodeOK(t, mustCompile(t, doc, m), Input{JSON: []byte(`{"x":"bad"}`)})
	if got := readingsMap(r); got["x"] != "1" || len(r.Readings) != 1 {
		t.Fatalf("%v", r.Readings)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "collision" {
		t.Fatalf("%v", r.Skipped)
	}
	// A surviving non-numeric entry is still an error.
	if _, err := mustCompile(t, strings.Replace(doc, `,{"key":"x","expr":"1.0"}`, "", 1), m).Decode(Input{JSON: []byte(`{"x":"bad"}`)}); err == nil || err.Reason != ReasonInvalidOutput {
		t.Fatalf("got %v", err)
	}
}

func TestJSONSubObjectMapOutput(t *testing.T) {
	m := tm("a", calcfield.Double, "b", calcfield.Double)
	doc := `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[
	  {"map":"json.values","prefix":"","maxKeys":4}]}]}`
	p := mustCompile(t, doc, m)
	r := decodeOK(t, p, Input{JSON: []byte(`{"values":{"a":1,"b":true,"other":"text"}}`)})
	if got := readingsMap(r); got["a"] != "1" || got["b"] != "1" || len(got) != 2 {
		t.Fatalf("%v", r.Readings)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Key != "other" || r.Skipped[0].Reason != "undeclared" {
		t.Fatalf("%v", r.Skipped)
	}
	if _, err := p.Decode(Input{JSON: []byte(`{"values":5}`)}); err == nil || err.Reason != ReasonEvalError {
		t.Fatalf("a non-map must fail the decode: %v", err)
	}
	if _, err := p.Decode(Input{JSON: []byte(`{"values":{"a":1,"b":2,"c":3,"d":4,"e":5}}`)}); err == nil || err.Reason != ReasonMapOverflow {
		t.Fatalf("overflow: %v", err)
	}
}

// F1: a long error message is cut on a rune boundary, so it is always valid UTF-8 (a record
// with a split rune is refused by the database).
func TestErrorMessageTruncationKeepsUTF8(t *testing.T) {
	key := strings.Repeat("é", 150)
	p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":"double(json[\"`+key+`\"])"}]}]}`, nil)
	_, err := p.Decode(Input{JSON: []byte(`{}`)})
	if err == nil || err.Reason != ReasonEvalError {
		t.Fatalf("want eval_error, got %v", err)
	}
	if !utf8.ValidString(err.Message) || len(err.Message) > 200 || len(err.Message) < 190 {
		t.Fatalf("message must be valid UTF-8 of about 200 bytes, got %d bytes valid=%v", len(err.Message), utf8.ValidString(err.Message))
	}
	if !utf8.ValidString(truncateUTF8("a\xffb\xc3", 10)) || truncateUTF8("ééé", 5) != "éé" {
		t.Fatal("truncateUTF8")
	}
}

// F7: a JSON key spelled like the cost-limit message is not a cost overrun.
func TestCostLimitSpoofIsNotCostExceeded(t *testing.T) {
	p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":"double(json[\"actual cost limit exceeded\"])"}]}]}`, nil)
	_, err := p.Decode(Input{JSON: []byte(`{}`)})
	if err == nil || err.Reason != ReasonEvalError {
		t.Fatalf("want eval_error, got %v", err)
	}
}

// F3: a map over maxKeys is refused from its size, before any key is copied.
func TestMapOverflowCheckedBeforeIterating(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{`)
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&sb, `"k%d":0,`, i)
	}
	sb.WriteString(`"z":0}`)
	in := Input{JSON: []byte(sb.String())}
	p := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"map":"json","maxKeys":1}]}]}`, nil)
	if _, err := p.Decode(in); err == nil || err.Reason != ReasonMapOverflow {
		t.Fatalf("want map_overflow, got %v", err)
	}
	// Parsing the body allocates; the key copies of an iteration would add thousands.
	base := mustCompile(t, `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":"double(json.z)"}]}]}`, nil)
	bytesPer := func(pl *Plan) uint64 {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		for i := 0; i < 20; i++ {
			_, _ = pl.Decode(in)
		}
		runtime.ReadMemStats(&m1)
		return (m1.TotalAlloc - m0.TotalAlloc) / 20
	}
	b1, b0 := bytesPer(p), bytesPer(base)
	if b1 > b0+20_000 {
		t.Fatalf("the overflow path allocates %d bytes against a %d baseline: it copied the keys", b1, b0)
	}
}

// F6: an unknown metric type is an error, not a silent DOUBLE.
func TestFormatValueUnknownTypeFails(t *testing.T) {
	if _, err := formatValue("k", types.Double(1.5), calcfield.Type(99), true); err == nil {
		t.Fatal("an unknown metric type must be refused")
	}
	if got, err := formatValue("k", types.Double(1.5), calcfield.Double, true); err != nil || got != "1.5" {
		t.Fatalf("%q %v", got, err)
	}
}
