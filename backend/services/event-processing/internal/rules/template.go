// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A payload template (ADR-060) is a CEL expression that renders the body of an outbound connector
// action (httpCall / publish) against a fired detection. It shares the guard's derived-event
// vocabulary and cost-gate posture — the ONLY difference is the output type: a template evaluates
// to a STRING (the rendered payload bytes), where a guard evaluates to a boolean.
//
// The determinism boundary holds exactly as it does for a guard: a template is a pure,
// side-effect-free, stateless per-message function of the derived event, cost-gated at publish and
// bounded at runtime, so REACT can render it once on the at-least-once dispatch path and a
// redelivery renders the same bytes. CEL only — no JS, no host callbacks (ADR-053/056).
//
// v1 requires string output. Building JSON by CEL string construction is injection-safe here
// because the whole input vocabulary is constrained (value/hasValue are numeric/bool; series is an
// ADR-042 grammar-bounded token — it cannot contain a quote). A structured map-output mode is a
// future additive enhancement (the console/canvas generates the string-building expression, so an
// author never hand-writes raw CEL — slice C5).
package rules

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
)

// templateCostCeilingBackstop is the runtime CostLimit stamped on a dispatch-built template
// program — the runaway backstop, mirroring guardCostCeilingBackstop. The AUTHORITATIVE gate is the
// publish-time EstimateCost in CompileTemplate against the per-tenant ceiling.
//
// INVARIANT (shared with guard.go): this fixed backstop must stay >= any per-tenant
// PredicateCostCeiling. Today PredicateCostCeiling is the hardcoded default 100 (no override wiring
// yet), so 1000 is ample headroom. When the ADR-023 per-tenant-override slice lets an operator
// raise a tenant's ceiling ABOVE 1000, this must become ceiling-derived (as the DETECT predicate
// already stamps CostLimit(costCeiling)) — otherwise a template/guard that PASSES publish (estimate
// <= a >1000 ceiling) would trip this fixed backstop and fail at every dispatch.
const templateCostCeilingBackstop uint64 = 1_000

// templateConvMaxLen bounds the estimated output length of a scalar→string conversion (string(value)
// / string(hasValue)). A finite double stringifies to at most ~24 chars, a bool to 5; 64 is a safe
// cap. Without this bound cel-go's estimator treats string(double) as UNBOUNDED (max-uint), so any
// template that renders a numeric value would blow past every cost ceiling and be rejected at
// publish — this is what makes a numeric payload template gateable at all.
const templateConvMaxLen = 64

// templateEstimator is the cost/size estimator CompileTemplate gates against. It bounds the series
// string like the guard estimator AND bounds the result size of a `string(...)` conversion so a
// template that stringifies the scalar value/hasValue has a finite, decidable worst-case cost.
type templateEstimator struct{}

func (templateEstimator) EstimateSize(node checker.AstNode) *checker.SizeEstimate {
	path := node.Path()
	if len(path) > 0 && path[0] == GuardVarSeries {
		return &checker.SizeEstimate{Min: 0, Max: guardSeriesMaxLen}
	}
	return nil
}

func (templateEstimator) EstimateCallCost(function, overloadID string, target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	// Bound a type→string conversion's result size (cel-go leaves it unbounded), so a downstream
	// concatenation of a rendered scalar has a finite estimated cost.
	if function == "string" {
		return &checker.CallEstimate{
			CostEstimate: checker.CostEstimate{Min: 1, Max: 1},
			ResultSize:   &checker.SizeEstimate{Min: 0, Max: templateConvMaxLen},
		}
	}
	return nil
}

// CompileTemplate parses, type-checks, and cost-gates a payload-template CEL string against the
// REACT derived-event environment (the same env a guard uses), requiring STRING output. It is the
// publish-time gate, mirroring CompileGuard: a parse/type error, a non-string result, or a
// worst-case cost above the ceiling rejects the rule (fail-closed, so a runaway template never
// reaches the dispatcher). costCeiling is the per-tenant ceiling the caller resolves (never
// zero/"unlimited" — the ADR-023 fail-safe). Returns the estimated worst-case cost.
func CompileTemplate(source string, costCeiling uint64) (costMax uint64, err error) {
	env, err := GuardEnv()
	if err != nil {
		return 0, err
	}
	ast, iss := env.Compile(source)
	if iss != nil && iss.Err() != nil {
		return 0, fmt.Errorf("template: %w", iss.Err())
	}
	if ast.OutputType() != cel.StringType {
		return 0, fmt.Errorf("template must evaluate to a string, got %s", ast.OutputType())
	}
	est, err := env.EstimateCost(ast, templateEstimator{})
	if err != nil {
		return 0, fmt.Errorf("template: estimate cost: %w", err)
	}
	if est.Max > costCeiling {
		return 0, fmt.Errorf("template worst-case cost %d exceeds the ceiling %d", est.Max, costCeiling)
	}
	return est.Max, nil
}

// CompiledTemplate is a built, evaluable payload-template program producing a string. It wraps the
// cel.Program so cel-go stays contained in this package (the REACT dispatcher renders templates
// without importing cel). Safe for concurrent Eval.
type CompiledTemplate struct {
	program cel.Program
}

// BuildTemplateProgram builds an evaluable template WITHOUT the publish-time cost gate — for the
// REACT dispatcher, which re-derives a template from the durable rule projection per dispatch and
// only needs to render it (the tenant ceiling already gated it at publish). It still type-checks,
// requires string output, and stamps a generous runtime CostLimit backstop, so a forged/hand-edited
// non-string or runaway template is still rejected/bounded rather than trusted.
func BuildTemplateProgram(source string) (*CompiledTemplate, error) {
	env, err := GuardEnv()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(source)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("template: %w", iss.Err())
	}
	if ast.OutputType() != cel.StringType {
		return nil, fmt.Errorf("template must evaluate to a string, got %s", ast.OutputType())
	}
	program, err := env.Program(ast, cel.CostLimit(templateCostCeilingBackstop))
	if err != nil {
		return nil, fmt.Errorf("template: build program: %w", err)
	}
	return &CompiledTemplate{program: program}, nil
}

// Eval renders the template against one derived event, returning the payload string. It binds the
// same value/hasValue/series vocabulary as a guard (GuardInput): a nil Value binds value=0.0 and
// hasValue=false. An evaluation error (e.g. the runtime cost limit tripped) is returned; the
// dispatcher fails the action closed on a render error rather than sending an empty/partial body.
func (t *CompiledTemplate) Eval(in GuardInput) (string, error) {
	var v float64
	has := in.Value != nil
	if has {
		v = *in.Value
	}
	out, _, err := t.program.Eval(map[string]any{
		GuardVarValue:    v,
		GuardVarHasValue: has,
		GuardVarSeries:   in.Series,
	})
	if err != nil {
		return "", err
	}
	s, ok := out.Value().(string)
	if !ok {
		// The env guarantees string output at build; unreachable belt-and-braces.
		return "", fmt.Errorf("template produced a non-string %T at runtime", out.Value())
	}
	return s, nil
}
