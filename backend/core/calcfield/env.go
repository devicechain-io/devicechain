// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sync"

	"github.com/google/cel-go/cel"
	celenv "github.com/google/cel-go/common/env"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/overloads"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// VarM is the map every input is reachable through, identifier or not.
const VarM = "m"

// MaxInputs bounds how many inputs one Env declares. A profile's measured metrics are a
// small set; the bound keeps the sample map's static size estimate finite and the
// per-sample activation small.
const MaxInputs = 256

// identifierPattern is a CEL identifier. A key matching it (and not reserved) becomes a
// top-level variable; any other key is reachable only through m.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedNames cannot be top-level variables: CEL's reserved words, the type identifiers,
// the `m` and `device` maps, the `math` function namespace and the has macro. A metric with one of these
// keys is still an input, reached as m["key"].
var reservedNames = map[string]struct{}{
	// CEL language keywords and reserved words.
	"true": {}, "false": {}, "null": {}, "in": {}, "as": {}, "break": {}, "const": {},
	"continue": {}, "else": {}, "for": {}, "function": {}, "if": {}, "import": {}, "let": {},
	"loop": {}, "package": {}, "namespace": {}, "return": {}, "var": {}, "void": {}, "while": {},
	// Type identifiers, which name type values in expressions.
	"int": {}, "uint": {}, "double": {}, "bool": {}, "string": {}, "bytes": {}, "list": {},
	"map": {}, "type": {}, "null_type": {}, "dyn": {},
	// This environment's own names.
	VarM: {}, VarDevice: {}, "math": {}, "has": {},
}

// isTopLevel reports whether a key is declared as a top-level variable.
func isTopLevel(key string) bool {
	if _, reserved := reservedNames[key]; reserved {
		return false
	}
	return identifierPattern.MatchString(key)
}

// celType is the CEL type of an input of type t.
func celType(t Type) *cel.Type {
	switch t {
	case Integer:
		return cel.IntType
	case Boolean:
		return cel.BoolType
	default:
		return cel.DoubleType
	}
}

// Env is the compile environment for one profile snapshot's inputs. It is immutable after
// NewEnv and safe for concurrent use; every Formula compiled from it reads Samples from it.
type Env struct {
	inputs []Input
	// index maps every input key (identifier or not) to its position in inputs and in a
	// Sample's value slice. Read-only after construction.
	index map[string]int
	cel   *cel.Env
	pool  sync.Pool
}

// ErrInvalidInputs is returned by NewEnv for an input list it cannot declare.
var ErrInvalidInputs = errors.New("calcfield: invalid inputs")

// NewEnv builds the environment for the given declared, measured inputs. Keys must be
// non-empty and unique, and every type valid; at most MaxInputs. The order of inputs is the
// order of the Sample value slice (Env.Index).
func NewEnv(inputs []Input) (*Env, error) {
	if len(inputs) > MaxInputs {
		return nil, fmt.Errorf("%w: %d inputs exceeds the maximum of %d", ErrInvalidInputs, len(inputs), MaxInputs)
	}
	base, err := baseEnv()
	if err != nil {
		return nil, err
	}
	e := &Env{inputs: make([]Input, len(inputs)), index: make(map[string]int, len(inputs))}
	copy(e.inputs, inputs)
	vars := make([]cel.EnvOption, 0, len(inputs))
	for i, in := range e.inputs {
		if in.Key == "" {
			return nil, fmt.Errorf("%w: input %d has an empty key", ErrInvalidInputs, i)
		}
		if !in.Type.valid() {
			return nil, fmt.Errorf("%w: input %q has invalid type %s", ErrInvalidInputs, in.Key, in.Type)
		}
		if _, dup := e.index[in.Key]; dup {
			return nil, fmt.Errorf("%w: input %q is declared twice", ErrInvalidInputs, in.Key)
		}
		e.index[in.Key] = i
		if isTopLevel(in.Key) {
			vars = append(vars, cel.Variable(in.Key, celType(in.Type)))
		}
	}
	e.cel, err = base.Extend(vars...)
	if err != nil {
		return nil, fmt.Errorf("calcfield: declare inputs: %w", err)
	}
	e.pool.New = func() any {
		return &Sample{env: e, vals: make([]ref.Val, len(e.inputs)), bad: make([]SkipReason, len(e.inputs))}
	}
	return e, nil
}

