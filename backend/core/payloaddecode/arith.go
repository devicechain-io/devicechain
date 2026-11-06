// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"errors"
	"fmt"
	"math"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// The arithmetic and math functions below are a frozen copy of core/calcfield's, not an
// import of its unexported helpers: a function's semantics never change once an env version
// ships, and a copy cannot move when calcfield does.

var arithmeticOps = []struct {
	op, name string
	f        func(a, b float64) float64
}{
	{operators.Add, "add", func(a, b float64) float64 { return a + b }},
	{operators.Subtract, "subtract", func(a, b float64) float64 { return a - b }},
	{operators.Multiply, "multiply", func(a, b float64) float64 { return a * b }},
	{operators.Divide, "divide", func(a, b float64) float64 { return a / b }},
}

var intArithmetic = map[string]func(a types.Int, b ref.Val) ref.Val{
	operators.Add:      func(a types.Int, b ref.Val) ref.Val { return a.Add(b) },
	operators.Subtract: func(a types.Int, b ref.Val) ref.Val { return a.Subtract(b) },
	operators.Multiply: func(a types.Int, b ref.Val) ref.Val { return a.Multiply(b) },
	operators.Divide:   func(a types.Int, b ref.Val) ref.Val { return a.Divide(b) },
}

// arithmetic declares + - * / over int and double, with the mixed forms promoting the int
// operand. (int, int) is CEL's checked int arithmetic; a double result that is NaN or ±Inf is
// an error, never a value.
func arithmetic() []cel.EnvOption {
	d, i := cel.DoubleType, cel.IntType
	opts := make([]cel.EnvOption, 0, len(arithmeticOps))
	for _, a := range arithmeticOps {
		f, intOp := a.f, intArithmetic[a.op]
		opts = append(opts, cel.Function(a.op,
			cel.Overload("payloaddecode_"+a.name+"_int_int", []*cel.Type{i, i}, i,
				cel.BinaryBinding(func(x, y ref.Val) ref.Val { return intOp(x.(types.Int), y) })),
			cel.Overload("payloaddecode_"+a.name+"_double_double", []*cel.Type{d, d}, d,
				cel.BinaryBinding(func(x, y ref.Val) ref.Val {
					return finiteDouble(f(float64(x.(types.Double)), float64(y.(types.Double))))
				})),
			cel.Overload("payloaddecode_"+a.name+"_double_int", []*cel.Type{d, i}, d,
				cel.BinaryBinding(func(x, y ref.Val) ref.Val {
					return finiteDouble(f(float64(x.(types.Double)), float64(y.(types.Int))))
				})),
			cel.Overload("payloaddecode_"+a.name+"_int_double", []*cel.Type{i, d}, d,
				cel.BinaryBinding(func(x, y ref.Val) ref.Val {
					return finiteDouble(f(float64(x.(types.Int)), float64(y.(types.Double))))
				})),
		))
	}
	return opts
}

var errIntOverflow = errors.New("integer overflow")

func errIntOverflowVal() ref.Val { return types.NewErr("%s", errIntOverflow.Error()) }

// mathFunctionNames are the functions mathFunctions declares; the allowlist admits exactly
// these.
var mathFunctionNames = map[string]struct{}{
	"math.abs": {}, "math.floor": {}, "math.ceil": {}, "math.round": {}, "math.sqrt": {},
	"math.pow": {}, "math.min": {}, "math.max": {}, "math.clamp": {},
}

// mathCost is what a math function's own call costs: it has no estimator of its own, so the
// standard charge of one unit per call applies and nothing more.
const mathCost = costOne

var mathDocs = []FunctionDoc{
	{"math.abs", "math.abs(double|int) -> double|int", "Absolute value.", mathCost},
	{"math.floor", "math.floor(double) -> double", "Largest integral value not above the argument.", mathCost},
	{"math.ceil", "math.ceil(double) -> double", "Smallest integral value not below the argument.", mathCost},
	{"math.round", "math.round(double) -> double", "Rounds half away from zero.", mathCost},
	{"math.sqrt", "math.sqrt(double|int) -> double", "Square root; a negative argument is an error.", mathCost},
	{"math.pow", "math.pow(double|int, int) -> double", "Integer power by repeated multiplication.", mathCost},
	{"math.min", "math.min(a, b) -> double|int", "The smaller of two numbers.", mathCost},
	{"math.max", "math.max(a, b) -> double|int", "The larger of two numbers.", mathCost},
	{"math.clamp", "math.clamp(x, lo, hi) -> double|int", "x limited to [lo, hi]; lo above hi is an error.", mathCost},
}

func toFloat(v ref.Val) float64 {
	switch n := v.(type) {
	case types.Double:
		return float64(n)
	case types.Int:
		return float64(n)
	}
	panic(fmt.Sprintf("payloaddecode: numeric overload received %T", v))
}

