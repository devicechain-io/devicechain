// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/interpreter"
)

// exprGen builds random, well-typed expressions over the whole admitted language. It is a
// generator, not a mutator: every expression it makes is meant to COMPILE, so the property
// tests spend their time evaluating rather than being refused by the parser.
type exprGen struct {
	r *rand.Rand
	// safe keeps offsets and lengths small, so most evaluations succeed and the deep paths
	// (nested natives, long chains) are reached; unsafe lets them run wild to hit the errors.
	safe bool
	lets []string // bytes-typed lets in scope
	maps []string // map-typed lets in scope
}

func (g *exprGen) pick(xs ...string) string { return xs[g.r.Intn(len(xs))] }

func (g *exprGen) off() string {
	if g.safe {
		return fmt.Sprint(g.r.Intn(4))
	}
	switch g.r.Intn(6) {
	case 0:
		return "0"
	case 1:
		return fmt.Sprint(g.r.Intn(8))
	case 2:
		return fmt.Sprint(g.r.Intn(300))
	case 3:
		return "size(" + g.bytes(0) + ") - " + fmt.Sprint(g.r.Intn(6))
	case 4:
		return "u8(" + g.bytes(0) + ", 0)"
	}
	return fmt.Sprint(g.r.Intn(5000) - 10)
}

func (g *exprGen) bytes(d int) string {
	if d > 2 {
		return "frame"
	}
	switch g.r.Intn(9) {
	case 0, 1, 2:
		return "frame"
	case 3:
		if len(g.lets) > 0 {
			return g.pick(g.lets...)
		}
		return "frame"
	case 4, 5:
		if g.safe {
			return fmt.Sprintf("slice(%s, %s, %s)", g.bytes(d+1), g.off(), g.pick("1", "2", "3", "4"))
		}
		return fmt.Sprintf("slice(%s, %s, %s)", g.bytes(d+1), g.off(), g.pick("1", "2", "4", "8", "100", "4096", g.off()))
	case 6:
		return fmt.Sprintf("hex.decode(%s)", g.str())
	case 7:
		return fmt.Sprintf("base64.decode(%s)", g.pick(`"AQID"`, `"AAAA"`, `json.p`, `json.b64`, `""`, `"AQ=="`))
	}
	return fmt.Sprintf("(%s ? %s : %s)", g.boolean(d+1), g.bytes(d+1), g.bytes(d+1))
}

func (g *exprGen) str() string {
	if g.safe {
		return g.pick(`"0a0b0c"`, `json.p`, `"00ff"`)
	}
	return g.pick(`"0a0b0c"`, `""`, `"zz"`, `json.p`, `json.type`, `"00ff"`, `"0"`)
}

func (g *exprGen) integer(d int) string {
	if d > 3 {
		return fmt.Sprint(g.r.Intn(20))
	}
	switch g.r.Intn(16) {
	case 0:
		return fmt.Sprint(g.r.Intn(1000) - 100)
	case 1:
		return g.pick("fPort", "fCnt")
	case 2, 3:
		fn := g.pick("u8", "i8", "u16be", "u16le", "i16be", "i16le", "u24be", "u24le", "i24be", "i24le", "u32be", "u32le", "i32be", "i32le")
		return fmt.Sprintf("%s(%s, %s)", fn, g.bytes(d+1), g.off())
	case 4:
		return fmt.Sprintf("bit(%s, %s, %d)", g.bytes(d+1), g.off(), g.r.Intn(9))
	case 5:
		if g.safe {
			return fmt.Sprintf("%s(%s, %s, %d, %d)", g.pick("bits", "sbits"), g.bytes(d+1), g.off(), g.r.Intn(6), 1+g.r.Intn(12))
		}
		return fmt.Sprintf("%s(%s, %s, %d, %d)", g.pick("bits", "sbits"), g.bytes(d+1), g.off(), g.r.Intn(70), g.r.Intn(34))
	case 6:
		if g.r.Intn(4) == 0 {
			return fmt.Sprintf("bcd(%s, %s, 1 + u8(%s, 0) %% 8)", g.bytes(d+1), g.off(), g.bytes(d+1))
		}
		if g.safe {
			return fmt.Sprintf("bcd(%s, %s, %d)", g.bytes(d+1), g.off(), 1+g.r.Intn(3))
		}
		return fmt.Sprintf("bcd(%s, %s, %d)", g.bytes(d+1), g.off(), g.r.Intn(10))
	case 7:
		return fmt.Sprintf("size(%s)", g.bytes(d+1))
	case 8:
		return fmt.Sprintf("(%s %s %s)", g.integer(d+1), g.pick("+", "-", "*", "/", "%"), g.integer(d+1))
	case 9:
		return fmt.Sprintf("int(%s)", g.double(d+1))
	case 10:
		return fmt.Sprintf("math.%s(%s, %s)", g.pick("min", "max"), g.integer(d+1), g.integer(d+1))
	case 11:
		return fmt.Sprintf("(%s ? %s : %s)", g.boolean(d+1), g.integer(d+1), g.integer(d+1))
	case 12:
		if g.safe {
			return fmt.Sprintf("int(json.%s)", g.pick("k", "a.b", "n"))
		}
		return fmt.Sprintf("int(json.%s)", g.pick("k", "a.b", "n", "missing"))
	case 13:
		return fmt.Sprintf("math.abs(%s)", g.integer(d+1))
	case 14:
		return fmt.Sprintf("-%s", g.integer(d+1))
	}
	return fmt.Sprintf("int(uint(%s))", g.integer(d+1))
}

