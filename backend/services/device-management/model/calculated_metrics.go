// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Calculated metrics (ADR-082): authoring and the publish gate.
//
// A calculated metric is a MetricDefinition with a formula over the other, measured
// metrics of the same reading. This file holds everything device-management decides about
// one BEFORE it runs: whether a profile may publish it (checkCalculatedMetrics) and what a
// formula gives for a hand-typed sample (TestMetricFormula). Both compile through the same
// core/calcfield code the resolver will use, so what the gate admits and what the test
// panel shows are what would run.
//
// What runs a published formula against a reading is the resolution (profile_calculation.go,
// compiled once per cached ProfileResolution) and the resolver that appends its values. It
// compiles with the same environment rule as newProfileFormulas below, so a formula this
// gate admits is one the resolver can run. Whether a tenant may publish any at all is its
// tier's calculatedMetricsPerProfile ceiling, zero on default tiers until the feature's
// cost has been measured.

// CalculatedMetricsCeilingResolver reads how many calculated metrics one of the tenant's
// profiles may publish: its tier's setting, else the platform default (zero). A narrow
// READ seam like GeoFenceCapsResolver, and like it the error travels: the ceiling is
// enforced at publish, where a wrong answer produces a durable version, and a tier may sit
// at zero, so there is no number to assume for a tenant whose ceiling cannot be read.
type CalculatedMetricsCeilingResolver interface {
	Resolve(ctx context.Context, tenant string) (int, error)
}

// calculatedMetricsCeiling resolves the calling tenant's ceiling. With no resolver wired (a
// unit test, an instance with no user-management coordinate) it is the platform default,
// zero: calculated metrics are off, which is the direction a governance limit must fail.
func (api *Api) calculatedMetricsCeiling(ctx context.Context) (int, error) {
	if api.CalculatedMetricsCeiling == nil {
		return governance.DefaultCalculatedMetricsPerProfile, nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return 0, errors.New("no tenant in context")
	}
	return api.CalculatedMetricsCeiling.Resolve(ctx, tenant)
}

// CalculatedMetricsCeilingError is the refusal of a profile carrying more calculated
// metrics than the tenant's tier allows. extensions.code LIMIT_EXCEEDED.
type CalculatedMetricsCeilingError struct {
	Count, Ceiling int
}

func (e *CalculatedMetricsCeilingError) Error() string {
	if e.Ceiling == 0 {
		return fmt.Sprintf("cannot publish device profile: it has %d calculated metric(s), and calculated "+
			"metrics are not enabled for this tenant's tier", e.Count)
	}
	return fmt.Sprintf("cannot publish device profile: it has %d calculated metrics, and this tenant's tier "+
		"allows at most %d per profile", e.Count, e.Ceiling)
}

// Extensions gives the refusal its wire code.
func (e *CalculatedMetricsCeilingError) Extensions() map[string]any {
	return map[string]any{"code": limit.Code}
}

// profileFormulas is one profile snapshot's metrics, split the way a formula sees them:
// the MEASURED metrics are the inputs a formula reads, and the calculated ones are what is
// compiled, together, into a calcfield.Plan. A calculated metric may read another one
// (ADR-082, chaining), so the plan also refuses a cycle and a chain deeper than
// calcfield.MaxChainDepth.
type profileFormulas struct {
	all        []*MetricDefinition
	measured   []*MetricDefinition
	calculated []*MetricDefinition // sorted by key, so problems are reported in a stable order
	duplicate  []string            // keys declared more than once, sorted
}

// newProfileFormulas splits metrics. A measured metric whose type a formula cannot read is
// simply not an input.
func newProfileFormulas(metrics []*MetricDefinition) *profileFormulas {
	p := &profileFormulas{all: metrics}
	seen := map[string]int{}
	for _, m := range metrics {
		seen[m.MetricKey]++
		if m.Calculated() {
			p.calculated = append(p.calculated, m)
		} else {
			p.measured = append(p.measured, m)
		}
	}
	for k, n := range seen {
		if n > 1 {
			p.duplicate = append(p.duplicate, k)
		}
	}
	sort.Strings(p.duplicate)
	sort.Slice(p.calculated, func(i, j int) bool { return p.calculated[i].MetricKey < p.calculated[j].MetricKey })
	return p
}

