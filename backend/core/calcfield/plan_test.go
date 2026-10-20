// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var planMeasured = []Input{
	{Key: "voltage", Type: Double},
	{Key: "current", Type: Double},
	{Key: "pf_in", Type: Double},
	{Key: "fan", Type: Integer},
}

func def(key, expr string, t Type) Definition { return Definition{Key: key, Expression: expr, Type: t} }

// kwChain is the textbook chain: kW from V and I, kVA from V and I, power factor from both.
func kwChain() []Definition {
	return []Definition{
		def("pf", "kw / kva", Double),
		def("kva", "voltage * current / 1000.0", Double),
		def("kw", "kva * pf_in", Double),
	}
}

func planKeys(p *Plan) []string {
	keys := make([]string, p.Len())
	for i := range keys {
		keys[i] = p.Step(i).Key
	}
	return keys
}

func fill(t *testing.T, p *Plan, vals map[string]string) *Sample {
	t.Helper()
	s := p.Env().Sample()
	for k, v := range vals {
		ok, _ := s.Set(k, v)
		require.True(t, ok, k)
	}
	return s
}

// run evaluates the whole plan in order and returns each metric's result.
func run(p *Plan, s *Sample) map[string]Result {
	out := map[string]Result{}
	for i := 0; i < p.Len(); i++ {
		out[p.Step(i).Key] = p.Eval(s, i)
	}
	return out
}

func TestPlanOrdersTopologically(t *testing.T) {
	p, err := NewPlan(planMeasured, kwChain())
	require.NoError(t, err)
	require.Empty(t, p.Problems())
	assert.Equal(t, []string{"kva", "kw", "pf"}, planKeys(p))
	assert.True(t, p.Chained())
	assert.Equal(t, 1, p.Step(0).Depth)
	assert.Equal(t, 2, p.Step(1).Depth)
	assert.Equal(t, 3, p.Step(2).Depth)
}

// The order is a function of the metrics, not of how they were declared.
func TestPlanOrderIgnoresDeclarationOrder(t *testing.T) {
	defs := kwChain()
	want := []string{"kva", "kw", "pf"}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 50; i++ {
		rng.Shuffle(len(defs), func(a, b int) { defs[a], defs[b] = defs[b], defs[a] })
		p, err := NewPlan(planMeasured, defs)
		require.NoError(t, err)
		assert.Equal(t, want, planKeys(p))
	}
}

func TestPlanWithoutChainingIsKeyOrderAndWritesNothingBack(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("zeta", "voltage * 2.0", Double), def("alpha", "current * 2.0", Double)})
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "zeta"}, planKeys(p))
	assert.False(t, p.Chained())
	for i := 0; i < p.Len(); i++ {
		assert.Equal(t, -1, p.Step(i).feed)
	}
}

func TestPlanEvaluatesAChainWithStoredValues(t *testing.T) {
	p, err := NewPlan(planMeasured, kwChain())
	require.NoError(t, err)
	s := fill(t, p, map[string]string{"voltage": "230", "current": "10", "pf_in": "0.5"})
	defer s.Release()
	got := run(p, s)
	assert.Equal(t, "2.3", got["kva"].Value)
	assert.Equal(t, "1.15", got["kw"].Value) // 2.3 * 0.5, read from the stored "2.3"
	assert.Equal(t, "0.5", got["pf"].Value)
}

// A downstream formula sees the value that is persisted, formatted as its declared type: an
// INT upstream is truncated before it is read, a BOOLEAN is the 1/0 it is stored as.
func TestPlanFeedsTheDeclaredTypeNotTheWiderIntermediate(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("whole", "int(voltage / current)", Integer), // 23 for 230/10, truncated from 23.7
		def("twice", "whole * 2", Integer),
		def("hot", "voltage > 200.0", Boolean),
		def("flag", "hot ? 1 : 0", Integer),
	})
	require.NoError(t, err)
	s := fill(t, p, map[string]string{"voltage": "237", "current": "10"})
	defer s.Release()
	got := run(p, s)
	assert.Equal(t, "23", got["whole"].Value)
	assert.Equal(t, "46", got["twice"].Value)
	assert.Equal(t, "1", got["hot"].Value)
	assert.Equal(t, "1", got["flag"].Value)
}

