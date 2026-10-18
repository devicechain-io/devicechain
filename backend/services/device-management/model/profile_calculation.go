// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"slices"
	"sort"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
)

// Calculated metrics at resolve (ADR-082 decision 4): the compiled formulas of one published
// profile version, held by its ProfileResolution and run by the resolver against every
// sample entry of a measurement event.
//
// They are compiled in freeze, so once per cache fill (a decode included) and never per
// event, and they are as immutable as the rest of the resolution: a calcfield.Formula and
// its Env are safe for concurrent use, and the plan is written only while freeze builds it.
//
// 🔴 NO STATE, AND THAT IS A RED LINE. Nothing here, and nothing the resolver does with it,
// keeps a value between samples, devices or events: a formula is a pure function of ONE
// sample entry and of the profile version named on the event. A request for "the previous
// reading", "since the last value" or "the other message" is a request for a windowed
// derivation stage in event-processing, never for a cache here — a cache would make the
// stored value depend on which replica resolved the event and in what order, which is
// exactly what replay correctness rules out.

// calcPlan is a version's calculated metrics, compiled. nil on a resolution with none.
type calcPlan struct {
	// env is the compile environment of the version's MEASURED metrics: the same inputs,
	// by the same rule, as the publish gate's (newProfileFormulas). nil when they could not
	// be declared, in which case every formula below is broken.
	env *calcfield.Env
	// formulas are the calculated metrics, sorted by key, so the order values are appended
	// in (and so skips are reported in) does not depend on declaration order.
	formulas []calcFormula
	// constKeys are the device constants the version's formulas read, together: sorted,
	// distinct. nil for a version none of whose formulas reads one, which is what makes a
	// device's metadata untouched on the resolver's path for every such profile.
	constKeys []string
}

// calcFormula is one calculated metric: its compiled formula and what its value is stamped
// with.
type calcFormula struct {
	key string
	// formula is nil when the formula did not compile here, with broken saying why. The
	// publish gate compiled it with the same package, so this is a backstop (a release that
	// tightened the language under an already-published version); it is skipped loudly as
	// SkipEvalError on every sample, never computed from anything else.
	formula    *calcfield.Formula
	broken     error
	classifier uint64
	unit       *string
	dataType   string
}

// buildCalcPlan compiles the calculated metrics among metrics, and marks on each measured
// metric a formula reads its input slot. It runs inside freeze, before the resolution is
// shared, and is the only writer of inputSlot.
func buildCalcPlan(scope ProfileScope, metrics []ResolvedMetric) *calcPlan {
	var calculated []int
	for i := range metrics {
		if metrics[i].Calculated() {
			calculated = append(calculated, i)
		}
	}
	if len(calculated) == 0 {
		return nil
	}
	plan := &calcPlan{formulas: make([]calcFormula, 0, len(calculated))}
	env, envErr := calcfield.NewEnv(resolvedFormulaInputs(metrics))
	if envErr == nil {
		plan.env = env
	}
	for _, i := range calculated {
		m := &metrics[i]
		cf := calcFormula{key: m.m.MetricKey, classifier: uint64(m.m.ID), unit: m.m.Unit, dataType: m.m.DataType}
		switch {
		case envErr != nil:
			cf.broken = fmt.Errorf("the version's measured metrics cannot be declared as formula inputs: %w", envErr)
		default:
			cf.formula, cf.broken = compileResolved(env, m)
		}
		if cf.broken != nil {
			log.Error().Err(cf.broken).Str("profileVersion", scope.ProfileVersionToken).Str("metric", cf.key).
				Msg("A published calculated metric does not compile; it will produce no value for any reading of this version")
		}
		plan.formulas = append(plan.formulas, cf)
	}
	sort.Slice(plan.formulas, func(a, b int) bool { return plan.formulas[a].key < plan.formulas[b].key })
	for _, cf := range plan.formulas {
		if cf.formula != nil {
			plan.constKeys = append(plan.constKeys, cf.formula.ConstantKeys()...)
		}
	}
	if len(plan.constKeys) > 0 {
		sort.Strings(plan.constKeys)
		plan.constKeys = slices.Compact(plan.constKeys)
	}

	if plan.env != nil {
		for _, cf := range plan.formulas {
			if cf.formula == nil {
				continue
			}
			for _, key := range cf.formula.InputKeys() {
				pos, _ := plan.env.Index(key)
				// Every declaration of the key: the resolver reads a key's LAST one (byKey),
				// and marking all of them keeps that true whichever it is.
				for j := range metrics {
					if metrics[j].m.MetricKey == key && !metrics[j].Calculated() {
						metrics[j].inputSlot = pos + 1
					}
				}
			}
		}
	}
	return plan
}

// resolvedFormulaInputs is formulaInputs over a resolution's projection: the measured
// metrics a formula may read, the first declaration of each key, in declaration order.
func resolvedFormulaInputs(metrics []ResolvedMetric) []calcfield.Input {
	inputs := make([]calcfield.Input, 0, len(metrics))
	seen := make(map[string]bool, len(metrics))
	for i := range metrics {
		m := &metrics[i].m
		if metrics[i].Calculated() || m.MetricKey == "" || seen[m.MetricKey] {
			continue
		}
		t, ok := calcfield.ParseType(m.DataType)
		if !ok {
			continue
		}
		seen[m.MetricKey] = true
		inputs = append(inputs, calcfield.Input{Key: m.MetricKey, Type: t})
	}
	return inputs
}

// compileResolved compiles one calculated metric of a resolution.
func compileResolved(env *calcfield.Env, m *ResolvedMetric) (*calcfield.Formula, error) {
	t, ok := calcfield.ParseType(m.m.DataType)
	if !ok {
		return nil, fmt.Errorf("data type %s cannot be calculated", m.m.DataType)
	}
	return env.Compile(calcfield.Definition{Key: m.m.MetricKey, Expression: *m.m.Expression, Type: t,
		Min: m.m.MinValue, Max: m.m.MaxValue})
}

