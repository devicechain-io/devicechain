// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureInputs is a meter profile's measured metrics: identifier keys of every type, and
// keys that cannot be identifiers — one with a dash, one a CEL keyword, one the map's own name.
var fixtureInputs = []Input{
	{Key: "voltage", Type: Double},
	{Key: "current", Type: Double},
	{Key: "temp", Type: Double},
	{Key: "fan", Type: Integer},
	{Key: "door", Type: Boolean},
	{Key: "a-b", Type: Double},
	{Key: "count-x", Type: Integer},
	{Key: "in", Type: Double},
	{Key: "m", Type: Boolean},
}

func fixtureEnv(t testing.TB) *Env {
	t.Helper()
	env, err := NewEnv(fixtureInputs)
	require.NoError(t, err)
	return env
}

// eval compiles expr as a metric of type ty and evaluates it against values.
func eval(t *testing.T, env *Env, expr string, ty Type, values map[string]string) Result {
	t.Helper()
	f, err := env.Compile(Definition{Key: "out", Expression: expr, Type: ty})
	require.NoError(t, err, "compile %q", expr)
	s := env.Sample()
	defer s.Release()
	for k, v := range values {
		known, err := s.Set(k, v)
		require.True(t, known, "fixture key %q is not an input", k)
		require.NoError(t, err, "fixture value %q=%q", k, v)
	}
	return f.Eval(s)
}