// Inputs returns a copy of the declared inputs, in Sample order.
func (e *Env) Inputs() []Input {
	out := make([]Input, len(e.inputs))
	copy(out, e.inputs)
	return out
}

// Index returns the Sample position of an input key.
func (e *Env) Index(key string) (int, bool) {
	i, ok := e.index[key]
	return i, ok
}

// baseEnv is the input-independent environment every Env extends, built once.
//
// It is a CUSTOM environment over an explicit subset of the CEL standard library: only the
// functions and overloads listed in stdlibSubset exist at all, so the checker refuses
// anything else (strings functions, timestamps, durations, size, matches, the comprehension
// macros) before the allowlist walker is even reached. The walker is the second fence.
var baseEnv = sync.OnceValues(func() (*cel.Env, error) {
	opts := []cel.EnvOption{
		cel.StdLib(cel.StdLibSubset(stdlibSubset())),
		// Bound the parser's error recovery. A refused formula is the common case while an
		// author types, and without a bound one malformed 1 KiB source makes the parser
		// recover at every operator, reporting dozens of errors nobody reads. Four
		// recoveries still name the first mistakes, which is what the author fixes next.
		cel.ParserErrorRecoveryLimit(4),
		// `temp > 80` with temp a double: ordering across int and double.
		cel.CrossTypeNumericComparisons(true),
		cel.Variable(VarM, cel.MapType(cel.StringType, cel.DynType)),
		// The device's constants (constants.go). Typed double, so `device.k` needs no conversion
		// and nothing dynamic reaches a result through it.
		cel.Variable(VarDevice, cel.MapType(cel.StringType, cel.DoubleType)),
	}
	opts = append(opts, arithmetic()...)
	opts = append(opts, mathFunctions()...)
	env, err := cel.NewCustomEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("calcfield: build base environment: %w", err)
	}
	return env, nil
})

// stdlibSubset is the part of the CEL standard library a formula may use. The arithmetic
// operators + - * / are NOT taken from it: arithmetic declares them, because the standard
// declarations cannot be extended with the mixed int/double overloads.
func stdlibSubset() *celenv.LibrarySubset {
	whole := func(names ...string) []*celenv.Function {
		fns := make([]*celenv.Function, len(names))
		for i, n := range names {
			fns[i] = celenv.NewFunction(n)
		}
		return fns
	}
	only := func(name string, ids ...string) *celenv.Function {
		ovs := make([]*celenv.Overload, len(ids))
		for i, id := range ids {
			ovs[i] = &celenv.Overload{ID: id}
		}
		return celenv.NewFunction(name, ovs...)
	}
	fns := whole(
		operators.Modulo, operators.Negate,
		operators.Equals, operators.NotEquals,
		operators.Less, operators.LessEquals, operators.Greater, operators.GreaterEquals,
		operators.LogicalAnd, operators.LogicalOr, operators.LogicalNot, operators.Conditional,
		operators.Index, operators.In,
	)
	fns = append(fns,
		only(overloads.TypeConvertInt, overloads.IntToInt, overloads.DoubleToInt),
		only(overloads.TypeConvertDouble, overloads.DoubleToDouble, overloads.IntToDouble),
		only(overloads.TypeConvertBool, overloads.BoolToBool),
	)
	return &celenv.LibrarySubset{
		// has() is the only macro; all, exists, exists_one, map and filter are comprehensions.
		IncludeMacros:    []string{operators.Has},
		IncludeFunctions: fns,
	}
}

// arithmeticOps are the four operators arithmetic declares, with each one's overload-id stem
// and its double form.
var arithmeticOps = []struct {
	op, name string
	f        func(a, b float64) float64
}{
	{operators.Add, "add", func(a, b float64) float64 { return a + b }},
	{operators.Subtract, "subtract", func(a, b float64) float64 { return a - b }},
	{operators.Multiply, "multiply", func(a, b float64) float64 { return a * b }},
	{operators.Divide, "divide", func(a, b float64) float64 { return a / b }},
}

// intArithmetic is CEL's own checked int arithmetic for each operator — overflow and
// division by zero are the errors cel-go's Int reports, so classify sees them unchanged.
var intArithmetic = map[string]func(a types.Int, b ref.Val) ref.Val{
	operators.Add:      func(a types.Int, b ref.Val) ref.Val { return a.Add(b) },
	operators.Subtract: func(a types.Int, b ref.Val) ref.Val { return a.Subtract(b) },
	operators.Multiply: func(a types.Int, b ref.Val) ref.Val { return a.Multiply(b) },
	operators.Divide:   func(a types.Int, b ref.Val) ref.Val { return a.Divide(b) },
}

