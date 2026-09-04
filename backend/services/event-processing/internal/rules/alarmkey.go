// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An alarm-key template (ADR-041 dec 3 / ADR-057) is a CEL expression that renders the KEY a
// raiseAlarm action's alarm is filed under, instead of the static AlarmKey the author typed. It is
// the same machinery as a connector payload template (template.go) — publish-time cost gate,
// dispatch-time build with a runtime backstop — with two deliberate differences, both forced by
// where the rendered string ends up.
//
// 🔴 1. THE ENVIRONMENT IS DELIBERATELY NARROWER THAN A GUARD'S: `series` ONLY, no `value` /
// `hasValue`. A raiseAlarm action is dispatched on BOTH edges — the rising edge adds this rule's
// contribution to the (device, alarmKey) alarm, the falling edge removes it — and the two edges are
// separate messages, possibly on separate replicas. The alarm key is what pairs them. `value` is
// NOT edge-stable: a rising edge carries the crossing sample, a falling edge usually carries no
// value at all (it binds 0.0), so a key built from `value` would render one string on the raise and
// a DIFFERENT one on the resolve. The resolve would then land on a stranger alarm — tombstoning a
// contributor that was never raised there — while the raised alarm holds ACTIVE with nothing left
// that can ever clear it. That is a permanent strand, and no runtime check can catch it (each edge
// on its own looks perfectly well-formed). Restricting the vocabulary to the edge-stable `series`
// makes the failure UNREPRESENTABLE rather than merely documented: `value` is an undeclared
// identifier here, so an author is told at publish, by the type checker, in their own editor.
//
// 🔴 2. THE RENDERED OUTPUT IS RE-VALIDATED, not just produced. A static AlarmKey is checked against
// the ADR-042 token grammar at publish (validateAction); a rendered one is not author-controlled, so
// the same grammar is enforced on the RESULT at dispatch — see CompiledAlarmKeyTemplate.Eval. The
// key is stored in a `varchar(128) not null` column and is spliced into the alarm's identity, so an
// over-long or metacharacter-bearing key is a write error that redelivers to poison, not a cosmetic
// defect.
//
// The determinism boundary is unchanged: a pure, stateless, side-effect-free function of one
// edge-stable scalar, so a redelivery renders the same key. CEL only — no JS (ADR-053/056).
package rules

import (
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
)

// alarmKeyCostCeilingBackstop is the runtime CostLimit stamped on a dispatch-built alarm-key
// program — the runaway backstop, mirroring guardCostCeilingBackstop / templateCostCeilingBackstop
// and carrying the same invariant: it must stay >= any per-tenant PredicateCostCeiling, or a
// template that PASSES publish would trip this at every dispatch.
const alarmKeyCostCeilingBackstop uint64 = 1_000

var (
	alarmKeyEnvOnce sync.Once
	alarmKeyEnvVal  *cel.Env
	alarmKeyEnvErr  error
)

// AlarmKeyEnv returns the process-wide shared CEL environment an alarm-key template is compiled
// against. It declares ONLY GuardVarSeries — see the package comment for why value/hasValue are
// excluded rather than merely discouraged. Built once and reused; fails closed on a construction
// error rather than yielding a half-built env.
func AlarmKeyEnv() (*cel.Env, error) {
	alarmKeyEnvOnce.Do(func() {
		alarmKeyEnvVal, alarmKeyEnvErr = cel.NewEnv(
			cel.Variable(GuardVarSeries, cel.StringType),
			// cel.bind, for the same reason the guard env carries it: the canvas compiler folds a
			// named compute into an expression as a real binding rather than by text interpolation.
			ext.Bindings(),
		)
	})
	if alarmKeyEnvErr != nil {
		return nil, fmt.Errorf("build alarm-key environment: %w", alarmKeyEnvErr)
	}
	return alarmKeyEnvVal, nil
}