// TestEvalValues pins the exact stored bytes of formulas the spec and its users write. The
// expectations are literals, not recomputed with the formatter under test.
func TestEvalValues(t *testing.T) {
	env := fixtureEnv(t)
	cases := []struct {
		name   string
		expr   string
		ty     Type
		values map[string]string
		want   string
	}{
		{"power: double by double over an int literal", "voltage * current / 1000", Double,
			map[string]string{"voltage": "230.5", "current": "12.25"}, "2.823625"},
		{"celsius to fahrenheit with an int literal", "temp * 1.8 + 32", Double,
			map[string]string{"temp": "21.5"}, "70.7"},
		{"shortest round-trip, not a rounded display", "0.1 + temp", Double,
			map[string]string{"temp": "0.2"}, "0.30000000000000004"},
		{"an integral double has no trailing zero", "voltage * 2.0", Double,
			map[string]string{"voltage": "1.5"}, "3"},
		{"a large double uses an exponent", "voltage * 1e20", Double,
			map[string]string{"voltage": "12.5"}, "1.25e+21"},
		{"negative zero is stored as 0", "temp * -1.0", Double,
			map[string]string{"temp": "0"}, "0"},
		{"int promoted in subtraction", "fan - 0.5", Double,
			map[string]string{"fan": "2"}, "1.5"},
		{"int arithmetic stays int", "fan * 2 + 1", Integer,
			map[string]string{"fan": "21"}, "43"},
		{"int modulus", "fan % 4", Integer,
			map[string]string{"fan": "10"}, "2"},
		{"explicit int() truncates toward zero", "int(temp)", Integer,
			map[string]string{"temp": "-2.75"}, "-2"},
		{"boolean true stores 1", "temp > 80 && fan == 0", Boolean,
			map[string]string{"temp": "90", "fan": "0"}, "1"},
		{"boolean false stores 0", "temp > 80 && fan == 0", Boolean,
			map[string]string{"temp": "70", "fan": "0"}, "0"},
		{"boolean input", "door ? 1 : 0", Integer,
			map[string]string{"door": "true"}, "1"},
		{"boolean input in the resolver's 1/0 form", "!door", Boolean,
			map[string]string{"door": "0"}, "1"},
		{"guarded absent input takes the author's default", "has(m.fan) ? fan : 0", Integer,
			map[string]string{}, "0"},
		{"guarded present input", "has(m.fan) ? fan : 0", Integer,
			map[string]string{"fan": "7"}, "7"},
		{"guard with && short-circuits an absent input", "has(m.fan) && fan > 0", Boolean,
			map[string]string{}, "0"},
		{"non-identifier key read through m", `double(m["a-b"]) * 2.0`, Double,
			map[string]string{"a-b": "1.25"}, "2.5"},
		{"non-identifier int key", `int(m["count-x"]) + 1`, Integer,
			map[string]string{"count-x": "41"}, "42"},
		{"presence of a non-identifier key", `"a-b" in m ? double(m["a-b"]) : -1.0`, Double,
			map[string]string{}, "-1"},
		{"keyword key", `double(m["in"]) + 1.0`, Double,
			map[string]string{"in": "1"}, "2"},
		{"the key m itself", `bool(m["m"]) && door`, Boolean,
			map[string]string{"m": "true", "door": "1"}, "1"},
		{"identifier key also reachable through m", `double(m["voltage"]) + voltage`, Double,
			map[string]string{"voltage": "2"}, "4"},
		{"math.abs double", "math.abs(temp)", Double, map[string]string{"temp": "-3.5"}, "3.5"},
		{"math.abs int", "math.abs(fan)", Integer, map[string]string{"fan": "-3"}, "3"},
		{"math.floor", "math.floor(temp)", Double, map[string]string{"temp": "-2.5"}, "-3"},
		{"math.ceil", "math.ceil(temp)", Double, map[string]string{"temp": "-2.5"}, "-2"},
		{"math.round is half away from zero", "math.round(temp)", Double, map[string]string{"temp": "-2.5"}, "-3"},
		{"math.round positive half", "math.round(temp)", Double, map[string]string{"temp": "2.5"}, "3"},
		{"math.sqrt of an int", "math.sqrt(fan)", Double, map[string]string{"fan": "16"}, "4"},
		{"math.pow mixed", "math.pow(voltage, 2)", Double, map[string]string{"voltage": "1.5"}, "2.25"},
		{"math.min int", "math.min(fan, 3)", Integer, map[string]string{"fan": "9"}, "3"},
		{"math.max mixed", "math.max(fan, 2.5)", Double, map[string]string{"fan": "1"}, "2.5"},
		{"math.clamp double", "math.clamp(temp, 0.0, 100.0)", Double, map[string]string{"temp": "140"}, "100"},
		{"math.clamp int", "math.clamp(fan, 0, 3)", Integer, map[string]string{"fan": "-4"}, "0"},
		{"math.clamp int with equal bounds", "math.clamp(fan, 3, 3)", Integer, map[string]string{"fan": "-4"}, "3"},
		{"math.clamp double with equal bounds", "math.clamp(temp, 2.5, 2.5)", Double, map[string]string{"temp": "9"}, "2.5"},
		{"inclusive range bound passes", "temp", Double, map[string]string{"temp": "100"}, "100"},
		// fan is guarded (has) somewhere, so it is not required; on a hot reading the || is
		// true whichever side is written first, although only one order ever reads fan.
		{"a guarded absent input absorbed by || (read first)", "fan > 1 || temp > 80.0 || has(m.fan)", Boolean,
			map[string]string{"temp": "90"}, "1"},
		{"a guarded absent input absorbed by || (never reached)", "temp > 80.0 || fan > 1 || has(m.fan)", Boolean,
			map[string]string{"temp": "90"}, "1"},
		{"a guarded absent input absorbed by && (read first)", "fan > 1 && temp > 80.0 && has(m.fan)", Boolean,
			map[string]string{"temp": "70"}, "0"},
		{"a guarded absent input absorbed by && (never reached)", "temp > 80.0 && fan > 1 && has(m.fan)", Boolean,
			map[string]string{"temp": "70"}, "0"},
		{"a non-finite intermediate absorbed by ||", "voltage / current > 10.0 || door", Boolean,
			map[string]string{"voltage": "1", "current": "0", "door": "true"}, "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := eval(t, env, c.expr, c.ty, c.values)
			require.Equal(t, SkipNone, r.Skip, "skipped: %v", r.Err)
			assert.True(t, r.OK())
			assert.Equal(t, c.want, r.Value)
		})
	}
}

