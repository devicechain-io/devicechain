// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Calculated-to-calculated chaining (ADR-082): a formula may read another calculated metric,
// to a depth of calcfield.MaxChainDepth. These tests hold the resolver to the contract the
// package documents (core/calcfield/doc.go, "Chaining") and the preview to the resolver.

// chainVersion is a meter whose calculated metrics form a chain:
//
//	apparent (kVA) = voltage * current / 1000
//	real (kW)      = apparent * pf_in
//	pf             = real / apparent          (reads both, a diamond)
//	pf_pct         = int(math.round(pf * 100.0)) (INT: reads a DOUBLE)
//	healthy        = pf_pct >= 90 && !has(m.fault)   (BOOLEAN over an INT; depth 5 would be refused)
//
// healthy reads pf_pct, which reads real and apparent: a chain of four.
func chainVersion(token string) model.ProfileResolution {
	return model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: token}, []*model.MetricDefinition{
		measured(1, "voltage", model.MetricDouble, "V"),
		measured(2, "current", model.MetricDouble, "A"),
		measured(3, "pf_in", model.MetricDouble, ""),
		measured(4, "fault", model.MetricInt, ""),
		calculatedDef(10, "healthy", model.MetricBoolean, "", "pf_pct >= 90 && !has(m.fault)"),
		calculatedDef(11, "pf_pct", model.MetricInt, "%", "int(math.round(real / apparent * 100.0))"),
		calculatedDef(12, "pf", model.MetricDouble, "", "real / apparent"),
		calculatedDef(13, "real", model.MetricDouble, "kW", "apparent * pf_in"),
		calculatedDef(14, "apparent", model.MetricDouble, "kVA", "voltage * current / 1000.0"),
	})
}

func chainValues(t *testing.T, p *resolvedevent.ResolvedMeasurementsPayload) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range p.Entries[0].Entries {
		out[e.Name] = e.Value
	}
	return out
}

func TestAChainIsComputedInDependencyOrderAndStoredAsDeclared(t *testing.T) {
	profile := chainVersion("meter@chain")
	require.Equal(t, 5, profile.CalculatedCount())
	metrics := testCalcMetrics()
	got := resolveSamples(t, profile, metrics, map[string]string{"voltage": "250", "current": "8", "pf_in": "0.95"})
	v := chainValues(t, got)
	assert.Equal(t, "2", v["apparent"])
	assert.Equal(t, "1.9", v["real"])
	assert.Equal(t, "0.95", v["pf"])
	assert.Equal(t, "95", v["pf_pct"], "read from the stored 1.9 and 2, converted to an INT")
	assert.Equal(t, "1", v["healthy"], "a BOOLEAN over an INT over two DOUBLEs")
	assert.EqualValues(t, 5, testutil.ToFloat64(metrics.evaluations))
	for _, e := range got.Entries[0].Entries {
		if e.Derived {
			require.NotNil(t, e.Classifier)
			require.NotNil(t, e.DataType)
		}
	}
}

func TestAnUpstreamSkipSkipsItsReadersUnlessGuarded(t *testing.T) {
	profile := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@skip"}, []*model.MetricDefinition{
		measured(1, "voltage", model.MetricDouble, ""),
		measured(2, "current", model.MetricDouble, ""),
		calculatedDef(10, "ratio", model.MetricDouble, "", "voltage / current"),
		calculatedDef(11, "half", model.MetricDouble, "", "ratio / 2.0"),
		calculatedDef(12, "guarded", model.MetricDouble, "", "has(m.ratio) ? ratio : -1.0"),
		calculatedDef(13, "quarter", model.MetricDouble, "", "half / 2.0"),
	})
	metrics := testCalcMetrics()
	// current is 0: ratio skips as non_finite, and everything that reads it follows.
	got := resolveSamples(t, profile, metrics, map[string]string{"voltage": "5", "current": "0"})
	assert.Equal(t, []string{"current", "guarded*", "voltage"}, names(got.Entries[0].Entries))
	assert.Equal(t, "-1", chainValues(t, got)["guarded"])
	assert.EqualValues(t, 1, metrics.skips(calcfield.SkipNonFinite), "only the metric that divided by zero")
	assert.EqualValues(t, 2, metrics.skips(calcfield.SkipMissingInput), "half, and quarter through it")
}

// A value a device sends under a calculated key is dropped and never reaches a formula that
// reads that key: the chain reads what was computed, or nothing.
func TestADeviceCannotInjectAnIntermediateValue(t *testing.T) {
	profile := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@inject"}, []*model.MetricDefinition{
		measured(1, "voltage", model.MetricDouble, ""),
		measured(2, "current", model.MetricDouble, ""),
		calculatedDef(10, "ratio", model.MetricDouble, "", "voltage / current"),
		calculatedDef(11, "half", model.MetricDouble, "", "has(m.ratio) ? ratio / 2.0 : -1.0"),
	})
	got := resolveSamples(t, profile, testCalcMetrics(), map[string]string{"voltage": "5", "current": "0", "ratio": "100"})
	assert.Equal(t, "-1", chainValues(t, got)["half"])
	assert.NotContains(t, chainValues(t, got), "ratio")
}

// Event identity does not see a calculated value, a chained one included: the same reading
// under a profile with no formulas has the same identity view.
func TestChainedValuesAreOutsideEventIdentity(t *testing.T) {
	reading := map[string]string{"voltage": "230", "current": "10", "pf_in": "0.93"}
	withChain := resolveSamples(t, chainVersion("meter@chain"), testCalcMetrics(), reading)
	plain := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@plain"}, []*model.MetricDefinition{
		measured(1, "voltage", model.MetricDouble, "V"),
		measured(2, "current", model.MetricDouble, "A"),
		measured(3, "pf_in", model.MetricDouble, ""),
		measured(4, "fault", model.MetricInt, ""),
	})
	without := resolveSamples(t, plain, testCalcMetrics(), reading)
	a, err := json.Marshal(withChain.IdentityView())
	require.NoError(t, err)
	b, err := json.Marshal(without.IdentityView())
	require.NoError(t, err)
	assert.JSONEq(t, string(b), string(a))
}

