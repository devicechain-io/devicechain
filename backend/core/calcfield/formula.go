// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// MaxExpressionBytes bounds a formula's source.
const MaxExpressionBytes = 1024

// StoredMagnitude bounds a stored measurement value: event storage keeps one as
// decimal(20,8), twelve digits before the point, so a value whose magnitude is 1e12 or more
// cannot be written at all. A result at or beyond it is skipped as SkipUnstorable, whatever
// the definition's Min/Max say: a row the store refuses fails the whole reading it belongs
// to, raw values included, where a skip loses only the one calculated value.
//
// The store also rounds to eight decimal places. That rounding is not a skip — the stored
// row is the rounded value, while the resolved event (which DETECT, preview and the live
// stream read) carries the full shortest-round-trip string. Nothing about event identity
// depends on either: a calculated value is outside the event id and its row's payload id is
// value-free.
const StoredMagnitude = 1e12

// CostCeiling is the static worst-case cost one formula may estimate to, in cel-go cost
// units. It is the DETECT predicate ceiling: a formula is the same kind of small arithmetic
// expression over one sample.
//
// It is the ONLY cost bound: the program carries no runtime cel.CostLimit. The language this
// package admits has no loops, comprehensions or size-dependent operations, so every node is
// charged at most once and the static estimate is an upper bound on the cost of every
// evaluation; TestStaticEstimateBoundsRuntimeCost pins that over generated formulas. A
// runtime limit would only re-check it, at more than twice the cost of the evaluation itself
// (cel-go clones a cost tracker and observes every node). Anything that admits a
// size-dependent operation must fail that test first, and bring the limit back.
const CostCeiling uint64 = 100

// ProfileCostCeiling bounds the summed static cost of every formula on one profile (see
// CheckProfileCost): the per-reading CPU a profile can add to the resolver.
const ProfileCostCeiling uint64 = 1000

// CompileError is a formula refused at compile: a parse, type or allowlist failure. Its
// message is meant for the author.
type CompileError struct {
	Key        string
	Expression string
	Err        error
}

func (e *CompileError) Error() string {
	return fmt.Sprintf("calculated metric %q: %v", e.Key, e.Err)
}

func (e *CompileError) Unwrap() error { return e.Err }

// CostError is a formula, or a profile's formulas together, whose static worst-case cost
// exceeds its ceiling.
type CostError struct {
	Key          string // empty for the profile-wide check
	EstimatedMax uint64
	Ceiling      uint64
}

func (e *CostError) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("the profile's calculated metrics estimate to cost %d together, above the ceiling of %d", e.EstimatedMax, e.Ceiling)
	}
	return fmt.Sprintf("calculated metric %q: estimated cost %d is above the ceiling of %d", e.Key, e.EstimatedMax, e.Ceiling)
}

// Formula is a compiled calculated metric: immutable and safe for concurrent use. Compile
// once (per published profile snapshot), evaluate against every sample.
type Formula struct {
	env     *Env
	def     Definition
	program cel.Program
	costMax uint64
	// inputs are the Env positions the expression names, ascending.
	inputs []int
	// required are the inputs the expression never tests for presence (no has(m.k), no
	// "k" in m), ascending. Any of them absent skips the formula before it runs.
	required []int
	// constants are the device constants the expression names, and requiredConstants the ones
	// it never tests for presence (no has(device.k), no "k" in device); both sorted by name.
	// Both are nil for a formula that reads none, which is every formula that predates them.
	constants         []string
	requiredConstants []string
}

// Compile parses, type-checks, allowlists and cost-gates a definition against e. It fails
// closed, with a message for the author, when:
//
//   - the definition has no valid result type, or the source is empty or longer than
//     MaxExpressionBytes;
//   - its key is one of the inputs (a calculated metric cannot shadow a measured one);
//   - the source does not parse or type-check, or names anything but declared inputs;
//   - the result type is not exactly the declared type (an int formula for a DOUBLE metric
//     needs double(...), a double for an INT metric needs int(...));
//   - it uses anything outside the allowlist (see the package documentation);
//   - it reads no input at all (a literal or a device constant is not a measurement), or reads
//     more than MaxConstants device constants;
//   - its static worst-case cost exceeds CostCeiling.
func (e *Env) Compile(def Definition) (*Formula, error) {
	return e.compile(def, CostCeiling)
}