// TestEvalSkips pins the skip-reason policy: no value, and the one bounded reason.
func TestEvalSkips(t *testing.T) {
	env := fixtureEnv(t)
	cases := []struct {
		name   string
		expr   string
		ty     Type
		values map[string]string
		want   SkipReason
	}{
		{"an unguarded input is absent", "voltage * current", Double,
			map[string]string{"voltage": "230"}, SkipMissingInput},
		{"absent although CEL would short-circuit to true", "temp > 80 || fan == 0", Boolean,
			map[string]string{"temp": "90"}, SkipMissingInput},
		{"absent with the operands the other way round", "fan == 0 || temp > 80", Boolean,
			map[string]string{"temp": "90"}, SkipMissingInput},
		{"a guarded input read outside its guard", "has(m.fan) || fan > 1", Boolean,
			map[string]string{}, SkipMissingInput},
		{"a guarded non-identifier input read outside its guard", `"a-b" in m || double(m["a-b"]) > 1.0`, Boolean,
			map[string]string{}, SkipMissingInput},
		{"double division by zero is non-finite", "temp / 0", Double,
			map[string]string{"temp": "1"}, SkipNonFinite},
		{"overflowing double is non-finite", "temp * temp", Double,
			map[string]string{"temp": "1e300"}, SkipNonFinite},
		{"NaN is non-finite", "math.sqrt(temp)", Double,
			map[string]string{"temp": "-1"}, SkipNonFinite},
		{"a guarded absent input whose read reaches the result", "fan > 1 || temp > 80.0 || has(m.fan)", Boolean,
			map[string]string{"temp": "70"}, SkipMissingInput},
		{"a double division by zero inside a comparison", "voltage / current > 10.0", Boolean,
			map[string]string{"voltage": "1", "current": "0"}, SkipNonFinite},
		{"0/0 inside a comparison", "voltage / current < 10.0", Boolean,
			map[string]string{"voltage": "0", "current": "0"}, SkipNonFinite},
		{"a double overflow inside a comparison", "temp * temp > 1.0", Boolean,
			map[string]string{"temp": "1e300"}, SkipNonFinite},
		{"a NaN square root inside a comparison", "math.sqrt(temp) > 1.0", Boolean,
			map[string]string{"temp": "-1"}, SkipNonFinite},
		{"pow to +Inf inside a comparison", "math.pow(fan, -1) > 0.0", Boolean,
			map[string]string{"fan": "0"}, SkipNonFinite},
		{"int() of a non-finite double", "int(temp / 0.0)", Integer,
			map[string]string{"temp": "1"}, SkipNonFinite},
		{"a promoted int over a zero double", "fan / temp", Double,
			map[string]string{"fan": "1", "temp": "0"}, SkipNonFinite},
		{"an int over a zero double", "temp / fan", Double,
			map[string]string{"fan": "0", "temp": "1"}, SkipNonFinite},
		{"int division by zero", "fan / 0", Integer, map[string]string{"fan": "1"}, SkipDivZero},
		{"int modulus by zero", "fan % 0", Integer, map[string]string{"fan": "1"}, SkipDivZero},
		{"int addition overflow", "fan + 9223372036854775807", Integer,
			map[string]string{"fan": "1"}, SkipOverflow},
		{"int subtraction overflow", "fan - 1", Integer,
			map[string]string{"fan": "-9223372036854775808"}, SkipOverflow},
		{"int multiplication overflow", "fan * 9223372036854775807", Integer,
			map[string]string{"fan": "2"}, SkipOverflow},
		{"math.abs of the minimum int", "math.abs(fan)", Integer,
			map[string]string{"fan": "-9223372036854775808"}, SkipOverflow},
		{"int() of a double beyond int range", "int(temp)", Integer,
			map[string]string{"temp": "1e30"}, SkipOverflow},
		{"clamp with its bounds reversed", "math.clamp(temp, 10.0, 0.0)", Double,
			map[string]string{"temp": "5"}, SkipEvalError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := eval(t, env, c.expr, c.ty, c.values)
			assert.Equal(t, c.want, r.Skip, "err: %v", r.Err)
			assert.False(t, r.OK())
			assert.Empty(t, r.Value, "a skipped formula must carry no value")
		})
	}
}

// A DOUBLE input that parses to NaN or ±Inf — which the measurement validator admits — is
// refused at Set and skips any formula reading it as non-finite, required or guarded: no
// comparison ever sees one and turns it into a plausible boolean.
func TestNonFiniteInput(t *testing.T) {
	env := fixtureEnv(t)
	for _, raw := range []string{"NaN", "+Inf", "-Inf", "inf", "Infinity"} {
		for _, expr := range []string{"temp > 80.0", "has(m.door) && door || temp > 80.0", "temp != temp"} {
			f, err := env.Compile(Definition{Key: "out", Expression: expr, Type: Boolean})
			require.NoError(t, err)
			s := env.Sample()
			_, _ = s.Set("door", "false")
			known, err := s.Set("temp", raw)
			assert.True(t, known)
			assert.ErrorContains(t, err, "is not a finite DOUBLE")
			r := f.Eval(s)
			assert.Equal(t, Result{Skip: SkipNonFinite}, Result{Value: r.Value, Skip: r.Skip}, "%s with temp=%s", expr, raw)
			s.Release()
		}
	}
	// A guarded read that reaches the result names the input.
	f, err := env.Compile(Definition{Key: "out", Expression: "has(m.temp) || temp > 1.0", Type: Boolean})
	require.NoError(t, err)
	s := env.Sample()
	defer s.Release()
	_, _ = s.Set("temp", "NaN")
	r := f.Eval(s)
	assert.Equal(t, SkipNonFinite, r.Skip)
	assert.ErrorContains(t, r.Err, `input "temp" is not finite`)
}

