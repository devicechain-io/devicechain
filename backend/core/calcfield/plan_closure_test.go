// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClosureChainsAndDiamonds(t *testing.T) {
	// kw reads kva; pf reads kw and kva (a diamond over kva); lone reads only a measured input.
	p, err := NewPlan(planMeasured, append(kwChain(), def("lone", "voltage + 1.0", Double)))
	require.NoError(t, err)
	require.Empty(t, p.Problems())

	got, err := p.Closure([]string{"pf"})
	require.NoError(t, err)
	assert.Equal(t, []string{"kva", "kw", "pf"}, got, "dependencies first, each once")

	got, err = p.Closure([]string{"kw", "pf", "kva", "kw"})
	require.NoError(t, err)
	assert.Equal(t, []string{"kva", "kw", "pf"}, got, "repeated and overlapping keys collapse")

	got, err = p.Closure([]string{"kva"})
	require.NoError(t, err)
	assert.Equal(t, []string{"kva"}, got)

	got, err = p.Closure([]string{"lone"})
	require.NoError(t, err)
	assert.Equal(t, []string{"lone"}, got, "a metric over measured inputs only adds nothing")

	got, err = p.Closure(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestClosureAtMaximumDepth(t *testing.T) {
	p, err := NewPlan(planMeasured, chainOf(MaxChainDepth))
	require.NoError(t, err)
	got, err := p.Closure([]string{"c4"})
	require.NoError(t, err)
	assert.Equal(t, []string{"c1", "c2", "c3", "c4"}, got)
}

func TestClosureRefusesWhatCannotRun(t *testing.T) {
	t.Run("unknown key", func(t *testing.T) {
		p, err := NewPlan(planMeasured, kwChain())
		require.NoError(t, err)
		_, err = p.Closure([]string{"nope"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"nope"`)
	})
	t.Run("a measured metric is not a calculated one", func(t *testing.T) {
		p, err := NewPlan(planMeasured, kwChain())
		require.NoError(t, err)
		_, err = p.Closure([]string{"voltage"})
		require.Error(t, err)
	})
	t.Run("too deep", func(t *testing.T) {
		p, err := NewPlan(planMeasured, chainOf(MaxChainDepth+1))
		require.NoError(t, err)
		_, err = p.Closure([]string{"c5"})
		require.Error(t, err)
		var de *DepthError
		assert.ErrorAs(t, err, &de)
		got, err := p.Closure([]string{"c4"})
		require.NoError(t, err, "the links before the too-deep one still run")
		assert.Len(t, got, 4)
	})
	t.Run("cycle members", func(t *testing.T) {
		p, err := NewPlan(planMeasured, []Definition{
			def("a", "b + voltage", Double), def("b", "a + voltage", Double), def("ok", "voltage + 1.0", Double)})
		require.NoError(t, err)
		_, err = p.Closure([]string{"a"})
		var ce *CycleError
		assert.ErrorAs(t, err, &ce)
		got, err := p.Closure([]string{"ok"})
		require.NoError(t, err)
		assert.Equal(t, []string{"ok"}, got)
	})
	t.Run("broken transitive dependency", func(t *testing.T) {
		// mid reads broken, which does not compile (an undeclared reference); top reads mid.
		p, err := NewPlan(planMeasured, []Definition{
			def("broken", "voltage + nowhere", Double),
			def("mid", "broken + 1.0", Double),
			def("top", "mid + 1.0", Double),
			def("fine", "voltage + 1.0", Double)})
		require.NoError(t, err)
		_, err = p.Closure([]string{"top"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"broken"`, "names the member that cannot run")
		got, err := p.Closure([]string{"fine"})
		require.NoError(t, err)
		assert.Equal(t, []string{"fine"}, got)
	})
}

func TestClosureCannotLoopOnAnyPlan(t *testing.T) {
	// Cycles are impossible by construction (NewPlan removes every ring's edges); assert it
	// across the ring shapes by requiring Closure to terminate with the ring refused.
	for name, defs := range map[string][]Definition{
		"self":     {def("a", "a + voltage", Double)},
		"pair":     {def("b", "a + voltage", Double), def("a", "b + voltage", Double)},
		"triangle": {def("c", "a * 1.0 + voltage", Double), def("a", "b * 1.0 + voltage", Double), def("b", "c * 1.0 + voltage", Double)},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := NewPlan(planMeasured, defs)
			require.NoError(t, err)
			for i := 0; i < p.Len(); i++ {
				_, err := p.Closure([]string{p.Step(i).Key})
				assert.Error(t, err)
			}
		})
	}
}

func TestSingleFormulaPlanEvaluatesTheGivenFormula(t *testing.T) {
	env, err := NewEnv([]Input{{Key: "a", Type: Double}, {Key: "b", Type: Double}})
	require.NoError(t, err)
	f, err := env.Compile(Definition{Expression: "a + b", Type: Double})
	require.NoError(t, err)
	p := SingleFormulaPlan(env, "", f)
	assert.Equal(t, 1, p.Len())
	assert.False(t, p.Chained())
	s := env.Sample()
	defer s.Release()
	_, _ = s.Set("a", "1.5")
	_, _ = s.Set("b", "2")
	want := f.Eval(s)
	got := p.Eval(s, 0)
	assert.Equal(t, want, got)
	assert.Equal(t, "3.5", got.Value)
}