// compile is Compile at an arbitrary ceiling. Only this package's tests pass anything but
// CostCeiling, to reach the static gate's boundary.
func (e *Env) compile(def Definition, staticCeiling uint64) (*Formula, error) {
	refuse := func(err error) (*Formula, error) {
		return nil, &CompileError{Key: def.Key, Expression: def.Expression, Err: err}
	}
	if !def.Type.valid() {
		return refuse(fmt.Errorf("result type %s is not DOUBLE, INT or BOOLEAN", def.Type))
	}
	if def.Expression == "" {
		return refuse(errors.New("the formula is empty"))
	}
	if len(def.Expression) > MaxExpressionBytes {
		return refuse(fmt.Errorf("the formula is %d bytes, above the maximum of %d", len(def.Expression), MaxExpressionBytes))
	}
	if i, isInput := e.index[def.Key]; isInput && i < e.measured {
		return refuse(errors.New("the key is already a measured metric of this profile"))
	}
	ast, iss := e.cel.Compile(def.Expression)
	if iss != nil && iss.Err() != nil {
		return refuse(withHint(iss.Err()))
	}
	if want := celType(def.Type); !ast.OutputType().IsExactType(want) {
		return refuse(fmt.Errorf("the formula produces %s but the metric is %s%s",
			ast.OutputType(), def.Type, conversionHint(def.Type)))
	}
	w := &allowlist{env: e}
	walk, err := w.check(ast.NativeRep().Expr())
	if err != nil {
		return refuse(err)
	}
	if len(walk.inputs) == 0 {
		return refuse(errors.New("the formula reads no input: a calculated metric must use at least one measured or calculated metric"))
	}
	est, err := e.cel.EstimateCost(ast, estimator{size: uint64(len(e.inputs))})
	if err != nil {
		return refuse(fmt.Errorf("estimate cost: %w", err))
	}
	if est.Max > staticCeiling {
		return nil, &CostError{Key: def.Key, EstimatedMax: est.Max, Ceiling: staticCeiling}
	}
	// No cel.CostLimit: the static gate above is the bound (see CostCeiling).
	program, err := e.cel.Program(ast)
	if err != nil {
		return refuse(fmt.Errorf("build program: %w", err))
	}
	return &Formula{env: e, def: def, program: program, costMax: est.Max, inputs: walk.inputs,
		required: walk.required, constants: walk.constants, requiredConstants: walk.reqConstant}, nil
}

// CheckProfileCost sums the static cost of a profile's formulas against ProfileCostCeiling.
func CheckProfileCost(formulas []*Formula) error {
	var total uint64
	for _, f := range formulas {
		total += f.costMax
	}
	if total > ProfileCostCeiling {
		return &CostError{EstimatedMax: total, Ceiling: ProfileCostCeiling}
	}
	return nil
}

// withHint appends a hint to a checker error that is almost always a missing decimal point.
func withHint(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "'math.pow'") {
		return fmt.Errorf("%w\nhint: math.pow takes an int exponent, as math.pow(x, 2); for a square root use math.sqrt(x)", err)
	}
	if strings.Contains(msg, "double, int") || strings.Contains(msg, "int, double") {
		return fmt.Errorf("%w\nhint: an int and a double do not mix here; write 0.0 rather than 0, or convert with double(...)", err)
	}
	return err
}

// conversionHint suggests the conversion that would make a formula produce t.
func conversionHint(t Type) string {
	switch t {
	case Double:
		return "; wrap it in double(...)"
	case Integer:
		return "; wrap it in int(...), which truncates toward zero"
	}
	return ""
}

// Key is the calculated metric's key.
func (f *Formula) Key() string { return f.def.Key }

// Type is the declared result type.
func (f *Formula) Type() Type { return f.def.Type }

// Expression is the CEL source.
func (f *Formula) Expression() string { return f.def.Expression }

// CostMax is the static worst-case cost that passed the ceiling.
func (f *Formula) CostMax() uint64 { return f.costMax }

// InputKeys returns the keys of the inputs the formula names, in Env order.
func (f *Formula) InputKeys() []string {
	keys := make([]string, len(f.inputs))
	for j, i := range f.inputs {
		keys[j] = f.env.inputs[i].Key
	}
	return keys
}

// ConstantKeys returns the names of the device constants the formula reads, sorted. Empty for
// a formula that reads none. The caller reads exactly these from the device record.
func (f *Formula) ConstantKeys() []string { return append([]string{}, f.constants...) }

// errForeignSample is a Sample from another Env handed to Eval: a caller bug, skipped loudly.
var errForeignSample = errors.New("calcfield: the sample belongs to a different environment")

// Eval evaluates the formula against one sample. It never fails the sample: a formula that
// cannot produce a value returns a SkipReason and no value. See the package documentation
// for the policy; in order of precedence:
//
//   - an input the formula never tests for presence is absent → SkipMissingInput,
//     unparseable → SkipInvalidInput, or a non-finite DOUBLE → SkipNonFinite, decided BEFORE
//     evaluation. So `temp > 80 || fan == 0`
//     skips when fan is absent although CEL would short-circuit to true on a hot reading:
//     whether an absent input is noticed must not depend on operand order or on the other
//     inputs' values;
//   - otherwise the formula runs, and an evaluation that ends in an error is skipped by that
//     error: an input the formula guards somewhere but read outside its guard while absent →
//     SkipMissingInput, or unparseable → SkipInvalidInput; an integer division or modulus
//     by zero → SkipDivZero; an integer overflow →
//     SkipOverflow; a double operation producing NaN or ±Inf → SkipNonFinite, wherever it
//     occurs (so `voltage / current > 10.0` skips when current is 0 rather than storing a
//     true); anything else → SkipEvalError. A failed read that CEL's && or || absorbs
//     into a value (`temp > 80.0 || fan > 1` on a hot reading, fan guarded elsewhere) does not
//     skip: that value is the result. CEL's logical operators are commutative over errors, so
//     the value is the same in either operand order, whereas whether the read is REACHED is
//     not (|| short-circuits left to right), which is why nothing here depends on it. When two
//     different failures both reach the result CEL reports the first it met, so the reason,
//     never whether the formula skips, can follow operand order;
//   - a DOUBLE result that is NaN or ±Inf → SkipNonFinite (a backstop: no double operation
//     produces one);
//   - a DOUBLE or INT result outside the definition's Min/Max → SkipRange.
func (f *Formula) Eval(s *Sample) Result {
	if s.env != f.env {
		return Result{Skip: SkipEvalError, Err: errForeignSample}
	}
	for _, i := range f.required {
		if s.vals[i] == nil {
			return Result{Skip: s.absentReason(i)}
		}
	}
	for _, k := range f.requiredConstants {
		if r := s.constantReason(k); r != SkipNone {
			return Result{Skip: r}
		}
	}
	out, _, err := f.program.Eval(s)
	if err != nil {
		return Result{Skip: classify(err), Err: err}
	}
	return f.finish(out)
}

