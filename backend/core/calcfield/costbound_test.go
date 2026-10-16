// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
)

// exprGen generates random, well-typed expressions over fixtureInputs from the language the
// allowlist admits: every operator, conversion, presence test and math function, nested.
// With mirror set it renders the operands of every && and || the other way round, making the
// same random choices in the same order, so two generators on equal seeds write a formula and
// its operand-order mirror.
type exprGen struct {
	r      *rand.Rand
	mirror bool
}

func (g exprGen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

func (g exprGen) double(depth int) string {
	if depth <= 0 || g.r.IntN(4) == 0 {
		return g.pick("voltage", "current", "temp", `double(m["a-b"])`, `double(m["in"])`, "double(m.voltage)",
			"1.5", "0.0", "-2.25", "1e308", "1e-300")
	}
	d := depth - 1
	switch g.r.IntN(12) {
	case 0:
		return "(" + g.double(d) + g.pick(" + ", " - ", " * ", " / ") + g.double(d) + ")"
	case 1:
		return "(" + g.double(d) + g.pick(" + ", " - ", " * ", " / ") + g.int(d) + ")"
	case 2:
		return "(" + g.int(d) + g.pick(" + ", " - ", " * ", " / ") + g.double(d) + ")"
	case 3:
		return "-" + g.double(d)
	case 4:
		return g.pick("math.abs", "math.floor", "math.ceil", "math.round", "math.sqrt") + "(" + g.double(d) + ")"
	case 5:
		return "math.sqrt(" + g.int(d) + ")"
	case 6:
		return g.pick("math.min", "math.max") + "(" + g.num(d) + ", " + g.double(d) + ")"
	case 7:
		return "math.pow(" + g.num(d) + ", " + g.int(d) + ")"
	case 8:
		return "math.clamp(" + g.double(d) + ", " + g.double(d) + ", " + g.double(d) + ")"
	case 9:
		return "double(" + g.int(d) + ")"
	case 10:
		return "(" + g.bool(d) + " ? " + g.double(d) + " : " + g.double(d) + ")"
	default:
		return "(has(m.voltage) ? voltage : " + g.double(d) + ")"
	}
}

func (g exprGen) int(depth int) string {
	if depth <= 0 || g.r.IntN(4) == 0 {
		return g.pick("fan", `int(m["count-x"])`, "int(m.fan)", "0", "1", "-1", "7",
			"9223372036854775807", "-9223372036854775807")
	}
	d := depth - 1
	switch g.r.IntN(8) {
	case 0, 1:
		return "(" + g.int(d) + g.pick(" + ", " - ", " * ", " / ", " % ") + g.int(d) + ")"
	case 2:
		return "-" + g.int(d)
	case 3:
		return "math.abs(" + g.int(d) + ")"
	case 4:
		return g.pick("math.min", "math.max") + "(" + g.int(d) + ", " + g.int(d) + ")"
	case 5:
		return "math.clamp(" + g.int(d) + ", " + g.int(d) + ", " + g.int(d) + ")"
	case 6:
		return "int(" + g.double(d) + ")"
	default:
		return "(" + g.bool(d) + " ? " + g.int(d) + " : " + g.int(d) + ")"
	}
}

func (g exprGen) num(depth int) string {
	if g.r.IntN(2) == 0 {
		return g.int(depth)
	}
	return g.double(depth)
}

func (g exprGen) bool(depth int) string {
	if depth <= 0 || g.r.IntN(4) == 0 {
		return g.pick("door", `bool(m["m"])`, "has(m.fan)", `("a-b" in m)`, "has(m.temp)", `("count-x" in m)`,
			"true", "false")
	}
	d := depth - 1
	switch g.r.IntN(7) {
	case 0:
		return "!" + g.bool(d)
	case 1, 2:
		l := g.bool(d)
		op := g.pick(" && ", " || ")
		rt := g.bool(d)
		if g.mirror {
			l, rt = rt, l
		}
		return "(" + l + op + rt + ")"
	case 3:
		return "(" + g.num(d) + g.pick(" < ", " <= ", " > ", " >= ") + g.num(d) + ")"
	case 4:
		return "(" + g.double(d) + g.pick(" == ", " != ") + g.double(d) + ")"
	case 5:
		return "(" + g.int(d) + g.pick(" == ", " != ") + g.int(d) + ")"
	default:
		return "(" + g.bool(d) + " ? " + g.bool(d) + " : " + g.bool(d) + ")"
	}
}

// fill sets each fixture input to a random value, or leaves it absent.
func fillRandom(r *rand.Rand, s *Sample) {
	s.Reset()
	for _, in := range fixtureInputs {
		if r.IntN(5) == 0 {
			continue
		}
		var v string
		switch in.Type {
		case Double:
			v = []string{"0", "-0", "1", "-1.5", "230.5", "1e308", "-1e308", "5e-324", "NaN", "+Inf", "-Inf"}[r.IntN(11)]
		case Integer:
			v = []string{"0", "1", "-1", "2", "9223372036854775807", "-9223372036854775808"}[r.IntN(6)]
		case Boolean:
			v = []string{"true", "false"}[r.IntN(2)]
		}
		_, _ = s.Set(in.Key, v)
	}
}

// TestStaticEstimateBoundsRuntimeCost is WHY Formula programs carry no runtime cel.CostLimit.
//
// For every admitted formula and every sample, the cost cel-go's tracker actually charges
// must never exceed the static worst-case estimate the compile gate admitted. The language
// has no loops, comprehensions or size-dependent operations, so each node is charged at most
// once, and the static gate is a complete bound. If this test fails — because the allowlist,
// the generator's language or cel-go's cost model changed so that something's cost grows
// with its input — the static gate is no longer enough: restore a runtime CostLimit (and a
// "cost" skip reason) before admitting the construct. Extend exprGen whenever the language
// grows, or this test stops covering it.
func TestStaticEstimateBoundsRuntimeCost(t *testing.T) {
	env := fixtureEnv(t)
	r := rand.New(rand.NewPCG(1, 2))
	g := exprGen{r: r}
	types := []Type{Double, Integer, Boolean}
	admitted, samples, tight := 0, 0, 0
	s := env.Sample()
	defer s.Release()
	for i := 0; admitted < 3000 && i < 50000; i++ {
		ty := types[r.IntN(3)]
		var expr string
		switch depth := 1 + r.IntN(6); ty {
		case Double:
			expr = g.double(depth)
		case Integer:
			expr = g.int(depth)
		default:
			expr = g.bool(depth)
		}
		f, err := env.Compile(Definition{Key: "out", Expression: expr, Type: ty})
		if err != nil {
			// The generator only writes well-typed admitted syntax, so the only refusals
			// expected are the cost ceiling and a formula naming no input.
			var ce *CostError
			if !(asCostError(err, &ce) || strings.Contains(err.Error(), "reads no input") || strings.Contains(err.Error(), "above the maximum of 1024")) {
				t.Fatalf("generator wrote a refused expression %q: %v", expr, err)
			}
			continue
		}
		admitted++
		ast, iss := env.cel.Compile(expr)
		require.NoError(t, iss.Err())
		prg, err := env.cel.Program(ast, cel.EvalOptions(cel.OptTrackCost))
		require.NoError(t, err)
		for range 8 {
			fillRandom(r, s)
			_, det, _ := prg.Eval(s)
			samples++
			actual := *det.ActualCost()
			if actual > f.CostMax() {
				t.Fatalf("%q: actual runtime cost %d exceeds the static estimate %d", expr, actual, f.CostMax())
			}
			if actual == f.CostMax() {
				tight++
			}
		}
	}
	require.GreaterOrEqual(t, admitted, 3000, "the generator must reach the admitted set")
	require.Positive(t, tight, "some evaluations must land exactly on the estimate, or the comparison is not being exercised at its boundary")
	t.Logf("%d admitted formulas, %d evaluations, %d at exactly the estimate", admitted, samples, tight)
}

func asCostError(err error, target **CostError) bool {
	ce, ok := err.(*CostError)
	if ok {
		*target = ce
	}
	return ok
}

// TestLogicalOperandOrderDoesNotChangeTheOutcome pins the missing-input policy's promise that
// operand order never matters: a formula and its mirror — every && and || with its operands
// swapped — produce the same value, or both skip, on every sample. CEL short-circuits left to
// right, so which input reads are REACHED depends on order; the outcome may not.
//
// The one thing order may change is WHICH of two failures names the skip (an absent input on
// one side of || and a reversed clamp on the other): CEL reports the first it met. Both orders
// still skip, and the reason is a counter label, not a stored value.
func TestLogicalOperandOrderDoesNotChangeTheOutcome(t *testing.T) {
	env := fixtureEnv(t)
	samples := rand.New(rand.NewPCG(5, 6))
	s := env.Sample()
	defer s.Release()
	compared, differentText := 0, 0
	for seed := uint64(0); compared < 3000 && seed < 50000; seed++ {
		depth := 2 + int(seed%5)
		expr := exprGen{r: rand.New(rand.NewPCG(seed, 9))}.bool(depth)
		mirrored := exprGen{r: rand.New(rand.NewPCG(seed, 9)), mirror: true}.bool(depth)
		f, err := env.Compile(Definition{Key: "out", Expression: expr, Type: Boolean})
		if err != nil {
			continue
		}
		fm, err := env.Compile(Definition{Key: "out", Expression: mirrored, Type: Boolean})
		require.NoError(t, err, "the mirror of an admitted formula is admitted: %q", mirrored)
		compared++
		if expr != mirrored {
			differentText++
		}
		for range 8 {
			fillRandom(samples, s)
			a, b := f.Eval(s), fm.Eval(s)
			if a.Value != b.Value || a.OK() != b.OK() {
				t.Fatalf("operand order changed the outcome:\n  %s\n    → value=%q skip=%q (%v)\n  %s\n    → value=%q skip=%q (%v)",
					expr, a.Value, a.Skip, a.Err, mirrored, b.Value, b.Skip, b.Err)
			}
		}
	}
	require.GreaterOrEqual(t, compared, 3000)
	require.Greater(t, differentText, compared/4, "the mirror must actually reorder operands")
	t.Logf("%d formulas compared with their mirror (%d textually different)", compared, differentText)
}