// An input whose value did not parse is reported as invalid, not missing, whether the formula
// requires it or guards it — and Set says so.
func TestInvalidInput(t *testing.T) {
	env := fixtureEnv(t)
	for _, expr := range []string{"voltage * current", "has(m.current) ? voltage : 0.0 + current"} {
		f, err := env.Compile(Definition{Key: "out", Expression: expr, Type: Double})
		require.NoError(t, err)
		s := env.Sample()
		_, err = s.Set("voltage", "1")
		require.NoError(t, err)
		known, err := s.Set("current", "abc")
		assert.True(t, known)
		assert.ErrorContains(t, err, `"abc" is not a valid DOUBLE`)
		assert.Equal(t, SkipInvalidInput, f.Eval(s).Skip, expr)
		s.Release()
	}
	// The same unparseable value does not affect a formula that never names it.
	f, err := env.Compile(Definition{Key: "out", Expression: "voltage * 2.0", Type: Double})
	require.NoError(t, err)
	s := env.Sample()
	defer s.Release()
	_, _ = s.Set("voltage", "1")
	_, _ = s.Set("current", "abc")
	assert.Equal(t, Result{Value: "2"}, f.Eval(s))

	// Int and boolean inputs parse as the validator does.
	_, err = s.Set("fan", "1.5")
	assert.ErrorContains(t, err, "not a valid INT")
	_, err = s.Set("door", "yes")
	assert.ErrorContains(t, err, "not a valid BOOLEAN")
	for _, raw := range []string{"0x10", "1_000", "010e"} {
		_, err = s.Set("fan", raw)
		assert.ErrorContains(t, err, "not a valid INT", "an INT is base-10 only, as the validator parses it: %q", raw)
	}
	known, err := s.Set("undeclared", "1")
	assert.False(t, known)
	assert.NoError(t, err)
}

// A value outside the definition's bounds is skipped, as a device-sent one would be refused.
func TestRange(t *testing.T) {
	env := fixtureEnv(t)
	lo, hi := 0.0, 100.0
	compile := func(expr string, ty Type) *Formula {
		f, err := env.Compile(Definition{Key: "out", Expression: expr, Type: ty, Min: &lo, Max: &hi})
		require.NoError(t, err)
		return f
	}
	s := env.Sample()
	defer s.Release()
	_, _ = s.Set("temp", "60")
	_, _ = s.Set("fan", "-1")
	assert.Equal(t, SkipRange, compile("temp * 2.0", Double).Eval(s).Skip, "above max")
	assert.Equal(t, Result{Value: "100"}, compile("temp + 40.0", Double).Eval(s), "max is inclusive")
	assert.Equal(t, SkipRange, compile("fan", Integer).Eval(s).Skip, "int below min")
	assert.Equal(t, Result{Value: "0"}, compile("fan + 1", Integer).Eval(s), "min is inclusive")
	// Bounds do not apply to a boolean.
	assert.Equal(t, Result{Value: "1"}, compile("temp > 50", Boolean).Eval(s))
}