// formulaInputs is the calcfield input list for metrics, skipping any whose type is not a
// formula type and the key except (so a formula's own key is never its input).
func formulaInputs(metrics []*MetricDefinition, except string) []calcfield.Input {
	inputs := make([]calcfield.Input, 0, len(metrics))
	seen := map[string]bool{}
	for _, m := range metrics {
		t, ok := calcfield.ParseType(m.DataType)
		if !ok || m.MetricKey == except || m.MetricKey == "" || seen[m.MetricKey] {
			continue
		}
		seen[m.MetricKey] = true
		inputs = append(inputs, calcfield.Input{Key: m.MetricKey, Type: t})
	}
	return inputs
}

// plan compiles definitions against the measured metrics, as the resolver compiles a
// published version's (buildCalcPlan): the same rule, so a formula this gate admits is one
// the resolver can run. The error is for measured metrics that cannot be declared as inputs.
func (p *profileFormulas) plan(defs []calcfield.Definition) (*calcfield.Plan, error) {
	plan, err := calcfield.NewPlan(formulaInputs(p.measured, ""), defs)
	if err != nil {
		return nil, fmt.Errorf("the profile's measured metrics cannot be declared as formula inputs: %w", err)
	}
	return plan, nil
}

// planProblems is why each metric of plan that cannot run cannot, in key order, leaving out
// the keys in skip (already reported another way).
func planProblems(plan *calcfield.Plan, skip map[string]bool) []string {
	var failed []*calcfield.Step
	for i := 0; i < plan.Len(); i++ {
		if s := plan.Step(i); s.Err != nil && !skip[s.Key] {
			failed = append(failed, s)
		}
	}
	sort.Slice(failed, func(a, b int) bool { return failed[a].Key < failed[b].Key })
	// A ring is one problem however many metrics are on it.
	out := make([]string, 0, len(failed))
	for _, s := range failed {
		if msg := s.Err.Error(); !slices.Contains(out, msg) {
			out = append(out, msg)
		}
	}
	return out
}

// hasKey reports whether any metric of the profile has key.
func (p *profileFormulas) hasKey(key string) bool {
	for _, m := range p.all {
		if m.MetricKey == key {
			return true
		}
	}
	return false
}

// measuredInputsOf is the measured metrics f and the formulas it depends on read, in the
// plan's input order.
func measuredInputsOf(plan *calcfield.Plan, f *calcfield.Formula, upstream []*calcfield.Formula) []string {
	read := map[string]bool{}
	for _, g := range append([]*calcfield.Formula{f}, upstream...) {
		for _, k := range g.InputKeys() {
			if !plan.Env().IsCalculated(k) {
				read[k] = true
			}
		}
	}
	out := make([]string, 0, len(read))
	for _, in := range plan.Env().Inputs() {
		if read[in.Key] {
			out = append(out, in.Key)
		}
	}
	return out
}

// upstreamFormulas is the formulas f reads, directly or through others, in evaluation order.
func upstreamFormulas(plan *calcfield.Plan, f *calcfield.Formula) []*calcfield.Formula {
	byKey := make(map[string]*calcfield.Formula, plan.Len())
	for _, g := range plan.Formulas() {
		byKey[g.Key()] = g
	}
	seen := map[string]*calcfield.Formula{}
	var walk func(g *calcfield.Formula)
	walk = func(g *calcfield.Formula) {
		for _, k := range g.InputKeys() {
			if u, ok := byKey[k]; ok && seen[k] == nil {
				seen[k] = u
				walk(u)
			}
		}
	}
	walk(f)
	out := make([]*calcfield.Formula, 0, len(seen))
	for _, g := range plan.Formulas() { // evaluation order
		if seen[g.Key()] != nil {
			out = append(out, g)
		}
	}
	return out
}

// describeFormulaError renders a compile refusal for the author. In the publish gate the
// metric's key leads the message, since several formulas are reported together; the test
// panel is about one formula, which may not have a key yet, so it gets the reason alone.
func describeFormulaError(err error, withKey bool) string {
	if withKey {
		return err.Error()
	}
	var ce *calcfield.CompileError
	if errors.As(err, &ce) {
		return ce.Err.Error()
	}
	var cost *calcfield.CostError
	if errors.As(err, &cost) {
		return fmt.Sprintf("the formula's estimated cost %d is above the ceiling of %d", cost.EstimatedMax, cost.Ceiling)
	}
	return err.Error()
}

