// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/google/cel-go/cel"
	celenv "github.com/google/cel-go/common/env"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/overloads"
)

// The variables every expression sees.
const (
	VarFrame   = "frame"
	VarFPort   = "fPort"
	VarFCnt    = "fCnt"
	VarJSON    = "json"
	VarDecoded = "decoded"
	VarLink    = "link"
)

// identifierPattern is a CEL identifier; a let name must match it.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedNames cannot name a let: CEL's keywords and type identifiers, the environment's own
// variables and function namespaces.
var reservedNames = map[string]struct{}{
	"true": {}, "false": {}, "null": {}, "in": {}, "as": {}, "break": {}, "const": {},
	"continue": {}, "else": {}, "for": {}, "function": {}, "if": {}, "import": {}, "let": {},
	"loop": {}, "package": {}, "namespace": {}, "return": {}, "var": {}, "void": {}, "while": {},
	"int": {}, "uint": {}, "double": {}, "bool": {}, "string": {}, "bytes": {}, "list": {},
	"map": {}, "type": {}, "null_type": {}, "dyn": {}, "has": {}, "size": {},
	VarFrame: {}, VarFPort: {}, VarFCnt: {}, VarJSON: {}, VarDecoded: {}, VarLink: {},
	"math": {}, "hex": {}, "base64": {}, "lpp": {},
}

// comparisonOverloads are the ordering overloads admitted: int, uint and double, and every
// cross-type pairing of them. There is no string, bytes, bool, timestamp or duration ordering.
func comparisonOverloads() []*celenv.Function {
	suffixes := []string{
		"int64", "uint64", "double",
		"int64_double", "int64_uint64", "uint64_int64", "uint64_double", "double_int64", "double_uint64",
	}
	var fns []*celenv.Function
	for _, op := range []struct{ fn, stem string }{
		{operators.Less, "less"}, {operators.LessEquals, "less_equals"},
		{operators.Greater, "greater"}, {operators.GreaterEquals, "greater_equals"},
	} {
		ovs := make([]*celenv.Overload, len(suffixes))
		for i, s := range suffixes {
			ovs[i] = &celenv.Overload{ID: op.stem + "_" + s}
		}
		fns = append(fns, celenv.NewFunction(op.fn, ovs...))
	}
	return fns
}

// stdlibSubset is the part of the CEL standard library a decoder may use: comparison,
// logic, the conditional, `%`, negation, map index, list `in`, size of bytes, and the scalar
// conversions. The arithmetic operators + - * / are NOT taken from it: arithmetic.go declares
// them, because the standard declarations cannot be extended with the mixed int/double
// overloads. Macros: has() only; every comprehension (all, exists, exists_one, map, filter)
// is absent, so a loop cannot be written.
func stdlibSubset() *celenv.LibrarySubset {
	only := func(name string, ids ...string) *celenv.Function {
		ovs := make([]*celenv.Overload, len(ids))
		for i, id := range ids {
			ovs[i] = &celenv.Overload{ID: id}
		}
		return celenv.NewFunction(name, ovs...)
	}
	fns := []*celenv.Function{}
	for _, n := range []string{
		operators.Modulo, operators.Negate, operators.Equals, operators.NotEquals,
		operators.LogicalAnd, operators.LogicalOr, operators.LogicalNot, operators.Conditional,
	} {
		fns = append(fns, celenv.NewFunction(n))
	}
	fns = append(fns, comparisonOverloads()...)
	fns = append(fns,
		only(operators.Index, overloads.IndexMap),
		only(operators.In, overloads.InList),
		only(overloads.Size, overloads.SizeBytes),
		only(overloads.TypeConvertInt, overloads.IntToInt, overloads.DoubleToInt, overloads.UintToInt),
		only(overloads.TypeConvertUint, overloads.UintToUint, overloads.IntToUint, overloads.DoubleToUint),
		only(overloads.TypeConvertDouble, overloads.DoubleToDouble, overloads.IntToDouble, overloads.UintToDouble),
		only(overloads.TypeConvertBool, overloads.BoolToBool),
	)
	return &celenv.LibrarySubset{IncludeMacros: []string{operators.Has}, IncludeFunctions: fns}
}

// baseEnv is the let-free environment, built once.
var baseEnv = sync.OnceValues(func() (*cel.Env, error) {
	opts := []cel.EnvOption{
		cel.StdLib(cel.StdLibSubset(stdlibSubset())),
		// A malformed expression is the common case while an author types; bound the parser
		// so one bad 2 KiB source cannot report dozens of errors, nest without limit, or
		// outgrow the cap.
		cel.ParserExpressionSizeLimit(MaxExpressionBytes),
		cel.ParserRecursionLimit(32),
		cel.ParserErrorRecoveryLimit(4),
		cel.CrossTypeNumericComparisons(true),
		cel.Variable(VarFrame, cel.BytesType),
		cel.Variable(VarFPort, cel.IntType),
		cel.Variable(VarFCnt, cel.IntType),
		cel.Variable(VarJSON, cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable(VarDecoded, cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable(VarLink, cel.MapType(cel.StringType, cel.DoubleType)),
	}
	opts = append(opts, arithmetic()...)
	opts = append(opts, mathFunctions()...)
	for i := range ownedFuncs {
		f := &ownedFuncs[i]
		opts = append(opts, cel.Function(f.name, cel.Overload(f.overload, f.args, f.ret, f.bind)))
	}
	env, err := cel.NewCustomEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("payloaddecode: build base environment: %w", err)
	}
	return env, nil
})