// arithmetic declares + - * / over int and double. (int, int) is CEL's checked int
// arithmetic and (double, double) is IEEE double arithmetic, as in standard CEL. In addition,
// (double, int) and (int, double) promote the int operand to double, so the formulas people
// write — `voltage * current / 1000`, `degC * 1.8 + 32` — compile; standard CEL has no
// implicit numeric conversion. The promotion is exact below 2^53 and deterministic above it.
// A double result that is NaN or ±Inf — a division by zero, an overflow — is an error, skipped
// as non-finite, never a value a comparison could turn into a plausible boolean.
func arithmetic() []cel.EnvOption {
	d, i := cel.DoubleType, cel.IntType
	opts := make([]cel.EnvOption, 0, len(arithmeticOps))
	for _, a := range arithmeticOps {
		f, intOp := a.f, intArithmetic[a.op]
		opts = append(opts, cel.Function(a.op,
			cel.Overload("calcfield_"+a.name+"_int_int", []*cel.Type{i, i}, i,
				cel.BinaryBinding(func(x, y ref.Val) ref.Val { return intOp(x.(types.Int), y) })),
			cel.Overload("calcfield_"+a.name+"_double_double", []*cel.Type{d, d}, d,
				cel.BinaryBinding(func(x, y ref.Val) ref.Val {
					return finiteDouble(f(float64(x.(types.Double)), float64(y.(types.Double))))
				})),
			cel.Overload("calcfield_"+a.name+"_double_int", []*cel.Type{d, i}, d,
				cel.BinaryBinding(func(x, y ref.Val) ref.Val {
					return finiteDouble(f(float64(x.(types.Double)), float64(y.(types.Int))))
				})),
			cel.Overload("calcfield_"+a.name+"_int_double", []*cel.Type{i, d}, d,
				cel.BinaryBinding(func(x, y ref.Val) ref.Val {
					return finiteDouble(f(float64(x.(types.Int)), float64(y.(types.Double))))
				})),
		))
	}
	return opts
}

// errNonFinite is the error of a double operation whose result is NaN or ±Inf. cel-go's
// types.Err compares by message in errors.Is, so classify matches it by this sentinel.
var errNonFinite = errors.New("calcfield: non-finite double")

// finiteDouble is f as a CEL double, or the non-finite error. Every double-producing function
// of this environment returns through it, and inputs are refused non-finite at Sample.Set, so
// no double inside an evaluation is ever NaN or ±Inf.
func finiteDouble(f float64) ref.Val {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return types.WrapErr(errNonFinite)
	}
	return types.Double(f)
}

// errIntOverflowVal is the CEL error an int function returns on overflow. Its message is the
// one cel-go's own int arithmetic uses, so classify treats both alike.
func errIntOverflowVal() ref.Val { return types.NewErr("%s", errIntOverflow.Error()) }

// mathFunctionNames are the functions mathFunctions declares; the allowlist admits exactly
// these.
var mathFunctionNames = map[string]struct{}{
	"math.abs": {}, "math.floor": {}, "math.ceil": {}, "math.round": {}, "math.sqrt": {},
	"math.pow": {}, "math.min": {}, "math.max": {}, "math.clamp": {},
}

