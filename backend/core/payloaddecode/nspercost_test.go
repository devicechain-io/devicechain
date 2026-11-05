// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// maxNSPerCost is the fixed bound on measured nanoseconds per unit of declared cost. cel-go's
// own convention is about 50 ns a unit and an honest decoder measures 30-250 here, on a
// loaded laptop; the bound is set so a native that under-declares by an order of magnitude or
// more is an outlier, with room for a slow CI runner. It is not a tight calibration.
// Cheap natives are called many times in one expression so a decode's fixed overhead does not
// swamp the measurement.
const maxNSPerCost = 500

// nsPerOp is the best (minimum) mean time of one Decode over `samples` timed loops: the
// minimum is the least noisy estimate of the cost of the work itself.
func nsPerOp(p *Plan, in Input, samples int) float64 {
	for i := 0; i < 50; i++ {
		_, _ = p.Decode(in)
	}
	iters := 20
	for {
		start := time.Now()
		for i := 0; i < iters; i++ {
			_, _ = p.Decode(in)
		}
		if time.Since(start) > 4*time.Millisecond || iters > 1<<20 {
			break
		}
		iters *= 2
	}
	best := -1.0
	for s := 0; s < samples; s++ {
		start := time.Now()
		for i := 0; i < iters; i++ {
			_, _ = p.Decode(in)
		}
		ns := float64(time.Since(start).Nanoseconds()) / float64(iters)
		if best < 0 || ns < best {
			best = ns
		}
	}
	return best
}

// The adversarial corpus: a frame of valid packed decimal, a maximal valid LPP frame, the
// largest JSON bodies, and the longest legal text for the text natives.
func advFrameBCD() []byte {
	b := make([]byte, MaxFrameBytes)
	for i := range b {
		b[i] = 0x12
	}
	return b
}

// advFrameLPP is a valid Cayenne LPP frame of (nearly) MaxFrameBytes, every key distinct.
func advFrameLPP() []byte {
	var b []byte
	for _, typ := range []struct {
		t    byte
		data int
	}{{103, 2}, {104, 1}, {101, 2}, {115, 2}, {102, 1}, {2, 2}, {0, 1}} {
		for ch := 0; ch < 256 && len(b)+2+typ.data <= MaxFrameBytes; ch++ {
			b = append(b, byte(ch), typ.t)
			for i := 0; i < typ.data; i++ {
				b = append(b, byte(ch+i+1))
			}
		}
	}
	return b
}

func advJSON() []byte {
	var sb strings.Builder
	sb.WriteString(`{"k":3,"p":"` + strings.Repeat("0a", MaxTextBytes/2) + `","b64":"` + strings.Repeat("AQID", 1365) + `","a":{`)
	for i := 0; sb.Len() < MaxJSONBytes-MaxTextBytes*2-4000; i++ {
		fmt.Fprintf(&sb, `"k%d":%d.5,`, i, i)
	}
	sb.WriteString(`"z":1}}`)
	return []byte(sb.String())
}

type nsCase struct {
	name string
	doc  string
	in   Input
}

// rep is the call repeated n times in one expression, summed as a balanced tree (the parser's
// recursion limit is 32, so a left-leaning chain of 32 additions would not parse): n calls
// amortise the fixed cost of a decode and of an evaluation, so the time measured is the
// native's.
func rep(call string, n int) string {
	if n <= 1 {
		return call
	}
	return "(" + rep(call, n/2) + " + " + rep(call, n-n/2) + ")"
}