// chainCorpus is the readings the parity and determinism tests run over: precision beyond
// eight places, every missing input, a zero divisor (the upstream skip), an invalid and a
// non-finite input, an injected intermediate.
func chainCorpus() []map[string]string {
	return []map[string]string{
		{"voltage": "230", "current": "10", "pf_in": "0.93"},
		{"voltage": "230.123456789012", "current": "0.000000015", "pf_in": "0.5"},
		{"voltage": "1e3", "current": "-0.5", "pf_in": "1", "fault": "1"},
		{"voltage": "0.1", "current": "0.2", "pf_in": "0.3333333333333333"},
		{"voltage": "5", "current": "0", "pf_in": "0.9"},
		{"current": "5", "pf_in": "0.9"},
		{"voltage": "5", "current": "2"},
		{"voltage": "NaN", "current": "1", "pf_in": "0.9"},
		{"voltage": "230", "current": "x", "pf_in": "0.9"},
		{"voltage": "230", "current": "10", "pf_in": "0.9", "real": "55"},
		{},
	}
}

// chainDraft changes the chain: different formulas mid-chain, metrics dropped, others added
// that read a metric the published version computed differently.
func chainDraft() model.ProfileResolution {
	return model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@draft"}, []*model.MetricDefinition{
		measured(1, "voltage", model.MetricDouble, "V"),
		measured(2, "current", model.MetricDouble, "A"),
		measured(3, "pf_in", model.MetricDouble, ""),
		measured(4, "fault", model.MetricInt, ""),
		calculatedDef(20, "apparent", model.MetricDouble, "VA", "voltage * current"),
		calculatedDef(21, "real", model.MetricDouble, "W", "apparent * pf_in"),
		calculatedDef(22, "loss", model.MetricDouble, "W", "apparent - real"),
		calculatedDef(23, "loss_pct", model.MetricDouble, "%", "has(m.loss) ? math.clamp(loss / apparent * 100.0, 0.0, 100.0) : -1.0"),
		calculatedDef(24, "tier", model.MetricInt, "", "loss_pct > 50.0 ? 2 : (loss_pct > 10.0 ? 1 : 0)"),
	})
}

// The parity contract, for chains: Recalculate(draft, resolve(published, reading)) ==
// resolve(draft, reading), entry for entry, over the whole corpus.
func TestRecalculateEqualsResolvingUnderTheDraftForChains(t *testing.T) {
	published, draft := chainVersion("meter@chain"), chainDraft()
	computed := 0
	for i, reading := range chainCorpus() {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			stored := resolveSamples(t, published, testCalcMetrics(), reading).Entries[0].Entries
			want := resolveSamples(t, draft, testCalcMetrics(), reading).Entries[0].Entries
			before := mustJSON(t, stored)
			obs := newCountingObserver()
			got := draft.Recalculate(stored, nil, obs)
			assert.Equal(t, want, got)
			assert.Equal(t, before, mustJSON(t, stored), "Recalculate wrote to the stored entries")
			computed += obs.computed["tier"] + obs.computed["loss_pct"]
		})
	}
	assert.Positive(t, computed, "the corpus never reaches the end of the draft's chain")
	// And a published chain recalculated under itself changes nothing.
	for _, reading := range chainCorpus() {
		stored := resolveSamples(t, published, testCalcMetrics(), reading).Entries[0].Entries
		assert.Equal(t, stored, published.Recalculate(stored, nil, newCountingObserver()))
	}
}

// A thousand resolutions of one reading are byte-identical, whatever Go does to map order.
func TestAChainResolvesDeterministically(t *testing.T) {
	profile := chainVersion("meter@chain")
	reading := chainCorpus()[1]
	first := mustJSON(t, resolveSamples(t, profile, testCalcMetrics(), reading))
	for i := 0; i < 1000; i++ {
		require.Equal(t, first, mustJSON(t, resolveSamples(t, profile, testCalcMetrics(), reading)), "run %d", i)
	}
}

// A published version the compiler would now refuse (a ring, a chain too deep: the backstop
// for a release that tightened the rules) skips the metrics that cannot run, loudly, and runs
// the rest.
func TestAPublishedCycleAndAnOverlongChainSkipLoudly(t *testing.T) {
	defs := []*model.MetricDefinition{
		measured(1, "voltage", model.MetricDouble, ""),
		calculatedDef(10, "a", model.MetricDouble, "", "b + voltage"),
		calculatedDef(11, "b", model.MetricDouble, "", "a + voltage"),
		calculatedDef(12, "ok", model.MetricDouble, "", "voltage + 1.0"),
	}
	prev := "voltage"
	for i := 1; i <= calcfield.MaxChainDepth+1; i++ {
		key := fmt.Sprintf("c%d", i)
		defs = append(defs, calculatedDef(uint(20+i), key, model.MetricDouble, "", prev+" + 1.0"))
		prev = key
	}
	profile := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@bad"}, defs)
	metrics := testCalcMetrics()
	got := resolveSamples(t, profile, metrics, map[string]string{"voltage": "0"})
	assert.Equal(t, []string{"c1*", "c2*", "c3*", "c4*", "ok*", "voltage"}, names(got.Entries[0].Entries))
	assert.EqualValues(t, 3, metrics.skips(calcfield.SkipEvalError), "a, b and c5")
}