func TestPlanSkipPropagatesAsMissingInput(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("ratio", "voltage / current", Double),
		def("half", "ratio / 2.0", Double),
		def("safe", "has(m.ratio) ? ratio : -1.0", Double),
		def("viaGuard", "has(m.half) ? half : -2.0", Double),
	})
	require.NoError(t, err)
	// current is 0: ratio skips as non_finite; the dependents are decided by the guard.
	s := fill(t, p, map[string]string{"voltage": "5", "current": "0"})
	defer s.Release()
	got := run(p, s)
	assert.Equal(t, SkipNonFinite, got["ratio"].Skip)
	assert.Equal(t, SkipMissingInput, got["half"].Skip, "an unguarded read of a skipped metric is a missing input")
	assert.Equal(t, "-1", got["safe"].Value)
	assert.Equal(t, "-2", got["viaGuard"].Value)
}

// A sample that is reused without Reset must not leak the previous evaluation's value.
func TestPlanClearsAFedValueWhenTheMetricSkips(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("ratio", "voltage / current", Double), def("half", "ratio / 2.0", Double)})
	require.NoError(t, err)
	s := fill(t, p, map[string]string{"voltage": "10", "current": "5"})
	defer s.Release()
	assert.Equal(t, "1", run(p, s)["half"].Value)
	require.NoError(t, s.SetAt(mustIndex(t, p, "current"), "0"))
	got := run(p, s)
	assert.Equal(t, SkipNonFinite, got["ratio"].Skip)
	assert.Equal(t, SkipMissingInput, got["half"].Skip)
}

func mustIndex(t *testing.T, p *Plan, key string) int {
	t.Helper()
	i, ok := p.Env().Index(key)
	require.True(t, ok, key)
	return i
}

func TestPlanRefusesCyclesAndNamesThem(t *testing.T) {
	for name, tc := range map[string]struct {
		defs []Definition
		ring string
	}{
		"self":     {[]Definition{def("a", "a + voltage", Double)}, "a -> a"},
		"pair":     {[]Definition{def("b", "a + voltage", Double), def("a", "b + voltage", Double)}, "a -> b -> a"},
		"triangle": {[]Definition{def("c", "a * 1.0 + voltage", Double), def("a", "b * 1.0 + voltage", Double), def("b", "c * 1.0 + voltage", Double)}, "a -> b -> c -> a"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := NewPlan(planMeasured, tc.defs)
			require.NoError(t, err)
			probs := p.Problems()
			require.NotEmpty(t, probs)
			var ce *CycleError
			require.True(t, errors.As(probs[0], &ce), probs[0].Error())
			assert.Equal(t, tc.ring, strings.Join(ce.Path, " -> "))
			assert.Contains(t, ce.Error(), tc.ring)
			// Every member of the ring is disabled and skips loudly.
			s := fill(t, p, map[string]string{"voltage": "1"})
			defer s.Release()
			for i := 0; i < p.Len(); i++ {
				if p.Step(i).Formula == nil {
					assert.Equal(t, SkipEvalError, p.Eval(s, i).Skip)
				}
			}
		})
	}
}

func TestPlanCycleOutsideMetricsStillRun(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("a", "b + voltage", Double), def("b", "a + voltage", Double), def("ok", "voltage + 1.0", Double)})
	require.NoError(t, err)
	assert.Len(t, p.Problems(), 2)
	s := fill(t, p, map[string]string{"voltage": "1"})
	defer s.Release()
	assert.Equal(t, "2", run(p, s)["ok"].Value)
}

func chainOf(n int) []Definition {
	defs := make([]Definition, n)
	for i := range defs {
		src := "voltage"
		if i > 0 {
			src = fmt.Sprintf("c%d", i)
		}
		defs[i] = def(fmt.Sprintf("c%d", i+1), src+" + 1.0", Double)
	}
	return defs
}