// definitionOf is the calcfield view of a calculated metric, or an error naming why it has
// none (a type a formula cannot produce).
func definitionOf(m *MetricDefinition) (calcfield.Definition, error) {
	t, ok := calcfield.ParseType(m.DataType)
	if !ok {
		return calcfield.Definition{}, fmt.Errorf("calculated metric %q: data type %s cannot be calculated; "+
			"use DOUBLE, INT or BOOLEAN", m.MetricKey, m.DataType)
	}
	def := calcfield.Definition{Key: m.MetricKey, Expression: m.Expression.String, Type: t}
	if m.MinValue.Valid {
		v := m.MinValue.Float64
		def.Min = &v
	}
	if m.MaxValue.Valid {
		v := m.MaxValue.Float64
		def.Max = &v
	}
	return def, nil
}

// problems runs publish-gate items 1–3, 4 (per formula and per profile), 5 (size; the
// count is checked by the caller against the ceiling) and 6, returning one line per
// problem for the author. It returns every problem, not the first: an author fixing a
// profile with three broken formulas should not need three publish attempts to find them.
func (p *profileFormulas) problems() []string {
	var out []string
	for _, k := range p.duplicate {
		out = append(out, fmt.Sprintf("metric key %q is declared more than once", k))
	}
	defs := make([]calcfield.Definition, 0, len(p.calculated))
	// reported are the metrics already refused below, whose compile error would only repeat it.
	reported := map[string]bool{}
	for _, m := range p.calculated {
		def, err := definitionOf(m)
		if err != nil {
			out = append(out, err.Error())
			reported[m.MetricKey] = true
			continue
		}
		defs = append(defs, def)
		// The same reading Resolved gives an allowed-values list: only a non-empty array of
		// strings restricts anything, so only that is a problem here.
		var allowed []string
		if m.Enum != nil && json.Unmarshal(*m.Enum, &allowed) == nil && len(allowed) > 0 {
			out = append(out, fmt.Sprintf("calculated metric %q declares allowed values; a calculated metric "+
				"cannot restrict its values with a list (use minValue and maxValue)", m.MetricKey))
			reported[m.MetricKey] = true
			continue
		}
		if def.Min != nil && def.Max != nil && *def.Min > *def.Max {
			out = append(out, fmt.Sprintf("calculated metric %q: minValue %v is above maxValue %v, so no value "+
				"could ever be stored", m.MetricKey, *def.Min, *def.Max))
			reported[m.MetricKey] = true
		}
	}
	plan, err := p.plan(defs)
	if err != nil {
		return append(out, err.Error())
	}
	out = append(out, planProblems(plan, reported)...)
	compiled := plan.Formulas()
	if err := calcfield.CheckProfileCost(compiled); err != nil {
		out = append(out, err.Error())
	}
	if err := calcfield.CheckProfileConstants(compiled); err != nil {
		out = append(out, err.Error())
	}
	return out
}

// checkCalculatedMetrics is the publish gate for calculated metrics (ADR-082, technical
// spec §4). It parses the EXACT snapshot bytes the publish is about to freeze, like the
// detection-rule gate, so what is checked is what would be frozen. A profile with no
// calculated metric pays one parse and reads no ceiling.
func (api *Api) checkCalculatedMetrics(ctx context.Context, snapshot datatypes.JSON) error {
	snap, err := parseProfileSnapshot(snapshot)
	if err != nil {
		return err
	}
	p := newProfileFormulas(snap.Metrics)
	if len(p.calculated) == 0 {
		return nil
	}
	// The platform maximum first: it bounds how much compiling the rest of the gate does.
	if n := len(p.calculated); n > governance.MaxCalculatedMetricsPerProfile {
		return &CalculatedMetricsCeilingError{Count: n, Ceiling: governance.MaxCalculatedMetricsPerProfile}
	}
	if problems := p.problems(); len(problems) > 0 {
		return &InvalidArgumentError{Msg: fmt.Sprintf("cannot publish device profile: %d calculated metric "+
			"problem(s): %s", len(problems), strings.Join(problems, "; "))}
	}
	ceiling, err := api.calculatedMetricsCeiling(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Calculated-metrics ceiling could not be resolved; refusing profile publish.")
		return errors.New("cannot publish device profile: this tenant's calculated-metrics ceiling could not " +
			"be read, and there is no safe number to assume")
	}
	if n := len(p.calculated); n > ceiling {
		return &CalculatedMetricsCeilingError{Count: n, Ceiling: ceiling}
	}
	return nil
}

