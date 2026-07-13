// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Guard is the REACT-side per-action gate (ADR-053 slice 9c / ADR-054): a bounded, stateless
// CEL boolean the dispatcher evaluates against a fired detection to decide whether one action
// runs. It is what a canvas "branch" node lowers to — a branch folds its predicate onto the
// guard of each action downstream of it (graph.Compile), so the runtime has no new node, only a
// richer Action. Like the DETECT predicate it is cost-gated at the publish gate and carries a
// runtime CostLimit backstop; unlike it, its environment is the DERIVED event (the thin scalar a
// detection carries), not the resolved event — a guard runs AFTER detection, so it sees only what
// the signal carries, never the original measurement map.
//
// The determinism boundary holds: a guard is a pure per-message boolean with no side effects and
// no state, so it is safe in REACT's at-least-once, queue-group-ready dispatcher (a redelivery
// re-evaluates to the same bit). It never gates a raiseAlarm action's structural falling-edge
// clear — that is the dispatcher's invariant (see react.Dispatcher), enforced there rather than
// here, because only the dispatcher knows the edge.
package rules

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/ext"
)

// The variable names the guard environment declares — the whole vocabulary a guard may
// reference (cel-go's type checker rejects any other identifier). They are the derived event's
// scalar fields (runtime.DerivedEvent), NOT the resolved event's map: a guard runs in REACT,
// after detection, so it cannot re-read the measurements. Keep these stable — a published guard
// names them.
const (
	// GuardVarValue is the scalar the detection is about (the crossing sample, computed rate, or
	// window aggregate). It is 0.0 when the detection carries no value (a silence-driven absence /
	// duration fire, a metric-less raw-CEL leaf), so a guard that means to test a real reading must
	// pair it with GuardVarHasValue (`hasValue && value > 100`) rather than trust a bare `value`.
	GuardVarValue = "value"
	// GuardVarHasValue reports whether the detection carried a value at all — the presence bit that
	// distinguishes a genuine 0.0 reading from "no value" (see GuardVarValue).
	GuardVarHasValue = "hasValue"
	// GuardVarSeries is the detection's series token — the device token (or, for a correlation
	// rule, the anchor token, though correlation rules cannot carry actions so a guard never sees
	// one). It lets a guard route on device identity before the enrich node (attribute routing)
	// lands.
	GuardVarSeries = "series"
)

// guardCostCeilingBackstop is the runtime CostLimit stamped on a dispatch-built guard program. The
// AUTHORITATIVE cost gate is the publish-time EstimateCost check in CompileGuard against the
// tenant ceiling; this is only the runaway backstop on the Program, generous for any real guard.
const guardCostCeilingBackstop uint64 = 1_000

var (
	guardEnv     *cel.Env
	guardEnvErr  error
	guardEnvOnce sync.Once
)

// GuardEnv returns the process-wide shared CEL environment a guard is compiled against. Built
// once and reused (cel.Env is safe for concurrent Program construction); fails closed on a
// construction error rather than yielding a half-built env.
func GuardEnv() (*cel.Env, error) {
	guardEnvOnce.Do(func() {
		guardEnv, guardEnvErr = cel.NewEnv(
			cel.Variable(GuardVarValue, cel.DoubleType),
			cel.Variable(GuardVarHasValue, cel.BoolType),
			cel.Variable(GuardVarSeries, cel.StringType),
			// The `cel.bind` scoping macro — the ONLY surface a compute node adds on the REACT side
			// (ADR-053 slice 9a-2). It lets the canvas compiler fold a named compute into a branch
			// guard as a real binding rather than by text interpolation; purely additive (a guard that
			// never writes cel.bind compiles identically), no new data or side effects.
			ext.Bindings(),
		)
	})
	if guardEnvErr != nil {
		return nil, fmt.Errorf("build REACT guard environment: %w", guardEnvErr)
	}
	return guardEnv, nil
}

// guardEstimator bounds the cost of the guard env's only sizeable variable (the series string) so
// a guard's worst-case cost is decidable and gateable at publish. The scalar value/hasValue need
// no bound; everything else defers to cel-go's defaults.
type guardEstimator struct{}