func TestPlanDepthLimit(t *testing.T) {
	p, err := NewPlan(planMeasured, chainOf(MaxChainDepth))
	require.NoError(t, err)
	assert.Empty(t, p.Problems(), "a chain of exactly the maximum is allowed")
	assert.Equal(t, MaxChainDepth, p.Step(p.Len()-1).Depth)

	p, err = NewPlan(planMeasured, chainOf(MaxChainDepth+1))
	require.NoError(t, err)
	probs := p.Problems()
	require.Len(t, probs, 1)
	var de *DepthError
	require.True(t, errors.As(probs[0], &de))
	assert.Equal(t, "c5", de.Key)
	assert.Equal(t, 5, de.Depth)
	assert.Equal(t, []string{"c5", "c4", "c3", "c2", "c1"}, de.Chain)
	assert.Contains(t, de.Error(), "above the maximum of 4")

	// The too-deep link never runs; the four before it still do.
	s := fill(t, p, map[string]string{"voltage": "0"})
	defer s.Release()
	got := run(p, s)
	assert.Equal(t, "4", got["c4"].Value)
	assert.Equal(t, SkipEvalError, got["c5"].Skip)

	p, err = NewPlan(planMeasured, chainOf(7))
	require.NoError(t, err)
	assert.Len(t, p.Problems(), 3, "every metric past the limit is named")
}

// Depth is the LONGEST chain, whichever branch of a diamond is shorter.
func TestPlanDepthTakesTheLongestBranch(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("a", "voltage + 1.0", Double),
		def("b", "a + 1.0", Double),
		def("c", "b + 1.0", Double),
		def("d", "c + a", Double), // via c: depth 4; via a: depth 2
	})
	require.NoError(t, err)
	assert.Empty(t, p.Problems())
	assert.Equal(t, 4, p.Step(p.Len()-1).Depth)
}

func TestPlanCompileErrorsArePerMetric(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("bad", "nosuch + 1.0", Double), def("ok", "voltage + 1.0", Double),
		def("dep", "bad + 1.0", Double)})
	require.NoError(t, err)
	probs := p.Problems()
	require.Len(t, probs, 1, "dep reads a declared metric; only bad fails to compile")
	var ce *CompileError
	require.True(t, errors.As(probs[0], &ce))
	assert.Equal(t, "bad", ce.Key)
	s := fill(t, p, map[string]string{"voltage": "1"})
	defer s.Release()
	got := run(p, s)
	assert.Equal(t, "2", got["ok"].Value)
	assert.Equal(t, SkipMissingInput, got["dep"].Skip)
}

func TestPlanKeyCollidingWithMeasuredIsRefused(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{def("voltage", "current * 2.0", Double)})
	require.NoError(t, err)
	probs := p.Problems()
	require.Len(t, probs, 1)
	assert.Contains(t, probs[0].Error(), "already a measured metric")
	assert.Nil(t, p.Step(0).Formula)
}

func TestPlanCostOfAChainSumsAgainstTheProfileCeiling(t *testing.T) {
	p, err := NewPlan(planMeasured, kwChain())
	require.NoError(t, err)
	var sum uint64
	for _, f := range p.Formulas() {
		sum += f.CostMax()
	}
	require.NoError(t, CheckProfileCost(p.Formulas()))
	assert.Greater(t, sum, p.Formulas()[0].CostMax(), "the chain is more than its first link")
	// Ninety links of a heavy formula exceed the profile ceiling together though each is under its own.
	heavy := "math.sqrt(math.abs(voltage * current + voltage)) + math.sqrt(math.abs(current * voltage + current)) + math.sqrt(math.abs(voltage - current))"
	defs := make([]Definition, 0, 90)
	prev := "voltage"
	for i := 0; i < 90; i++ {
		key := fmt.Sprintf("h%02d", i)
		defs = append(defs, def(key, strings.ReplaceAll(heavy, "voltage", prev), Double))
		if i%MaxChainDepth == MaxChainDepth-1 {
			prev = "voltage"
		} else {
			prev = key
		}
	}
	hp, err := NewPlan(planMeasured, defs)
	require.NoError(t, err)
	require.Empty(t, hp.Problems(), "each link is under the per-formula ceiling and no chain is too deep")
	var ce *CostError
	require.True(t, errors.As(CheckProfileCost(hp.Formulas()), &ce))
	assert.Equal(t, ProfileCostCeiling, ce.Ceiling)
}