// MetricValueInput is one input value of a hand-typed sample.
type MetricValueInput struct {
	Key   string
	Value string
}

// MetricFormulaTestRequest is a formula and a sample to evaluate it against, under a
// profile's DRAFT measured metrics.
type MetricFormulaTestRequest struct {
	ProfileToken string
	Expression   string
	DataType     string
	// MetricKey is the calculated metric's key, when the formula belongs to one. It is
	// used for the advisory warnings and to refuse a formula reading its own key.
	MetricKey *string
	MinValue  *float64
	MaxValue  *float64
	Sample    []MetricValueInput
	// Constants are the hand-typed per-device constants the formula reads as `device.name`:
	// the numbers a device's metadata would hold. One the formula does not read is ignored
	// with a warning.
	Constants []MetricValueInput
}

// MetricFormulaTestResult is what a formula gives for the sample, through the same
// compiler and evaluator the resolver will use. A formula that does not compile has a
// CompileError and nothing else; one that compiles has exactly one of Value or
// SkippedReason.
type MetricFormulaTestResult struct {
	CompileError  *string
	Value         *string
	SkippedReason *string
	Inputs        []string
	// Chain is the calculated metrics the formula depends on, directly or through others, in
	// evaluation order; MeasuredInputs every measured metric the evaluation reads (the formula's
	// own and the chain's), in the profile's input order.
	Chain          []string
	MeasuredInputs []string
	Constants      []string
	Warnings       []string
	EstimatedCost  *int
}

// storedMeasurementMagnitude is the bound on a stored measurement value: event storage
// keeps a measurement as decimal(20,8), 12 digits before the point. A result at or beyond
// it is skipped as unstorable by the evaluator itself, whatever minValue/maxValue say.
const storedMeasurementMagnitude = calcfield.StoredMagnitude