func nsCases() []nsCase {
	frame := advFrameBCD()
	js := advJSON()
	lpp := advFrameLPP()
	row := func(e string) string { return frameDoc("v", e) }
	jrow := func(e string) string {
		return `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":` + fmt.Sprintf("%q", e) + `}]}]}`
	}
	fin := Input{Frame: frame}
	jin := Input{Frame: frame, JSON: js}
	var cs []nsCase
	add := func(name, doc string, in Input) { cs = append(cs, nsCase{name, doc, in}) }
	const R = 32
	for _, fn := range []string{"u8", "i8", "u16be", "u16le", "u24be", "u24le", "u32be", "u32le", "i16be", "i16le", "i24be", "i24le", "i32be", "i32le"} {
		add(fn, row(rep(fmt.Sprintf("%s(frame, 4000)", fn), R)), fin)
	}
	for _, fn := range []string{"f32be", "f32le", "f64be", "f64le"} {
		add(fn, row(rep(fmt.Sprintf("%s(frame, 4000)", fn), R)), fin)
	}
	add("bit", row(rep("bit(frame, 4095, 7)", R)), fin)
	add("bits", row(rep("bits(frame, 4000, 12, 32)", R)), fin)
	add("sbits", row(rep("sbits(frame, 4000, 12, 32)", R)), fin)
	add("bcd", row(rep("bcd(frame, 0, 8)", R)), fin)
	add("slice 4096 x2", row(rep("size(slice(frame, 0, 4096))", 2)), fin)
	add("math.pow", row(rep("math.pow(1.0001, 1000000)", R)), fin)
	add("math.sqrt/floor/round", row(rep("math.sqrt(math.floor(math.round(1.5e9)))", R)), fin)
	add("int/double arithmetic", row(rep("(u16be(frame, 1) * 3 - 7) / 2 % 11", R)), fin)
	// The text natives are measured on literals (at most 2048 characters an expression), so JSON
	// parsing, which is priced on its own below, does not drown the decode in the measurement.
	hexLit := strings.Repeat("0a1B", 500) // 2000 characters
	b64Lit := strings.Repeat("AQID", 480) // 1920 characters
	// Four decodes of the literal, in four lets (one expression cannot hold the literal twice).
	textDoc := func(fn, lit string) string {
		var lets []string
		for i := 0; i < 4; i++ {
			lets = append(lets, fmt.Sprintf(`{"name":"t%d","expr":%q}`, i, fmt.Sprintf("size(%s(%q))", fn, lit)))
		}
		return `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[` + strings.Join(lets, ",") + `],"sections":[{"name":"s","outputs":[{"key":"v","expr":"t0 + t1 + t2 + t3"}]}]}`
	}
	add("hex.decode 2000 x4", textDoc("hex.decode", hexLit), fin)
	add("base64.decode 1920 x4", textDoc("base64.decode", b64Lit), fin)
	add("json parse 64 KiB", jrow("double(json.k)"), jin)
	add("lpp.decode 4 KiB", `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"m","expr":"lpp.decode(frame)"}],"sections":[{"name":"s","outputs":[{"key":"v","expr":"m.temperature_0"}]}]}`, Input{Frame: lpp})
	add("json select x32", jrow(rep("double(json.a.z)", R)), jin)
	add("json in/==", jrow("(json.type == \"x\" || json.k in [1, 2, 3]) ? 1 : 0"), jin)
	return cs
}

// A6: the declared cost of every native is checked against MEASURED time on an adversarial
// corpus, not against the native's own declaration: a native that under-declares (statically
// AND at run time, so the two agree) stands out as an outlier in ns per declared unit. The
// runtime limit cannot catch it; this test does. JSON parsing is charged too (ParseCost), so
// the same ratio validates its calibration.
func TestNativeCostsMatchMeasuredTime(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing: skipped under -short and -race")
	}
	var worst float64
	var rows []string
	for _, c := range nsCases() {
		p := mustCompile(t, c.doc, nil)
		if _, err := p.Decode(c.in); err != nil {
			t.Fatalf("%s: the adversarial input must decode: %v", c.name, err)
		}
		declared := float64(p.CostFor(c.in))
		ns := nsPerOp(p, c.in, 9)
		ratio := ns / declared
		worst = max(worst, ratio)
		rows = append(rows, fmt.Sprintf("%-24s declared %7.0f  measured %9.0f ns  = %7.2f ns/unit", c.name, declared, ns, ratio))
		if ratio > maxNSPerCost {
			t.Errorf("%s: %.0f ns/unit is over the bound of %d: the cost model under-declares", c.name, ratio, maxNSPerCost)
		}
	}
	for _, r := range rows {
		t.Log(r)
	}
	t.Logf("worst ns per declared unit: %.2f (bound %d)", worst, maxNSPerCost)
}