func (g *exprGen) double(d int) string {
	if d > 3 {
		return fmt.Sprintf("%d.5", g.r.Intn(20))
	}
	switch g.r.Intn(14) {
	case 0:
		return fmt.Sprintf("%d.25", g.r.Intn(100))
	case 1, 2:
		return fmt.Sprintf("double(%s)", g.integer(d+1))
	case 3:
		return fmt.Sprintf("%s(%s, %s)", g.pick("f32be", "f32le", "f64be", "f64le"), g.bytes(d+1), g.off())
	case 4, 5:
		return fmt.Sprintf("(%s %s %s)", g.double(d+1), g.pick("+", "-", "*", "/"), g.double(d+1))
	case 6:
		return fmt.Sprintf("math.%s(%s)", g.pick("sqrt", "floor", "ceil", "round", "abs"), g.double(d+1))
	case 7:
		return fmt.Sprintf("math.pow(%s, %d)", g.double(d+1), g.r.Intn(9)-3)
	case 8:
		return fmt.Sprintf("math.clamp(%s, %s, %s)", g.double(d+1), g.double(d+1), g.double(d+1))
	case 9:
		if g.safe {
			return fmt.Sprintf("double(json.%s)", g.pick("k", "a.b", "n", "f"))
		}
		return fmt.Sprintf("double(json.%s)", g.pick("k", "a.b", "n", "missing", "f"))
	case 10:
		return fmt.Sprintf("(has(decoded.t) ? double(decoded.t) : 0.0)")
	case 11:
		return fmt.Sprintf("(%s ? %s : %s)", g.boolean(d+1), g.double(d+1), g.double(d+1))
	case 12:
		// A dynamic comparison: refused by the allowlist, so most of these do not compile.
		return fmt.Sprintf("(json.%s < json.%s ? 1.0 : 2.0)", g.pick("p", "type", "k"), g.pick("p", "type", "a.b"))
	}
	if len(g.maps) > 0 {
		return fmt.Sprintf("%s.%s", g.pick(g.maps...), g.pick("temperature_1", "humidity_2", "accelerometer_x_3", "gps_lat_4"))
	}
	return "(has(link.rssi) ? link.rssi : 0.0)"
}

func (g *exprGen) boolean(d int) string {
	if d > 3 {
		return g.pick("true", "false")
	}
	switch g.r.Intn(12) {
	case 0:
		return g.pick("true", "false")
	case 1, 2:
		return fmt.Sprintf("(%s %s %s)", g.integer(d+1), g.pick("<", "<=", ">", ">=", "==", "!="), g.integer(d+1))
	case 3:
		return fmt.Sprintf("(%s %s %s)", g.double(d+1), g.pick("<", "<=", ">", ">=", "==", "!="), g.double(d+1))
	case 4:
		return fmt.Sprintf("(%s && %s)", g.boolean(d+1), g.boolean(d+1))
	case 5:
		return fmt.Sprintf("(%s || %s)", g.boolean(d+1), g.boolean(d+1))
	case 6:
		return fmt.Sprintf("!%s", g.boolean(d+1))
	case 7:
		return fmt.Sprintf("has(json.%s)", g.pick("k", "a.b", "missing", "a"))
	case 8:
		return fmt.Sprintf("(json.type == %s)", g.pick(`"x"`, `"uplink"`, `""`))
	case 9:
		return fmt.Sprintf("(%s in [%s])", g.integer(d+1), g.pick("1, 2, 3", "0", "-1, 255, 256, 65535"))
	case 10:
		return fmt.Sprintf("(json.type in [%s])", g.pick(`"x", "y"`, `"uplink"`))
	}
	return fmt.Sprintf("bool(json.%s)", g.pick("flag", "missing"))
}