// mathFunctions declares the fixed-arity math functions. math.pow takes an INT exponent
// only: Go's fractional math.Pow goes through Exp and Log, whose steps the compiler fuses into
// FMA instructions on arm64 but not on amd64, so the same inputs would give results one ulp
// apart by architecture. An integer power is multiplications only.
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
			cel.Overload("payloaddecode_math_abs_double", []*cel.Type{d}, d, dd(math.Abs)),
			cel.Overload("payloaddecode_math_abs_int", []*cel.Type{i}, i, cel.UnaryBinding(func(v ref.Val) ref.Val {
				n := int64(v.(types.Int))
				if n == math.MinInt64 {
					return errIntOverflowVal()
				}
				if n < 0 {
					n = -n
				}
				return types.Int(n)
			}))),
		cel.Function("math.floor", cel.Overload("payloaddecode_math_floor_double", []*cel.Type{d}, d, dd(math.Floor))),
		cel.Function("math.ceil", cel.Overload("payloaddecode_math_ceil_double", []*cel.Type{d}, d, dd(math.Ceil))),
		cel.Function("math.round", cel.Overload("payloaddecode_math_round_double", []*cel.Type{d}, d, dd(math.Round))),
		cel.Function("math.sqrt",
			cel.Overload("payloaddecode_math_sqrt_double", []*cel.Type{d}, d, dd(math.Sqrt)),
			cel.Overload("payloaddecode_math_sqrt_int", []*cel.Type{i}, d, dd(math.Sqrt))),
		cel.Function("math.pow",
			cel.Overload("payloaddecode_math_pow_double_int", []*cel.Type{d, i}, d, powBinding),
			cel.Overload("payloaddecode_math_pow_int_int", []*cel.Type{i, i}, d, powBinding)),
		cel.Function("math.min",
			cel.Overload("payloaddecode_math_min_double_double", []*cel.Type{d, d}, d, dd2(math.Min)),
			cel.Overload("payloaddecode_math_min_double_int", []*cel.Type{d, i}, d, dd2(math.Min)),
			cel.Overload("payloaddecode_math_min_int_double", []*cel.Type{i, d}, d, dd2(math.Min)),
			cel.Overload("payloaddecode_math_min_int_int", []*cel.Type{i, i}, i, cel.BinaryBinding(func(a, b ref.Val) ref.Val {
				return types.Int(min(int64(a.(types.Int)), int64(b.(types.Int))))
			}))),
		cel.Function("math.max",
			cel.Overload("payloaddecode_math_max_double_double", []*cel.Type{d, d}, d, dd2(math.Max)),
			cel.Overload("payloaddecode_math_max_double_int", []*cel.Type{d, i}, d, dd2(math.Max)),
			cel.Overload("payloaddecode_math_max_int_double", []*cel.Type{i, d}, d, dd2(math.Max)),
			cel.Overload("payloaddecode_math_max_int_int", []*cel.Type{i, i}, i, cel.BinaryBinding(func(a, b ref.Val) ref.Val {
				return types.Int(max(int64(a.(types.Int)), int64(b.(types.Int))))
			}))),
		cel.Function("math.clamp",
			cel.Overload("payloaddecode_math_clamp_double", []*cel.Type{d, d, d}, d, cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				x, lo, hi := toFloat(args[0]), toFloat(args[1]), toFloat(args[2])
				if lo > hi {
					return types.NewErr("math.clamp: lower bound %v is above upper bound %v", lo, hi)
				}
				return types.Double(math.Min(math.Max(x, lo), hi))
			})),
			cel.Overload("payloaddecode_math_clamp_int", []*cel.Type{i, i, i}, i, cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				x, lo, hi := int64(args[0].(types.Int)), int64(args[1].(types.Int)), int64(args[2].(types.Int))
				if lo > hi {
					return types.NewErr("math.clamp: lower bound %d is above upper bound %d", lo, hi)
				}
				return types.Int(min(max(x, lo), hi))
			}))),
	}
}

// maxPowExponent bounds math.pow's exponent. Go's math.Pow with an integer exponent is
// repeated squaring (O(log n)), but a huge exponent only ever yields 0, 1 or an overflow
// error, so there is nothing to compute past this.
const maxPowExponent = 1 << 20

var powBinding = cel.BinaryBinding(func(a, b ref.Val) ref.Val {
	e := int64(b.(types.Int))
	if e < -maxPowExponent || e > maxPowExponent {
		return nativeErr("math.pow", -1, "exponent %d is outside ±%d", e, maxPowExponent)
	}
	return finiteDouble(math.Pow(toFloat(a), float64(e)))
})