// scalingNS is the extra time per call a native spends on a maximal frame compared with a tiny
// one: the same call, at an offset valid in both, repeated n times in one expression. A native
// whose declared cost is a constant must spend about the same on both; one that walks the
// frame spends more as the frame grows, and the difference is its under-declaration made
// visible, free of the interpreter's fixed overhead (which cancels) and of any noise that does
// not scale with the frame.
func scalingNS(t *testing.T, call string, n int) float64 {
	t.Helper()
	doc := frameDoc("v", rep(call, n))
	p := mustCompile(t, doc, nil)
	big, small := Input{Frame: advFrameBCD()}, Input{Frame: advFrameBCD()[:16]}
	for _, in := range []Input{big, small} {
		if _, err := p.Decode(in); err != nil {
			t.Fatalf("%s: %v", call, err)
		}
	}
	return max(nsPerOp(p, big, 9)-nsPerOp(p, small, 9), 0) / float64(n)
}

// noiseFloorNS is the per-call difference below which a scaling measurement is noise.
const noiseFloorNS = 300

// A6, the single-slice-walk control: every fixed-cost native is measured at 16 bytes and at
// 4096; the per-call growth must stay within its declared cost (at most maxNSPerCost a unit)
// plus the noise floor. Natives whose declared cost already scales with the frame (slice,
// the decoders) are covered by TestNativeCostsMatchMeasuredTime.
func TestFixedCostNativesDoNotScaleWithTheFrame(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing: skipped under -short and -race")
	}
	for _, c := range []struct {
		call     string
		declared float64 // declared cost of one call
	}{
		{"u8(frame, 4)", 1}, {"i8(frame, 4)", 1}, {"u16be(frame, 4)", 1}, {"i16le(frame, 4)", 1}, {"u24be(frame, 4)", 1},
		{"i24le(frame, 4)", 1}, {"u32be(frame, 4)", 1}, {"i32le(frame, 4)", 1}, {"f32be(frame, 4)", 1}, {"f64le(frame, 4)", 1},
		{"bit(frame, 4, 7)", 1}, {"bits(frame, 4, 3, 17)", 1}, {"sbits(frame, 4, 3, 17)", 1}, {"bcd(frame, 4, 8)", 8},
		{"size(frame)", 1},
	} {
		grow := scalingNS(t, c.call, 32)
		allowed := noiseFloorNS + c.declared*maxNSPerCost
		t.Logf("%-24s grows %7.1f ns/call from 16 to 4096 bytes (allowed %.0f)", c.call, grow, allowed)
		if grow > allowed {
			t.Errorf("%s grows %.0f ns/call with the frame but declares %.0f: it walks the input", c.call, grow, c.declared)
		}
	}
}

// The check has to be able to fail. A native that declares cost 1 (statically AND at run time)
// and walks the 4 KiB frame ONCE must be flagged by the scaling check, and so must the
// 200-pass walker by the ns-per-cost check.
func TestNativeCostChecksFlagUnderDeclaration(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing: skipped under -short and -race")
	}
	grow := scalingNS(t, "test.walk1(frame)", 32)
	t.Logf("test.walk1 (one pass over the frame, declares 1): grows %.0f ns/call (allowed %d)", grow, noiseFloorNS+maxNSPerCost)
	if grow <= noiseFloorNS+maxNSPerCost {
		t.Errorf("a native declaring cost 1 and walking 4 KiB once grows only %.0f ns/call: the scaling check did not flag it", grow)
	}
	in := Input{Frame: advFrameBCD()}
	p := mustCompile(t, frameDoc("v", "test.walk(frame)"), nil)
	ratio := nsPerOp(p, in, 5) / float64(p.CostFor(in))
	t.Logf("test.walk (200 passes, declares 1): %.0f ns/unit (bound %d)", ratio, maxNSPerCost)
	if ratio <= maxNSPerCost {
		t.Errorf("a native declaring cost 1 and walking 4 KiB 200 times measures only %.0f ns/unit: the check did not flag it", ratio)
	}
}