// TestCompileRefusals pins every class of formula the gate refuses, with the author-facing
// message fragment that says why.
func TestCompileRefusals(t *testing.T) {
	env := fixtureEnv(t)
	cases := []struct {
		name string
		def  Definition
		want string
	}{
		{"no type", Definition{Key: "out", Expression: "voltage"}, "is not DOUBLE, INT or BOOLEAN"},
		{"empty", Definition{Key: "out", Expression: "", Type: Double}, "the formula is empty"},
		{"too long", Definition{Key: "out", Expression: "voltage" + strings.Repeat(" ", MaxExpressionBytes-6), Type: Double},
			"1025 bytes, above the maximum of 1024"},
		{"key collides with an input", Definition{Key: "temp", Expression: "voltage", Type: Double},
			"already a measured metric"},
		{"parse error", Definition{Key: "out", Expression: "voltage *", Type: Double}, "Syntax error"},
		{"undeclared identifier", Definition{Key: "out", Expression: "power * 2.0", Type: Double},
			"undeclared reference to 'power'"},
		{"a calculated metric is not an input", Definition{Key: "out", Expression: "double(m[\"power\"])", Type: Double},
			`"power" is not a declared measured metric`},
		{"int result for a DOUBLE metric", Definition{Key: "out", Expression: "fan * 2", Type: Double},
			"produces int but the metric is DOUBLE; wrap it in double(...)"},
		{"double result for an INT metric", Definition{Key: "out", Expression: "voltage * current / 1000", Type: Integer},
			"produces double but the metric is INT; wrap it in int(...)"},
		{"double result for a BOOLEAN metric", Definition{Key: "out", Expression: "voltage", Type: Boolean},
			"produces double but the metric is BOOLEAN"},
		{"constant formula", Definition{Key: "out", Expression: "1.0 + 2.0", Type: Double}, "reads no input"},
		{"double == int needs a decimal point", Definition{Key: "out", Expression: "temp == 80", Type: Boolean},
			"hint: an int and a double do not mix here"},
		{"ternary branches of int and double", Definition{Key: "out", Expression: "has(m.fan) ? temp : 0", Type: Double},
			"hint: an int and a double do not mix here"},
		{"string literal", Definition{Key: "out", Expression: `temp > 1.0 && "a" == "a"`, Type: Boolean},
			`string "a" is not allowed`},
		{"string function", Definition{Key: "out", Expression: `size("x") > 0 && temp > 1.0`, Type: Boolean},
			"undeclared reference to 'size'"},
		{"time", Definition{Key: "out", Expression: "now() > 0 && temp > 1.0", Type: Boolean},
			"undeclared reference to 'now'"},
		{"timestamp", Definition{Key: "out", Expression: `timestamp("2026-01-01T00:00:00Z") > timestamp("2025-01-01T00:00:00Z") && temp > 1.0`, Type: Boolean},
			"undeclared reference to 'timestamp'"},
		{"regex", Definition{Key: "out", Expression: `matches("a", "a") && temp > 1.0`, Type: Boolean},
			"undeclared reference to 'matches'"},
		{"comprehension macro", Definition{Key: "out", Expression: "[temp].all(x, x > 0.0)", Type: Boolean},
			"undeclared reference to 'all'"},
		{"cel.bind", Definition{Key: "out", Expression: "cel.bind(x, temp, x > 0.0)", Type: Boolean},
			"undeclared reference to 'cel'"},
		{"list literal", Definition{Key: "out", Expression: "temp in [1.0, 2.0]", Type: Boolean},
			"`in` is only allowed as \"key\" in m"},
		{"list index", Definition{Key: "out", Expression: "[temp, 2.0][0]", Type: Double}, "indexing is only allowed as m[\"key\"]"},
		{"map literal", Definition{Key: "out", Expression: `{"k": temp}["k"]`, Type: Double}, "indexing is only allowed"},
		{"whole map", Definition{Key: "out", Expression: "m == m && temp > 0.0", Type: Boolean},
			`m may only be used as m["key"]`},
		{"size of m", Definition{Key: "out", Expression: "size(m) > 0 && temp > 0.0", Type: Boolean},
			"undeclared reference to 'size'"},
		{"dynamic key", Definition{Key: "out", Expression: `double(m[door ? "temp" : "voltage"])`, Type: Double},
			"m must be indexed by a constant string key"},
		{"unconverted read through m", Definition{Key: "out", Expression: `m["a-b"] > 1.0`, Type: Boolean},
			`read the DOUBLE input "a-b" as double(m["a-b"])`},
		{"wrong conversion through m", Definition{Key: "out", Expression: `int(m["a-b"]) > 1`, Type: Boolean},
			`read the DOUBLE input "a-b" as double(m["a-b"])`},
		{"conversion not directly around the read", Definition{Key: "out", Expression: `double(m["a-b"] + 1.0)`, Type: Double},
			`read the DOUBLE input "a-b" as double(m["a-b"])`},
		{"select on m of an undeclared key", Definition{Key: "out", Expression: "has(m.power)", Type: Boolean},
			`"power" is not a declared measured metric`},
		{"uint literal", Definition{Key: "out", Expression: "fan > 1u", Type: Boolean}, "of type uint is not allowed"},
		{"null", Definition{Key: "out", Expression: "temp == null", Type: Boolean}, "found no matching overload"},
		{"bytes", Definition{Key: "out", Expression: `b"x" == b"x" && temp > 0.0`, Type: Boolean}, "of type bytes is not allowed"},
		{"dyn", Definition{Key: "out", Expression: "dyn(temp) > 0.0", Type: Boolean}, "undeclared reference to 'dyn'"},
		{"type()", Definition{Key: "out", Expression: "type(temp) == double", Type: Boolean}, "undeclared reference to 'type'"},
		{"member call", Definition{Key: "out", Expression: "temp.abs()", Type: Double}, "undeclared reference to 'abs'"},
		{"math function not in the set", Definition{Key: "out", Expression: "math.greatest(temp, 1.0)", Type: Double},
			"undeclared reference"},
		{"wrong arity", Definition{Key: "out", Expression: "math.min(temp, 1.0, 2.0)", Type: Double}, "found no matching overload"},
		{"fractional power", Definition{Key: "out", Expression: "math.pow(temp, 0.5)", Type: Double},
			"hint: math.pow takes an int exponent"},
		{"double exponent", Definition{Key: "out", Expression: "math.pow(fan, temp)", Type: Double},
			"hint: math.pow takes an int exponent"},
		{"list literals compared", Definition{Key: "out", Expression: "[temp] == [temp]", Type: Boolean}, "lists are not allowed"},
		{"map literals compared", Definition{Key: "out", Expression: "{1: temp} == {1: temp}", Type: Boolean}, "map literals are not allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := env.Compile(c.def)
			require.Error(t, err)
			assert.Nil(t, f)
			assert.ErrorContains(t, err, c.want)
			var ce *CompileError
			assert.True(t, errors.As(err, &ce), "a refusal is a CompileError, got %T", err)
		})
	}
}