// 1000 evaluations of one chain are byte-identical.
func TestPlanIsDeterministic(t *testing.T) {
	p, err := NewPlan(planMeasured, append(kwChain(), def("code", "int(pf * 100.0)", Integer)))
	require.NoError(t, err)
	var first string
	for n := 0; n < 1000; n++ {
		s := fill(t, p, map[string]string{"voltage": "230.7", "current": "12.31", "pf_in": "0.93"})
		var b strings.Builder
		for i := 0; i < p.Len(); i++ {
			r := p.Eval(s, i)
			fmt.Fprintf(&b, "%s=%s/%s;", p.Step(i).Key, r.Value, r.Skip)
		}
		s.Release()
		if n == 0 {
			first = b.String()
			require.Contains(t, first, "code=")
		}
		require.Equal(t, first, b.String(), "evaluation %d", n)
	}
}

// A calculated metric may read measured and calculated metrics through m[...] too.
func TestPlanReadsCalculatedMetricsThroughM(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("a-b", "voltage + 1.0", Double), def("down", `double(m["a-b"]) * 2.0`, Double)})
	require.NoError(t, err)
	require.Empty(t, p.Problems())
	s := fill(t, p, map[string]string{"voltage": "1"})
	defer s.Release()
	assert.Equal(t, "4", run(p, s)["down"].Value)
}

// A sample that carries none of a formula's measured inputs is not the formula's: Applicable is
// false and the step is not evaluated. Some of them is applicable (and a skip when one it
// requires is absent). A chain is applicable on the measured inputs it reads transitively.
func TestPlanApplicability(t *testing.T) {
	p, err := NewPlan(planMeasured, append(kwChain(),
		def("fan_only", "double(fan)", Double),
		def("guarded", "has(m.pf_in) ? pf_in : 0.0", Double),
		def("over_guarded", "guarded * device.k", Double),
	))
	require.NoError(t, err)
	require.Empty(t, p.Problems(), "a step that did not compile is applicable vacuously")
	applicable := func(key string, set map[string]string) bool {
		s := p.Env().Sample()
		defer s.Release()
		for k, v := range set {
			_, _ = s.Set(k, v)
		}
		i, ok := p.StepIndex(key)
		require.True(t, ok, key)
		return p.Applicable(s, i)
	}
	// Nothing the formula reads is in the sample.
	assert.False(t, applicable("fan_only", map[string]string{"voltage": "1"}))
	assert.False(t, applicable("fan_only", nil))
	assert.True(t, applicable("fan_only", map[string]string{"fan": "1"}))
	// A value that is present but does not parse is carried: invalid_input is a real signal.
	assert.True(t, applicable("fan_only", map[string]string{"fan": "x"}))
	// Some inputs: applicable, and Eval reports the one it lacks.
	s := p.Env().Sample()
	defer s.Release()
	_, _ = s.Set("voltage", "230")
	i, _ := p.StepIndex("kva")
	require.True(t, p.Applicable(s, i))
	assert.Equal(t, SkipMissingInput, p.Eval(s, i).Skip)
	// A chain is applicable through the measured inputs it reads transitively.
	assert.False(t, applicable("pf", map[string]string{"fan": "1"}))
	assert.True(t, applicable("pf", map[string]string{"pf_in": "1"}), "kw reads pf_in, pf reads kw")
	assert.True(t, applicable("kw", map[string]string{"voltage": "1"}))
	// A formula that tests every input it reads for presence is evaluated on every sample.
	assert.True(t, applicable("guarded", nil))
	// So is one that requires only such a metric (and a device constant, which no sample carries).
	assert.True(t, applicable("over_guarded", nil))
}