// CompileAlarmKeyTemplate parses, type-checks and cost-gates an alarm-key template at publish,
// requiring STRING output — the authoritative gate, mirroring CompileTemplate. A parse/type error
// (including a reference to value/hasValue, which are undeclared in this env), a non-string result,
// or a worst-case cost above the tenant ceiling rejects the rule. costCeiling is the per-tenant
// ceiling the caller resolves (never zero/"unlimited" — the ADR-023 fail-safe).
//
// It does NOT and cannot check that the rendered key will satisfy the token grammar: the output
// depends on the series, which is not known until dispatch. That check is Eval's (fail closed).
func CompileAlarmKeyTemplate(source string, costCeiling uint64) (costMax uint64, err error) {
	env, err := AlarmKeyEnv()
	if err != nil {
		return 0, err
	}
	ast, iss := env.Compile(source)
	if iss != nil && iss.Err() != nil {
		return 0, fmt.Errorf("alarm-key template: %w", iss.Err())
	}
	if ast.OutputType() != cel.StringType {
		return 0, fmt.Errorf("alarm-key template must evaluate to a string, got %s", ast.OutputType())
	}
	// The SAME estimator the payload template uses: it bounds the series string and the result size
	// of a `string(...)` conversion, which is what makes a concatenating template's worst case finite.
	est, err := env.EstimateCost(ast, templateEstimator{})
	if err != nil {
		return 0, fmt.Errorf("alarm-key template: estimate cost: %w", err)
	}
	if est.Max > costCeiling {
		return 0, fmt.Errorf("alarm-key template worst-case cost %d exceeds the ceiling %d", est.Max, costCeiling)
	}
	return est.Max, nil
}

// CompiledAlarmKeyTemplate is a built, evaluable alarm-key program. It wraps the cel.Program so
// cel-go stays contained in this package. Safe for concurrent Eval.
type CompiledAlarmKeyTemplate struct {
	program cel.Program
}

// BuildAlarmKeyTemplateProgram builds an evaluable alarm-key template WITHOUT the publish-time cost
// gate — for the REACT dispatcher, which re-derives the template from the durable rule projection
// per dispatch (the tenant ceiling already gated it at publish). It still type-checks, requires
// string output, and stamps a runtime CostLimit backstop, so a forged/hand-edited definition is
// still rejected or bounded rather than trusted.
func BuildAlarmKeyTemplateProgram(source string) (*CompiledAlarmKeyTemplate, error) {
	env, err := AlarmKeyEnv()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(source)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("alarm-key template: %w", iss.Err())
	}
	if ast.OutputType() != cel.StringType {
		return nil, fmt.Errorf("alarm-key template must evaluate to a string, got %s", ast.OutputType())
	}
	program, err := env.Program(ast, cel.CostLimit(alarmKeyCostCeilingBackstop))
	if err != nil {
		return nil, fmt.Errorf("alarm-key template: build program: %w", err)
	}
	return &CompiledAlarmKeyTemplate{program: program}, nil
}

// Eval renders the alarm key for one detection's series and VALIDATES the result against the
// ADR-042 token grammar before returning it. The validation is not belt-and-braces: it is the whole
// reason this returns an error at all on a successful evaluation.
//
// A static AlarmKey is grammar-checked at publish, where the author can be told. A rendered key
// cannot be — its value depends on the device token — so the check moves to the only place that
// holds the answer. An unchecked key would reach a `varchar(128) not null` column and the alarm's
// identity: too long is a write error that redelivers to poison, empty is dropped by the raise-alarm
// consumer as malformed, and a metacharacter-bearing key is spliced wherever alarm keys travel. The
// caller (the REACT dispatcher) fails CLOSED on this error — it skips the action loudly rather than
// filing an alarm under a key it cannot trust.
func (t *CompiledAlarmKeyTemplate) Eval(series string) (string, error) {
	out, _, err := t.program.Eval(map[string]any{GuardVarSeries: series})
	if err != nil {
		return "", err
	}
	s, ok := out.Value().(string)
	if !ok {
		// The env guarantees string output at build; unreachable belt-and-braces.
		return "", fmt.Errorf("alarm-key template produced a non-string %T at runtime", out.Value())
	}
	if err := core.ValidateToken(s); err != nil {
		return "", fmt.Errorf("rendered alarm key %q is not a valid key: %w", s, err)
	}
	return s, nil
}