// mathFunctions declares this package's own fixed-arity math functions. They are defined
// here, not taken from a cel-go extension, so their semantics are this package's to keep
// stable: round is half away from zero (Go's math.Round), and min/max propagate NaN.
func mathFunctions() []cel.EnvOption {
	d, i := cel.DoubleType, cel.IntType
	dd := func(f func(float64) float64) cel.OverloadOpt {
		return cel.UnaryBinding(func(v ref.Val) ref.Val { return finiteDouble(f(toFloat(v))) })
	}
	dd2 := func(f func(a, b float64) float64) cel.OverloadOpt {
		return cel.BinaryBinding(func(a, b ref.Val) ref.Val { return finiteDouble(f(toFloat(a), toFloat(b))) })
	}
	return []cel.EnvOption{
		cel.Function("math.abs",
			cel.Overload("calcfield_math_abs_double", []*cel.Type{d}, d, dd(math.Abs)),
			cel.Overload("calcfield_math_abs_int", []*cel.Type{i}, i, cel.UnaryBinding(func(v ref.Val) ref.Val {
				n := int64(v.(types.Int))
				if n == math.MinInt64 {
					return errIntOverflowVal()
				}
				if n < 0 {
					n = -n
				}
				return types.Int(n)
			}))),
		cel.Function("math.floor", cel.Overload("calcfield_math_floor_double", []*cel.Type{d}, d, dd(math.Floor))),
		cel.Function("math.ceil", cel.Overload("calcfield_math_ceil_double", []*cel.Type{d}, d, dd(math.Ceil))),
		cel.Function("math.round", cel.Overload("calcfield_math_round_double", []*cel.Type{d}, d, dd(math.Round))),
		cel.Function("math.sqrt",
			cel.Overload("calcfield_math_sqrt_double", []*cel.Type{d}, d, dd(math.Sqrt)),
			cel.Overload("calcfield_math_sqrt_int", []*cel.Type{i}, d, dd(math.Sqrt))),
		// math.pow takes an INT exponent only. Go's math.Pow computes a fractional power
		// through Exp and Log, whose polynomial steps the compiler fuses into FMA
		// instructions on arm64 but not on amd64, so the same inputs give results one ulp
		// apart by CPU architecture — a stored value that depends on which replica computed
		// it. An integer power is repeated squaring (multiplications only) and is identical
		// on both; math.sqrt is correctly rounded everywhere.
		cel.Function("math.pow",
			cel.Overload("calcfield_math_pow_double_int", []*cel.Type{d, i}, d, dd2(math.Pow)),
			cel.Overload("calcfield_math_pow_int_int", []*cel.Type{i, i}, d, dd2(math.Pow))),
		cel.Function("math.min",
			cel.Overload("calcfield_math_min_double_double", []*cel.Type{d, d}, d, dd2(math.Min)),
			cel.Overload("calcfield_math_min_double_int", []*cel.Type{d, i}, d, dd2(math.Min)),
			cel.Overload("calcfield_math_min_int_double", []*cel.Type{i, d}, d, dd2(math.Min)),
			cel.Overload("calcfield_math_min_int_int", []*cel.Type{i, i}, i, cel.BinaryBinding(func(a, b ref.Val) ref.Val {
				return types.Int(min(int64(a.(types.Int)), int64(b.(types.Int))))
			}))),
		cel.Function("math.max",
			cel.Overload("calcfield_math_max_double_double", []*cel.Type{d, d}, d, dd2(math.Max)),
			cel.Overload("calcfield_math_max_double_int", []*cel.Type{d, i}, d, dd2(math.Max)),
			cel.Overload("calcfield_math_max_int_double", []*cel.Type{i, d}, d, dd2(math.Max)),
			cel.Overload("calcfield_math_max_int_int", []*cel.Type{i, i}, i, cel.BinaryBinding(func(a, b ref.Val) ref.Val {
				return types.Int(max(int64(a.(types.Int)), int64(b.(types.Int))))
			}))),
		cel.Function("math.clamp",
			cel.Overload("calcfield_math_clamp_double", []*cel.Type{d, d, d}, d, cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				x, lo, hi := toFloat(args[0]), toFloat(args[1]), toFloat(args[2])
				if lo > hi {
					return types.NewErr("math.clamp: lower bound %v is above upper bound %v", lo, hi)
				}
				return types.Double(math.Min(math.Max(x, lo), hi))
			})),
			cel.Overload("calcfield_math_clamp_int", []*cel.Type{i, i, i}, i, cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				x, lo, hi := int64(args[0].(types.Int)), int64(args[1].(types.Int)), int64(args[2].(types.Int))
				if lo > hi {
					return types.NewErr("math.clamp: lower bound %d is above upper bound %d", lo, hi)
				}
				return types.Int(min(max(x, lo), hi))
			}))),
	}
}

// toFloat reads an int or double CEL value as a float64. The overloads above admit only those
// two, so any other type is unreachable.
func toFloat(v ref.Val) float64 {
	switch n := v.(type) {
	case types.Double:
		return float64(n)
	case types.Int:
		return float64(n)
	}
	panic(fmt.Sprintf("calcfield: numeric overload received %T", v))
}