// CalculatedCount is how many calculated metrics the version declares. Zero — every
// profile without formulas — is the resolver's whole cost for the feature: one compare.
func (r ProfileResolution) CalculatedCount() int {
	if r.calc == nil {
		return 0
	}
	return len(r.calc.formulas)
}

// DeviceConstants reads, from a device's metadata, the numeric constants this version's
// formulas read as `device.name` (ADR-082, per-device constants). Call it once per EVENT, with
// the metadata of the device record the resolver already holds, and hand the result to
// BeginCalculation for every sample entry of the event.
//
// 🔴 It returns nil WITHOUT TOUCHING the metadata when the version reads no constant, which is
// every version that has no formulas and every one whose formulas read only measurements: the
// whole cost of the feature on those profiles is this one compare. The resolution caches
// nothing per device (the per-type cache is unchanged); the values come from the device
// record's own cache, evicted on every device write, and are frozen into each computed value
// when it is computed.
//
// Strict by construction, one outcome per name the formulas read: a JSON number is a constant;
// a key the metadata lacks is absent (a formula reading it is skipped as missing_input); a
// value that is not a finite number — a string, a boolean, an object, an array, null, or a
// number out of range — is present but unusable (invalid_input), never read as zero and never
// coerced from text. Of a key repeated in the document the last wins, as JSON decoders do.
// Metadata that is not a JSON object has no constants.
func (r ProfileResolution) DeviceConstants(metadata *datatypes.JSON) []calcfield.Constant {
	if r.calc == nil || len(r.calc.constKeys) == 0 || metadata == nil {
		return nil
	}
	return readConstants(*metadata, r.calc.constKeys)
}

// ConstantKeys returns the device constants this version's formulas read, sorted; empty when
// none. A copy.
func (r ProfileResolution) ConstantKeys() []string {
	if r.calc == nil {
		return nil
	}
	return slices.Clone(r.calc.constKeys)
}

// Calculation is one sample entry's evaluation of a version's calculated metrics: take one
// with BeginCalculation, Set each measured value the entry carries, Append the computed
// values, Release it. It is not safe for concurrent use; one resolver uses one at a time.
//
// It shares the resolution's compiled plan, which is immutable, and owns a pooled sample
// that nothing else holds, so it hands out nothing of the resolution that could be written.
type Calculation struct {
	plan   *calcPlan
	sample *calcfield.Sample
}

// BeginCalculation takes an empty Calculation for one sample entry. Call it only when
// CalculatedCount is not zero; on a resolution with none it returns one that appends
// nothing.
//
// constants are the device's (DeviceConstants), shared by every entry of the event and not
// copied: the caller must leave the slice alone until the last Release.
func (r ProfileResolution) BeginCalculation(constants []calcfield.Constant) Calculation {
	c := Calculation{plan: r.calc}
	if r.calc != nil && r.calc.env != nil {
		c.sample = r.calc.env.Sample()
		c.sample.UseConstants(constants)
	}
	return c
}

// Set records the raw value the sample entry carries for m, when a formula reads it. It is
// parsed once, as m's declared type; a value that does not parse makes the formulas that
// read it skip as invalid_input, never read as zero.
func (c Calculation) Set(m *ResolvedMetric, raw string) {
	if m.inputSlot == 0 || c.sample == nil {
		return
	}
	_ = c.sample.SetAt(m.inputSlot-1, raw) // a refused value is recorded on the sample
}

// CalculationObserver is told the outcome of every calculated metric of every sample.
type CalculationObserver interface {
	// Computed is a value produced and appended.
	Computed(key string)
	// Skipped is a metric that produced no value for the sample, and why. err is the
	// underlying error, when there is one, for a log line.
	Skipped(key string, reason calcfield.SkipReason, err error)
}

// Append evaluates every calculated metric against the values Set so far and appends each
// one that produced a value to dst as a Derived entry, stamped with its definition's
// classifier, unit and data type exactly as a device-sent value of it would have been. A
// metric that produced nothing is reported to obs and appends nothing: one missing value,
// never a plausible one and never a failed reading.
func (c Calculation) Append(dst []ResolvedMeasurementEntry, obs CalculationObserver) []ResolvedMeasurementEntry {
	if c.plan == nil {
		return dst
	}
	for i := range c.plan.formulas {
		cf := &c.plan.formulas[i]
		if cf.formula == nil {
			obs.Skipped(cf.key, calcfield.SkipEvalError, cf.broken)
			continue
		}
		out := cf.formula.Eval(c.sample)
		if !out.OK() {
			obs.Skipped(cf.key, out.Skip, out.Err)
			continue
		}
		// The entry's own copies of the stamp, in one allocation: an entry travels to every
		// consumer, so it must never point into the shared plan.
		st := &derivedStamp{classifier: cf.classifier, dataType: cf.dataType}
		entry := ResolvedMeasurementEntry{Name: cf.key, Value: out.Value, Classifier: &st.classifier,
			DataType: &st.dataType, Derived: true}
		if cf.unit != nil {
			st.unit = *cf.unit
			entry.Unit = &st.unit
		}
		dst = append(dst, entry)
		obs.Computed(cf.key)
	}
	return dst
}

// derivedStamp backs a derived entry's classifier, data type and unit pointers.
type derivedStamp struct {
	classifier     uint64
	dataType, unit string
}

// Release returns the Calculation's sample to its pool. The Calculation must not be used
// afterwards.
func (c Calculation) Release() {
	if c.sample != nil {
		c.sample.Release()
	}
}