// TestMetricFormula compiles a formula against a profile's draft measured metrics and
// evaluates it against a hand-typed sample. Nothing is stored. It is the authoring loop's
// zero-history fallback: it needs no device and no stored reading.
func (api *Api) TestMetricFormula(ctx context.Context, request *MetricFormulaTestRequest) (*MetricFormulaTestResult, error) {
	if len(request.Sample) > calcfield.MaxInputs {
		return nil, limit.Exceeded("sample values", len(request.Sample), calcfield.MaxInputs)
	}
	if len(request.Constants) > calcfield.MaxProfileConstants {
		return nil, limit.Exceeded("constant values", len(request.Constants), calcfield.MaxProfileConstants)
	}
	profile, err := api.deviceProfileByToken(ctx, request.ProfileToken)
	if err != nil {
		return nil, err
	}
	metrics, err := api.MetricDefinitionsByDeviceProfile(ctx, profile.ID)
	if err != nil {
		return nil, err
	}
	key := ""
	if request.MetricKey != nil {
		key = *request.MetricKey
	}
	// The formula under test replaces the draft's own formula for its key, if any.
	drafted := make([]*MetricDefinition, 0, len(metrics))
	for _, m := range metrics {
		if key != "" && m.MetricKey == key && m.Calculated() {
			continue
		}
		drafted = append(drafted, m)
	}
	p := newProfileFormulas(drafted)
	result := &MetricFormulaTestResult{Inputs: []string{}, Chain: []string{}, MeasuredInputs: []string{},
		Constants: []string{}, Warnings: []string{}}
	refuse := func(msg string) (*MetricFormulaTestResult, error) {
		result.CompileError = &msg
		return result, nil
	}
	// The formula is planned with every other calculated metric of the draft, so the platform
	// maximum bounds how much one request compiles, as it bounds the publish gate's. A draft
	// above it could not be published either.
	if n := len(p.calculated) + 1; n > governance.MaxCalculatedMetricsPerProfile {
		return refuse(fmt.Sprintf("the draft would have %d calculated metrics with this one, above the maximum of %d",
			n, governance.MaxCalculatedMetricsPerProfile))
	}
	t, ok := calcfield.ParseType(request.DataType)
	if !ok {
		return refuse(fmt.Sprintf("data type %s cannot be calculated; use DOUBLE, INT or BOOLEAN", request.DataType))
	}
	if request.MinValue != nil && request.MaxValue != nil && *request.MinValue > *request.MaxValue {
		return refuse(fmt.Sprintf("minValue %v is above maxValue %v, so no value could ever be stored",
			*request.MinValue, *request.MaxValue))
	}
	// The formula is compiled WITH the draft's other calculated metrics, as one plan, so it may
	// read them (chaining) and a cycle or a chain too deep is refused here as the publish gate
	// would. A formula with no key yet is planned under a name no metric has.
	testKey := key
	if testKey == "" {
		testKey = "_formula_under_test"
		for p.hasKey(testKey) {
			testKey += "_"
		}
	}
	defs := make([]calcfield.Definition, 0, len(p.calculated)+1)
	for _, m := range p.calculated {
		if def, err := definitionOf(m); err == nil {
			defs = append(defs, def)
		}
	}
	defs = append(defs, calcfield.Definition{Key: testKey, Expression: request.Expression, Type: t,
		Min: request.MinValue, Max: request.MaxValue})
	plan, err := p.plan(defs)
	if err != nil {
		return refuse(err.Error())
	}
	at := -1
	for i := 0; i < plan.Len(); i++ {
		if plan.Step(i).Key == testKey {
			at = i
		}
	}
	if at < 0 {
		return refuse(fmt.Sprintf("the formula's key %q cannot be planned", key))
	}
	f := plan.Step(at).Formula
	if f == nil {
		var deep *calcfield.DepthError
		if errors.As(plan.Step(at).Err, &deep) {
			// Named from this formula, which may not have a key yet.
			chain := slices.Clone(deep.Chain)
			chain[0] = "this formula"
			return refuse(fmt.Sprintf("the formula ends a chain of %d calculated metrics, %s (each reads the next), "+
				"above the maximum of %d", deep.Depth, strings.Join(chain, " -> "), calcfield.MaxChainDepth))
		}
		return refuse(strings.ReplaceAll(describeFormulaError(plan.Step(at).Err, false), `"`+testKey+`"`, "this formula"))
	}
	result.Inputs = f.InputKeys()
	// The constants the formula's whole chain reads: the values the evaluation below needs.
	upstream := upstreamFormulas(plan, f)
	result.MeasuredInputs = measuredInputsOf(plan, f, upstream)
	for _, u := range upstream {
		result.Chain = append(result.Chain, u.Key())
	}
	result.Constants = f.ConstantKeys()
	for _, u := range upstream {
		result.Constants = append(result.Constants, u.ConstantKeys()...)
	}
	sort.Strings(result.Constants)
	result.Constants = slices.Compact(result.Constants)
	cost := int(f.CostMax())
	result.EstimatedCost = &cost

	sample := plan.Env().Sample()
	defer sample.Release()
	seen := make(map[string]bool, len(request.Sample))
	for _, v := range request.Sample {
		if seen[v.Key] {
			return nil, &InvalidArgumentError{Msg: fmt.Sprintf("sample names %q more than once", v.Key)}
		}
		seen[v.Key] = true
		if plan.Env().IsCalculated(v.Key) {
			result.Warnings = append(result.Warnings, fmt.Sprintf("the sample value for %q is ignored: it is a "+
				"calculated metric, computed here from its own formula as it is when a reading is resolved", v.Key))
		} else if isInput, _ := sample.Set(v.Key, v.Value); !isInput {
			result.Warnings = append(result.Warnings, fmt.Sprintf("the sample value for %q is ignored: it is "+
				"not a measured metric of this profile, so a formula cannot read it", v.Key))
		}
	}
	constants := make([]calcfield.Constant, 0, len(request.Constants))
	seenConstant := make(map[string]bool, len(request.Constants))
	for _, v := range request.Constants {
		if seenConstant[v.Key] {
			return nil, &InvalidArgumentError{Msg: fmt.Sprintf("constants name %q more than once", v.Key)}
		}
		seenConstant[v.Key] = true
		if !slices.Contains(result.Constants, v.Key) {
			who := "the formula does not"
			if len(upstream) > 0 {
				who = "neither the formula nor the calculated metrics it reads"
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("the constant value for %q is ignored: %s read device.%s",
				v.Key, who, v.Key))
			continue
		}
		constants = append(constants, calcfield.ConstantFromString(v.Key, v.Value))
	}
	sample.UseConstants(constants)
	// Every metric up to the formula under test, in the order the resolver evaluates them, so
	// an upstream calculated metric has the value it would be stored with when this one reads it.
	var out calcfield.Result
	for i := 0; i <= at; i++ {
		r := plan.Eval(sample, i)
		if i == at {
			out = r
		}
	}
	if len(upstream) > 0 {
		names := make([]string, len(upstream))
		for i, u := range upstream {
			names[i] = u.Key()
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("the formula reads the calculated metric(s) %s: "+
			"each is computed here from the sample through its own formula, in dependency order, and is read "+
			"as the value the reading would carry (a chain may be at most %d calculated metrics deep); a metric "+
			"that produces no value in this sample leaves the formulas that read it without that input",
			strings.Join(names, ", "), calcfield.MaxChainDepth))
	}
	if len(result.Constants) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("the formula reads the device constants %s: each is "+
			"read from the device's metadata when a reading is resolved and must be a JSON number there; a device "+
			"that lacks one (missing_input) or holds something else (invalid_input) gets no value for that reading, "+
			"and a value computed from a constant keeps the constant as it was then, whatever the device is "+
			"later edited to", strings.Join(result.Constants, ", ")))
	}
	if out.OK() {
		result.Value = &out.Value
	} else {
		reason := string(out.Skip)
		result.SkippedReason = &reason
	}

	warnings, err := api.formulaWarnings(ctx, profile.ID, key, t, request.MinValue, request.MaxValue, f.InputKeys(), p)
	if err != nil {
		return nil, err
	}
	result.Warnings = append(result.Warnings, warnings...)
	return result, nil
}