// A formula under the byte cap can still exceed the static cost ceiling, and is refused with
// a CostError carrying the estimate — the gate is not the byte cap in disguise.
func TestStaticCostGate(t *testing.T) {
	env := fixtureEnv(t)
	long := "voltage" + strings.Repeat("+voltage", 127) // 1023 bytes, 127 additions
	require.LessOrEqual(t, len(long), MaxExpressionBytes)
	_, err := env.Compile(Definition{Key: "out", Expression: long, Type: Double})
	var ce *CostError
	require.True(t, errors.As(err, &ce), "want a CostError, got %v", err)
	assert.Equal(t, "out", ce.Key)
	assert.Equal(t, CostCeiling, ce.Ceiling)
	assert.Equal(t, uint64(255), ce.EstimatedMax)

	// The boundary: a formula estimating exactly to the ceiling passes, one unit over fails.
	f, err := env.Compile(Definition{Key: "out", Expression: "voltage * current / 1000", Type: Double})
	require.NoError(t, err)
	assert.Equal(t, uint64(4), f.CostMax())
	_, err = env.compile(Definition{Key: "out", Expression: "voltage * current / 1000", Type: Double}, 4)
	assert.NoError(t, err, "at the ceiling")
	_, err = env.compile(Definition{Key: "out", Expression: "voltage * current / 1000", Type: Double}, 3)
	assert.True(t, errors.As(err, &ce), "one over the ceiling")
}

func TestCheckProfileCost(t *testing.T) {
	env := fixtureEnv(t)
	f, err := env.Compile(Definition{Key: "out", Expression: "voltage * current / 1000", Type: Double}) // cost 4
	require.NoError(t, err)
	at := make([]*Formula, ProfileCostCeiling/4)
	for i := range at {
		at[i] = f
	}
	assert.NoError(t, CheckProfileCost(at), "exactly at the profile ceiling")
	err = CheckProfileCost(append(at, f))
	var ce *CostError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, ProfileCostCeiling+4, ce.EstimatedMax)
	assert.Empty(t, ce.Key)
	assert.NoError(t, CheckProfileCost(nil))
}