func (guardEstimator) EstimateSize(node checker.AstNode) *checker.SizeEstimate {
	path := node.Path()
	if len(path) > 0 && path[0] == GuardVarSeries {
		return &checker.SizeEstimate{Min: 0, Max: guardSeriesMaxLen}
	}
	return nil
}

func (guardEstimator) EstimateCallCost(function, overloadID string, target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	return nil
}

// guardSeriesMaxLen bounds the estimated length of the series token (ADR-042 token grammar caps
// tokens well under this). A static estimation bound only — the runtime CostLimit is the backstop.
const guardSeriesMaxLen = 256

// CompileGuard parses, type-checks, and cost-gates a guard CEL boolean against the guard
// environment — the publish-time gate, mirroring predicate.Compile's fail-closed posture: a
// parse/type error, a non-boolean result, or a worst-case cost above the ceiling rejects the
// rule. It returns the estimated worst-case cost (for operator introspection); the caller (the
// publish gate) uses only the error. costCeiling is the per-tenant ceiling resolved by the caller
// (never zero/"unlimited" — the ADR-023 fail-safe).
func CompileGuard(source string, costCeiling uint64) (costMax uint64, err error) {
	env, err := GuardEnv()
	if err != nil {
		return 0, err
	}
	ast, iss := env.Compile(source)
	if iss != nil && iss.Err() != nil {
		return 0, fmt.Errorf("guard: %w", iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return 0, fmt.Errorf("guard must evaluate to a boolean, got %s", ast.OutputType())
	}
	est, err := env.EstimateCost(ast, guardEstimator{})
	if err != nil {
		return 0, fmt.Errorf("guard: estimate cost: %w", err)
	}
	if est.Max > costCeiling {
		return 0, fmt.Errorf("guard worst-case cost %d exceeds the ceiling %d", est.Max, costCeiling)
	}
	return est.Max, nil
}

// CompiledGuard is a built, evaluable guard program. It wraps the cel.Program so cel-go stays
// contained in this package (the REACT dispatcher caches and evaluates guards without importing
// cel). Safe for concurrent Eval.
type CompiledGuard struct {
	program cel.Program
}

// BuildGuardProgram builds an evaluable guard WITHOUT the publish-time cost gate — for the REACT
// dispatcher, which re-derives a guard from the durable rule projection per dispatch and only needs
// to run it (the tenant cost ceiling already gated it at publish, and the dispatcher does not know
// per-tenant ceilings). It still type-checks, requires a boolean output, and stamps a generous
// runtime CostLimit backstop, so a forged/hand-edited non-boolean or runaway guard is still
// rejected/bounded rather than trusted.
func BuildGuardProgram(source string) (*CompiledGuard, error) {
	env, err := GuardEnv()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(source)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("guard: %w", iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("guard must evaluate to a boolean, got %s", ast.OutputType())
	}
	program, err := env.Program(ast, cel.CostLimit(guardCostCeilingBackstop))
	if err != nil {
		return nil, fmt.Errorf("guard: build program: %w", err)
	}
	return &CompiledGuard{program: program}, nil
}

// GuardInput is one derived event as a guard sees it. Value is a pointer so "no value" is distinct
// from a genuine 0.0 (matching runtime.DerivedEvent.Value); Eval folds it into the value/hasValue
// pair.
type GuardInput struct {
	Value  *float64
	Series string
}

// Eval evaluates the guard against one derived event. A nil Value binds value=0.0 and
// hasValue=false, so a bare `value > x` is a clean false on a value-less detection rather than an
// error. An evaluation error (e.g. the runtime cost limit tripped) is returned; the dispatcher
// treats any error as "guard did not pass" (fail closed — do not dispatch).
func (g *CompiledGuard) Eval(in GuardInput) (bool, error) {
	var v float64
	has := in.Value != nil
	if has {
		v = *in.Value
	}
	out, _, err := g.program.Eval(map[string]any{
		GuardVarValue:    v,
		GuardVarHasValue: has,
		GuardVarSeries:   in.Series,
	})
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		// The env guarantees bool output at build; unreachable belt-and-braces.
		return false, fmt.Errorf("guard produced a non-boolean %T at runtime", out.Value())
	}
	return b, nil
}