// formulaWarnings are the ADVISORY findings about a formula (technical spec §4.7): true
// things an author should know before publishing, none of which blocks a publish.
//
// Only the ones device-management can see are here. The two that need stored history —
// that devices recently sent the calculated key themselves, and that the inputs rarely
// arrive in the same reading — need event storage and are not computed in this release.
func (api *Api) formulaWarnings(ctx context.Context, profileId uint, key string, t calcfield.Type,
	min, max *float64, inputs []string, p *profileFormulas) ([]string, error) {
	var out []string
	if key != "" {
		active, err := api.activeProfileSnapshot(ctx, profileId)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if active != nil {
			for _, m := range active.Metrics {
				if m.MetricKey == key && !m.Calculated() {
					out = append(out, fmt.Sprintf("the published version declares %q as a measured metric, so "+
						"devices may be sending it; once %q is calculated, a value a device sends under that key "+
						"will be dropped in favour of the computed one", key, key))
				}
			}
		}
	}
	if t != calcfield.Boolean {
		bounded := min != nil && max != nil && *min > -storedMeasurementMagnitude && *max < storedMeasurementMagnitude
		if !bounded {
			out = append(out, "measurements are stored with at most 12 digits before the decimal point and 8 "+
				"after it: a result of magnitude 1e12 or more is skipped as unstorable, and a stored value is "+
				"rounded to 8 decimal places; set minValue and maxValue inside ±999999999999 to say what range "+
				"the metric really has")
		}
	}
	var withUnits []string
	for _, k := range inputs {
		for _, m := range p.measured {
			if m.MetricKey == k && m.Unit.Valid && m.Unit.String != "" {
				withUnits = append(withUnits, fmt.Sprintf("%s (%s)", k, m.Unit.String))
			}
		}
	}
	if len(withUnits) > 0 {
		out = append(out, fmt.Sprintf("units are not converted or checked: the formula reads %s as plain "+
			"numbers, and the result carries only the unit declared on the calculated metric",
			strings.Join(withUnits, ", ")))
	}
	return out, nil
}