// The derived input list is every key the formula names, in Env order — whether as a
// variable, a read through m, or a presence test — and is what the resolver will fill.
func TestInputKeys(t *testing.T) {
	env := fixtureEnv(t)
	f, err := env.Compile(Definition{Key: "out", Type: Integer,
		Expression: `has(m.fan) ? fan * 2 : ("count-x" in m ? int(m["count-x"]) : int(temp))`})
	require.NoError(t, err)
	assert.Equal(t, []string{"temp", "fan", "count-x"}, f.InputKeys())
	assert.Equal(t, "out", f.Key())
	assert.Equal(t, Integer, f.Type())
	assert.Equal(t, `has(m.fan) ? fan * 2 : ("count-x" in m ? int(m["count-x"]) : int(temp))`, f.Expression())
}

func TestNewEnvRefusals(t *testing.T) {
	cases := []struct {
		name   string
		inputs []Input
		want   string
	}{
		{"empty key", []Input{{Key: "", Type: Double}}, "empty key"},
		{"invalid type", []Input{{Key: "x"}}, "invalid type"},
		{"duplicate", []Input{{Key: "x", Type: Double}, {Key: "x", Type: Integer}}, "declared twice"},
		{"too many", make([]Input, MaxInputs+1), "exceeds the maximum"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewEnv(c.inputs)
			assert.ErrorIs(t, err, ErrInvalidInputs)
			assert.ErrorContains(t, err, c.want)
		})
	}
	// Exactly MaxInputs is allowed.
	most := make([]Input, MaxInputs)
	for i := range most {
		most[i] = Input{Key: fmt.Sprintf("k%d", i), Type: Double}
	}
	_, err := NewEnv(most)
	require.NoError(t, err, "MaxInputs inputs are allowed")

	// The caller's slice is copied: changing it afterwards does not change the Env.
	in := []Input{{Key: "x", Type: Double}}
	env, err := NewEnv(in)
	require.NoError(t, err)
	in[0].Key = "y"
	assert.Equal(t, []Input{{Key: "x", Type: Double}}, env.Inputs())
	i, ok := env.Index("x")
	assert.True(t, ok)
	assert.Equal(t, 0, i)
	_, ok = env.Index("y")
	assert.False(t, ok)
}

// A released Sample comes back empty: no value of one reading reaches the next.
func TestSampleReuseCarriesNothing(t *testing.T) {
	env := fixtureEnv(t)
	f, err := env.Compile(Definition{Key: "out", Expression: "voltage * 2.0", Type: Double})
	require.NoError(t, err)
	g, err := env.Compile(Definition{Key: "out2", Expression: "voltage * current", Type: Double})
	require.NoError(t, err)
	s := env.Sample()
	_, _ = s.Set("voltage", "3")
	_, _ = s.Set("current", "nope")
	require.Equal(t, Result{Value: "6"}, f.Eval(s))
	s.Release()
	for range 4 { // the pool may or may not hand back the same Sample; neither may carry state
		s = env.Sample()
		assert.Equal(t, SkipMissingInput, f.Eval(s).Skip)
		_, _ = s.Set("voltage", "1")
		assert.Equal(t, SkipMissingInput, g.Eval(s).Skip, "current's invalid mark must not survive Release")
		s.Release()
	}
	// SetAt overwrites a previous value of the same input, valid or not.
	s = env.Sample()
	defer s.Release()
	i, _ := env.Index("voltage")
	require.Error(t, s.SetAt(i, "bad"))
	require.NoError(t, s.SetAt(i, "5"))
	assert.Equal(t, Result{Value: "10"}, f.Eval(s))
	require.Error(t, s.SetAt(i, "bad"))
	assert.Equal(t, SkipInvalidInput, f.Eval(s).Skip, "an unparseable value replaces the previous valid one")
}

// A Sample from a different Env is a caller bug, skipped loudly rather than read.
func TestForeignSample(t *testing.T) {
	a, b := fixtureEnv(t), fixtureEnv(t)
	f, err := a.Compile(Definition{Key: "out", Expression: "voltage * 2.0", Type: Double})
	require.NoError(t, err)
	s := b.Sample()
	defer s.Release()
	_, _ = s.Set("voltage", "1")
	r := f.Eval(s)
	assert.Equal(t, SkipEvalError, r.Skip)
	assert.ErrorIs(t, r.Err, errForeignSample)
}