// Sentinels matching cel-go's integer error messages. cel-go's types.Err compares by message
// in errors.Is, so these match both its own arithmetic errors and errIntOverflowVal.
var (
	errDivideByZero  = errors.New("division by zero")
	errModulusByZero = errors.New("modulus by zero")
	errIntOverflow   = errors.New("integer overflow")
)

// classify maps an evaluation error to its skip reason.
func classify(err error) SkipReason {
	switch {
	case errors.Is(err, errDivideByZero), errors.Is(err, errModulusByZero):
		return SkipDivZero
	case errors.Is(err, errIntOverflow):
		return SkipOverflow
	case errors.Is(err, errNonFinite):
		return SkipNonFinite
	}
	var in *inputError
	if errors.As(err, &in) {
		return in.reason
	}
	return SkipEvalError
}

// finish checks and formats a result value.
func (f *Formula) finish(out ref.Val) Result {
	switch f.def.Type {
	case Double:
		d, ok := out.(types.Double)
		if !ok {
			return f.wrongType(out)
		}
		v := float64(d)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return Result{Skip: SkipNonFinite}
		}
		if !f.inRange(v) {
			return Result{Skip: SkipRange}
		}
		if math.Abs(v) >= StoredMagnitude {
			return Result{Skip: SkipUnstorable}
		}
		return Result{Value: FormatDouble(v)}
	case Integer:
		n, ok := out.(types.Int)
		if !ok {
			return f.wrongType(out)
		}
		if !f.inRange(float64(n)) {
			return Result{Skip: SkipRange}
		}
		if n >= StoredMagnitude || n <= -StoredMagnitude {
			return Result{Skip: SkipUnstorable}
		}
		return Result{Value: FormatInt(int64(n))}
	case Boolean:
		b, ok := out.(types.Bool)
		if !ok {
			return f.wrongType(out)
		}
		return Result{Value: FormatBool(bool(b))}
	}
	return f.wrongType(out)
}

// wrongType is a result whose runtime type is not the declared one. Compile checks the type
// statically, so this is a backstop: skipped loudly, never coerced.
func (f *Formula) wrongType(out ref.Val) Result {
	return Result{Skip: SkipEvalError, Err: fmt.Errorf("calcfield: %q produced %T, not %s", f.def.Key, out, f.def.Type)}
}

// inRange applies the definition's inclusive bounds, as the measurement validator does.
func (f *Formula) inRange(v float64) bool {
	if f.def.Min != nil && v < *f.def.Min {
		return false
	}
	if f.def.Max != nil && v > *f.def.Max {
		return false
	}
	return true
}

// FormatDouble renders a DOUBLE value as stored: the shortest decimal that parses back to
// exactly v (strconv 'g', precision -1), with negative zero normalized to "0". The stored
// string is the value of record, so this is a correctness contract, not presentation: the
// same float must always produce the same bytes.
func FormatDouble(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// FormatInt renders an INT value as stored: base 10.
func FormatInt(n int64) string { return strconv.FormatInt(n, 10) }

// FormatBool renders a BOOLEAN value as stored: "1" or "0", as the resolver normalizes a
// device-sent boolean.
func FormatBool(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// estimator gives `m` a finite size — the number of declared inputs — so a formula's
// worst-case cost is decidable; everything else takes cel-go's defaults. The allowlist only
// lets m be read by constant key, so its size does not change any admitted formula's cost;
// the hint keeps the estimate finite regardless.
type estimator struct{ size uint64 }

func (e estimator) EstimateSize(node checker.AstNode) *checker.SizeEstimate {
	if path := node.Path(); len(path) > 0 {
		switch path[0] {
		case VarM:
			return &checker.SizeEstimate{Min: 0, Max: e.size}
		case VarDevice:
			return &checker.SizeEstimate{Min: 0, Max: MaxConstants}
		}
	}
	return nil
}

func (estimator) EstimateCallCost(string, string, *checker.AstNode, []checker.AstNode) *checker.CallEstimate {
	return nil
}