// row is one output expression of a random type.
func (g *exprGen) row() string {
	switch g.r.Intn(3) {
	case 0:
		return g.integer(0)
	case 1:
		return g.double(0)
	}
	return g.boolean(0)
}

// The inputs the property tests run every expression against: empty, tiny, ordinary, maximal
// and adversarial frames; JSON that has, lacks and mistypes the keys the generator reads.
func propertyInputs() []Input {
	rnd := rand.New(rand.NewSource(7))
	rb := func(n int) []byte {
		b := make([]byte, n)
		rnd.Read(b)
		return b
	}
	ff := make([]byte, MaxFrameBytes)
	for i := range ff {
		ff[i] = 0xff
	}
	js := []byte(`{"k":3,"a":{"b":2.5},"n":-7,"f":1.5,"flag":true,"type":"uplink","p":"0a0b0c0d","b64":"AQIDBA=="}`)
	jbad := []byte(`{"k":"three","a":[1],"n":null,"flag":1,"type":5,"p":123,"b64":"!!"}`)
	lpp := frameOf("01670110" + "026865" + "037103e8fc1801f4" + "048801e240" + "0f4240" + "0003e8")
	lpp.LoRa = &LoRa{FPort: 2, FCnt: 9}
	lpp.JSON, lpp.Decoded, lpp.Link = js, []byte(`{"t":1,"u":2}`), map[string]float64{"rssi": -90, "snr": 5}
	return []Input{
		{Frame: []byte{}, JSON: js, Decoded: []byte(`{"t":1}`)},
		{Frame: []byte{1}, JSON: jbad, Decoded: []byte(`{}`)},
		{Frame: rb(8), JSON: js, Decoded: []byte(`{"t":1,"u":2}`), LoRa: &LoRa{FPort: 1, FCnt: 1}, Link: map[string]float64{"rssi": -80}},
		{Frame: rb(242), JSON: js, Decoded: []byte(`{"t":0.5,"u":-1}`)},
		{Frame: rb(MaxFrameBytes), JSON: js, Decoded: []byte(`{"t":1,"u":2}`)},
		{Frame: ff, JSON: js, Decoded: []byte(`{"t":1,"u":2}`)},
		lpp,
	}
}

func fmtLets(g *exprGen, n int) (string, []string) {
	var parts []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("l%d", i)
		switch g.r.Intn(3) {
		case 0:
			parts = append(parts, fmt.Sprintf(`{"name":%q,"expr":%q}`, name, g.bytes(0)))
			g.lets = append(g.lets, name)
		case 1:
			parts = append(parts, fmt.Sprintf(`{"name":%q,"expr":%q}`, name, "lpp.decode("+g.bytes(0)+")"))
			g.maps = append(g.maps, name)
		default:
			parts = append(parts, fmt.Sprintf(`{"name":%q,"expr":%q}`, name, g.integer(0)))
		}
	}
	return strings.Join(parts, ","), g.lets
}

func randomDoc(g *exprGen) string {
	lets, _ := fmtLets(g, g.r.Intn(4))
	n := 1
	if g.r.Intn(3) == 0 {
		n = 1 + g.r.Intn(4)
	}
	var rows []string
	for i := 0; i < n; i++ {
		row := fmt.Sprintf(`{"key":"k%d","expr":%q`, i, g.row())
		if g.r.Intn(3) == 0 {
			row += fmt.Sprintf(`,"when":%q`, g.boolean(0))
		}
		rows = append(rows, row+"}")
	}
	if g.r.Intn(5) == 0 && len(g.maps) > 0 {
		rows = append(rows, fmt.Sprintf(`{"map":%q,"prefix":"m.","maxKeys":%d}`, g.pick(g.maps...), 1+g.r.Intn(30)))
	}
	guard := "true"
	if g.r.Intn(3) == 0 {
		guard = g.boolean(0)
	}
	secs := fmt.Sprintf(`{"name":"a","when":%q,"outputs":[%s]},{"name":"b","outputs":[{"key":"z","expr":"1"}]}`, guard, strings.Join(rows, ","))
	return fmt.Sprintf(`{"kind":"CEL","envVersion":1,"input":"FRAME","let":[%s],"sections":[%s]}`, lets, secs)
}