func BenchmarkApplicable(b *testing.B) {
	p, err := NewPlan(planMeasured, kwChain())
	require.NoError(b, err)
	s := p.Env().Sample()
	_, _ = s.Set("fan", "1")
	b.ReportAllocs()
	for b.Loop() {
		for i := 0; i < p.Len(); i++ {
			_ = p.Applicable(s, i)
		}
	}
}

// Applicability is sound: it never withholds a value that evaluating the step would have
// produced. Every shape of formula, over every subset of the measured inputs (each present,
// absent or present-but-unparseable), with and without the device constant: a step that is
// not applicable must be one that evaluation skips, and every step evaluated after it sees the
// same sample it would have. Gating changes what is COUNTED as a skip, never what is computed.
func TestApplicabilityNeverWithholdsAValue(t *testing.T) {
	defs := append(kwChain(),
		def("fan_only", "double(fan)", Double),
		def("guarded", "has(m.pf_in) ? pf_in : 0.0", Double),
		def("guarded_int", "has(m.fan) ? double(int(m.fan)) : 0.0", Double),
		def("guarded_some", "has(m.pf_in) ? pf_in : voltage", Double),
		def("guarded_all_sum", "(has(m.pf_in) ? pf_in : 1.0) + (has(m.voltage) ? voltage : 2.0)", Double),
		def("in_guard", "\"current\" in m ? current : -1.0", Double),
		def("guarded_constant", "has(m.pf_in) ? pf_in : device.k", Double),
		def("guarded_or_constant", "has(m.pf_in) ? pf_in : (has(device.k) ? device.k : 3.0)", Double),
		def("constant_and_input", "device.k * voltage", Double),
		// Calculated metrics reading the always-evaluated ones above: a value they produce on a
		// sample with none of their measured inputs is a value, not a sample to withhold.
		def("over_guarded", "guarded * 2.0", Double),
		def("over_guarded_chain", "over_guarded + 1.0", Double),
		def("over_guarded_constant", "guarded_or_constant + 1.0", Double),
		def("over_guarded_and_input", "guarded + current", Double),
		def("over_guarded_and_gated", "guarded + kva", Double),
		def("guarded_over_gated", "has(m.kva) ? kva : guarded", Double),
	)
	p, err := NewPlan(planMeasured, defs)
	require.NoError(t, err)
	require.Empty(t, p.Problems())
	keys := []string{"voltage", "current", "pf_in", "fan"}
	good := map[string]string{"voltage": "230", "current": "10", "pf_in": "0.5", "fan": "2"}
	for _, consts := range [][]Constant{nil, {NewConstant("k", 4)}} {
		for mask := 0; mask < 81; mask++ { // 3^4: absent, present, unparseable
			vals := map[string]string{}
			for b, m := 0, mask; b < len(keys); b, m = b+1, m/3 {
				switch m % 3 {
				case 1:
					vals[keys[b]] = good[keys[b]]
				case 2:
					vals[keys[b]] = "x"
				}
			}
			all, gated := p.Env().Sample(), p.Env().Sample()
			for k, v := range vals {
				_, _ = all.Set(k, v)
				_, _ = gated.Set(k, v)
			}
			all.UseConstants(consts)
			gated.UseConstants(consts)
			for i := 0; i < p.Len(); i++ {
				want := p.Eval(all, i)
				if !p.Applicable(gated, i) {
					assert.False(t, want.OK(), "%s on %v (constants %v): withheld %q",
						p.Step(i).Key, vals, consts, want.Value)
					continue
				}
				assert.Equal(t, want, p.Eval(gated, i), "%s on %v", p.Step(i).Key, vals)
			}
			all.Release()
			gated.Release()
		}
	}
}