// One compiled Formula serves many goroutines, each with its own Sample.
func TestConcurrentEval(t *testing.T) {
	env := fixtureEnv(t)
	f, err := env.Compile(Definition{Key: "out", Expression: "voltage * current / 1000", Type: Double})
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan string, 16)
	for g := range 16 {
		wg.Go(func() {
			for n := range 200 {
				s := env.Sample()
				v := fmt.Sprintf("%d", g*1000+n)
				_, _ = s.Set("voltage", v)
				_, _ = s.Set("current", "1000")
				if r := f.Eval(s); r.Value != v {
					// Never block: a wrong value on every evaluation must fail this test, not
					// fill the buffer and hang it until the package timeout.
					select {
					case errs <- fmt.Sprintf("goroutine %d: got %q want %q", g, r.Value, v):
					default:
					}
				}
				s.Release()
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// pointOne and pointTwo are variables so their sum is computed in float64 at run time; Go
// folds 0.1 + 0.2 written as constants exactly, to 0.3.
var pointOne, pointTwo = 0.1, 0.2

// TestFormatDouble pins the stored form of doubles with literal expectations.
func TestFormatDouble(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{2, "2"},
		{-2.5, "-2.5"},
		{pointOne + pointTwo, "0.30000000000000004"}, // runtime float addition, not a folded constant
		{1.0 / 3, "0.3333333333333333"},
		{1e21, "1e+21"},
		{123456789012345678901.0, "1.2345678901234568e+20"},
		{1e-7, "1e-07"},
		{5e-324, "5e-324"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, FormatDouble(c.in), "%v", c.in)
	}
	assert.Equal(t, "-9223372036854775808", FormatInt(math.MinInt64))
	assert.Equal(t, "0", FormatInt(0))
	assert.Equal(t, "1", FormatBool(true))
	assert.Equal(t, "0", FormatBool(false))
}

// repeatable runs produce n times and reports the first output that differs from the first.
// It is the determinism check, factored out so the negative control below can show it fails.
func repeatable(n int, produce func(i int) string) error {
	first := produce(0)
	for i := 1; i < n; i++ {
		if got := produce(i); got != first {
			return fmt.Errorf("run %d produced %q, run 0 produced %q", i, got, first)
		}
	}
	return nil
}

// TestDeterminism: 1000 evaluations of one sample — each on a freshly taken Sample, filled
// in a different key order — store byte-identical values. The stored string is the value of
// record, so this is a correctness requirement.
func TestDeterminism(t *testing.T) {
	env := fixtureEnv(t)
	formulas := map[string]Definition{
		"power": {Key: "power", Expression: "voltage * current / 1000", Type: Double},
		"degF":  {Key: "degF", Expression: "temp * 1.8 + 32", Type: Double},
		"ratio": {Key: "ratio", Expression: "math.sqrt(voltage * voltage + current * current) / 3", Type: Double},
		"over":  {Key: "over", Expression: "temp > 80.0 && (has(m.fan) ? fan == 0 : true)", Type: Boolean},
	}
	values := map[string]string{"voltage": "230.123456789", "current": "12.987654321", "temp": "81.3", "fan": "0"}
	for name, def := range formulas {
		f, err := env.Compile(def)
		require.NoError(t, err)
		err = repeatable(1000, func(int) string {
			s := env.Sample()
			defer s.Release()
			for k, v := range values { // map order varies between runs
				_, _ = s.Set(k, v)
			}
			r := f.Eval(s)
			return string(r.Skip) + "|" + r.Value
		})
		assert.NoError(t, err, name)
	}

	// Negative control: the same check over a formatter whose output depends on something
	// other than the value (here, the run number) must fail — otherwise the check above
	// proves nothing.
	unstable := func(i int) string {
		return fmt.Sprintf("%.*f", 6+i%3, 2.823625)
	}
	assert.Error(t, repeatable(1000, unstable), "the determinism check must catch an unstable formatter")
}

func TestParseType(t *testing.T) {
	for _, ty := range []Type{Double, Integer, Boolean} {
		got, ok := ParseType(ty.String())
		assert.True(t, ok)
		assert.Equal(t, ty, got)
	}
	assert.Equal(t, "INT", Integer.String())
	_, ok := ParseType("STRING")
	assert.False(t, ok, "STRING is device state, never a formula type")
	_, ok = ParseType("double")
	assert.False(t, ok)
	assert.Equal(t, "Type(0)", Type(0).String())
	assert.Equal(t, []SkipReason{"missing_input", "invalid_input", "non_finite", "div_zero", "overflow", "range", "eval_error"}, SkipReasons)
}