// checkInvariants is the property every decode must satisfy, whatever the document or input.
func checkInvariants(t testing.TB, label string, p *Plan, in Input, res *Result, derr *DecodeError) {
	t.Helper()
	bound := p.CostFor(in)
	if derr != nil {
		if derr.Message == internalErrorMessage {
			t.Fatalf("%s: a panic was recovered (the decoder is not total)", label)
		}
		if derr.Reason == ReasonCostExceeded {
			t.Fatalf("%s: the runtime cost exceeded the static estimate: %v", label, derr)
		}
		if derr.Cost > bound {
			t.Fatalf("%s: failed after spending %d, above the static estimate %d", label, derr.Cost, bound)
		}
		return
	}
	if res.Cost > bound {
		t.Fatalf("%s: spent %d, above the static estimate %d", label, res.Cost, bound)
	}
	if bound > p.EstimatedCost() {
		t.Fatalf("%s: CostFor %d above EstimatedCost %d", label, bound, p.EstimatedCost())
	}
	if len(res.Readings) > p.MaxReadings() {
		t.Fatalf("%s: %d readings above maxReadings %d", label, len(res.Readings), p.MaxReadings())
	}
}

// A6: over a generated corpus of well-typed expressions and adversarial inputs, the runtime
// cost never exceeds the static estimate, nothing panics, and nothing trips the runtime
// CostLimit (which would mean the estimate was an under-count).
func TestPropertyRuntimeCostNeverExceedsStatic(t *testing.T) {
	n := 2500
	if testing.Short() {
		n = 300
	}
	trackAll(t)
	inputs := propertyInputs()
	g := &exprGen{r: rand.New(rand.NewSource(20261105))}
	compiled, decoded, failed := 0, 0, 0
	reasons := map[Reason]int{}
	for i := 0; i < n; i++ {
		g.lets, g.maps, g.safe = nil, nil, i%2 == 0
		js := randomDoc(g)
		doc, err := ParseDocument([]byte(js))
		if err != nil {
			t.Fatalf("generator made an unparseable document: %v\n%s", err, js)
		}
		p, err := Compile(doc, nil)
		if err != nil {
			continue // a refusal is fine (cost ceiling, a type the generator got wrong); only compiled plans are judged
		}
		compiled++
		for k, in := range inputs {
			res, derr := p.Decode(in)
			checkInvariants(t, fmt.Sprintf("doc %d input %d\n%s", i, k, js), p, in, res, derr)
			if derr == nil {
				decoded++
			} else {
				failed++
				reasons[derr.Reason]++
			}
		}
	}
	// The generator must reach the interesting paths, or the property is vacuous.
	if compiled < n/3 || decoded < compiled {
		t.Fatalf("generator is too weak: %d of %d compiled, %d decoded, %d failed", compiled, n, decoded, failed)
	}
	t.Logf("%d documents compiled of %d; %d decodes succeeded, %d failed cleanly %v", compiled, n, decoded, failed, reasons)
}

// The size bound a let carries into later estimates must hold for the value it describes.
// trackAll runs the rest of the test with the runtime CostLimit on EVERY expression, so the
// property covers the ones production runs unwatched.
func trackAll(t testing.TB) {
	t.Helper()
	trackEveryExpression = true
	t.Cleanup(func() { trackEveryExpression = false })
}

func TestBytesBoundIsSound(t *testing.T) {
	env, err := baseEnv()
	if err != nil {
		t.Fatal(err)
	}
	g := &exprGen{r: rand.New(rand.NewSource(99))}
	inputs := propertyInputs()
	checked := 0
	for i := 0; i < 1500; i++ {
		src := g.bytes(0)
		a, iss := env.Compile(src)
		if iss != nil && iss.Err() != nil {
			continue
		}
		bound := bytesBound(a.NativeRep().Expr(), nil)
		prog, err := env.Program(a)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range inputs {
			act := &state{plan: &Plan{slots: map[string]int{VarFrame: 0, VarFPort: 1, VarFCnt: 2, VarJSON: 3, VarDecoded: 4, VarLink: 5}}, vals: []any{
				types.Bytes(in.Frame), types.Int(-1), types.Int(-1), emptyDynMap, emptyDynMap, emptyDoubleMap}}
			if len(in.JSON) > 0 {
				v, de := parseObject(in.JSON, "json")
				if de == nil {
					act.vals[3] = v
				}
			}
			out, _, err := prog.Eval(interpreter.Activation(act))
			if err != nil {
				continue
			}
			b, ok := out.(types.Bytes)
			if !ok {
				t.Fatalf("%s produced %T", src, out)
			}
			checked++
			if uint64(len(b)) > bound {
				t.Fatalf("%s produced %d bytes, above its bound %d", src, len(b), bound)
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d evaluations checked", checked)
	}
}
