// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evalC compiles expr and evaluates it against values (inputs) and constants (the device's).
func evalC(t *testing.T, env *Env, expr string, ty Type, values map[string]string, consts ...Constant) Result {
	t.Helper()
	f, err := env.Compile(Definition{Key: "out", Expression: expr, Type: ty})
	require.NoError(t, err, "compile %q", expr)
	s := env.Sample()
	defer s.Release()
	for k, v := range values {
		_, err := s.Set(k, v)
		require.NoError(t, err)
	}
	s.UseConstants(consts)
	return f.Eval(s)
}

// TestConstantValues pins the exact stored bytes of formulas reading device constants. The
// expectations are literals.
func TestConstantValues(t *testing.T) {
	env := fixtureEnv(t)
	cal := NewConstant("calibration", 1.02)
	cases := []struct {
		name   string
		expr   string
		ty     Type
		values map[string]string
		consts []Constant
		want   string
	}{
		{"a calibration factor", "voltage * device.calibration", Double,
			map[string]string{"voltage": "230"}, []Constant{cal}, "234.6"},
		{"an index read, for a name that is not an identifier", `voltage * device["ct-ratio"]`, Double,
			map[string]string{"voltage": "2"}, []Constant{NewConstant("ct-ratio", 40)}, "80"},
		{"int mixes with the double constant", "fan * device.calibration", Double,
			map[string]string{"fan": "10"}, []Constant{cal}, "10.2"},
		{"int() reads a whole-number constant as an int", "fan + int(device.step)", Integer,
			map[string]string{"fan": "10"}, []Constant{NewConstant("step", 5)}, "15"},
		{"a constant in a comparison", "temp > device.limit", Boolean,
			map[string]string{"temp": "81"}, []Constant{NewConstant("limit", 80)}, "1"},
		{"two constants", "(temp - device.offset) * device.gain", Double,
			map[string]string{"temp": "20"}, []Constant{NewConstant("offset", 2), NewConstant("gain", 0.5)}, "9"},
		{"a guarded constant present", "voltage * (has(device.calibration) ? device.calibration : 1.0)", Double,
			map[string]string{"voltage": "100"}, []Constant{cal}, "102"},
		{"a guarded constant absent takes the authors default", "voltage * (has(device.calibration) ? device.calibration : 1.0)", Double,
			map[string]string{"voltage": "100"}, nil, "100"},
		{"in device is the same test", `voltage * ("calibration" in device ? device.calibration : 1.0)`, Double,
			map[string]string{"voltage": "100"}, nil, "100"},
		{"a constant text parses as a double", "voltage * device.k", Double,
			map[string]string{"voltage": "2"}, []Constant{ConstantFromString("k", "1.5e1")}, "30"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := evalC(t, env, c.expr, c.ty, c.values, c.consts...)
			require.True(t, r.OK(), "skipped: %s %v", r.Skip, r.Err)
			assert.Equal(t, c.want, r.Value)
		})
	}
}

// TestConstantSkips pins the skip policy for constants: absent, not a number, and not finite
// are skipped before the formula runs, whatever else the formula would have done.
func TestConstantSkips(t *testing.T) {
	env := fixtureEnv(t)
	cases := []struct {
		name   string
		expr   string
		ty     Type
		consts []Constant
		want   SkipReason
	}{
		{"absent constant", "voltage * device.calibration", Double, nil, SkipMissingInput},
		{"absent among others", "voltage * device.a * device.b", Double, []Constant{NewConstant("a", 1)}, SkipMissingInput},
		{"not a number", "voltage * device.calibration", Double, []Constant{InvalidConstant("calibration")}, SkipInvalidInput},
		{"text that is not a number", "voltage * device.k", Double, []Constant{ConstantFromString("k", "abc")}, SkipInvalidInput},
		{"text that parses to NaN", "voltage * device.k", Double, []Constant{ConstantFromString("k", "NaN")}, SkipNonFinite},
		{"infinite", "voltage * device.k", Double, []Constant{NewConstant("k", math.Inf(1))}, SkipNonFinite},
		{"NaN", "voltage * device.k", Double, []Constant{NewConstant("k", math.NaN())}, SkipNonFinite},
		// An unguarded read decides before evaluation, so a short circuit cannot hide it.
		{"short-circuit does not hide a missing constant", "voltage > 0.0 || device.k > 1.0", Boolean, nil, SkipMissingInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := evalC(t, env, c.expr, c.ty, map[string]string{"voltage": "230"}, c.consts...)
			require.False(t, r.OK(), "value %q", r.Value)
			assert.Equal(t, c.want, r.Skip)
		})
	}
}

// TestConstantUnusableIsAbsentToHas: a guarded constant that is unusable reads as absent to
// has(), so the guard's else branch is the author's explicit default.
func TestConstantUnusableIsAbsentToHas(t *testing.T) {
	env := fixtureEnv(t)
	expr := "voltage * (has(device.k) ? device.k : 1.0)"
	for _, c := range []Constant{InvalidConstant("k"), NewConstant("k", math.Inf(-1))} {
		r := evalC(t, env, expr, Double, map[string]string{"voltage": "7"}, c)
		require.True(t, r.OK(), "%v %v", r.Skip, r.Err)
		assert.Equal(t, "7", r.Value)
	}
}

// TestConstantAloneIsRefused: a formula over constants and literals reads no input.
func TestConstantAloneIsRefused(t *testing.T) {
	env := fixtureEnv(t)
	_, err := env.Compile(Definition{Key: "out", Expression: "device.a * 2.0", Type: Double})
	require.Error(t, err)
	assert.ErrorContains(t, err, "reads no input")
}

func TestConstantCompileRefusals(t *testing.T) {
	env := fixtureEnv(t)
	long := strings.Repeat("k", MaxConstantKeyBytes+1)
	var many strings.Builder
	many.WriteString("voltage")
	for i := 0; i <= MaxConstants; i++ {
		fmt.Fprintf(&many, " + device.c%d", i)
	}
	var okMany strings.Builder
	okMany.WriteString("voltage")
	for i := 0; i < MaxConstants; i++ {
		fmt.Fprintf(&okMany, " + device.c%d", i)
	}
	cases := []struct{ name, expr, want string }{
		{"the bare map", "voltage + (device == device ? 1.0 : 0.0)", "device may only be used as"},
		{"a non-constant index", `voltage + device[string(voltage)]`, ""},
		{"a name too long", fmt.Sprintf("voltage + device[%q]", long), "device constant name must be"},
		{"an empty name", `voltage + device[""]`, "device constant name must be"},
		{"too many constants", many.String(), "above the maximum of"},
		{"an int formula does not read a double constant", "fan + device.k", "produces double"},
		{"a field of a constant", "voltage + device.k.x", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ty := Double
			if c.name == "an int formula does not read a double constant" {
				ty = Integer
			}
			_, err := env.Compile(Definition{Key: "out", Expression: c.expr, Type: ty})
			require.Error(t, err)
			if c.want != "" {
				assert.ErrorContains(t, err, c.want)
			}
		})
	}
	_, err := env.Compile(Definition{Key: "out", Expression: okMany.String(), Type: Double})
	assert.NoError(t, err, "exactly MaxConstants constants is admitted")
}

// TestConstantKeys: a formula reports the constants it reads, sorted and distinct, guarded
// ones included, and none when it reads none.
func TestConstantKeys(t *testing.T) {
	env := fixtureEnv(t)
	f, err := env.Compile(Definition{Key: "out", Type: Double,
		Expression: `voltage * device.b + device["a-b"] + (has(device.c) ? device.c : 0.0) + device.b`})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-b", "b", "c"}, f.ConstantKeys())
	g, err := env.Compile(Definition{Key: "out2", Type: Double, Expression: "voltage * 2.0"})
	require.NoError(t, err)
	assert.Empty(t, g.ConstantKeys())
	// The returned slice is a copy.
	keys := f.ConstantKeys()
	keys[0] = "mutated"
	assert.Equal(t, []string{"a-b", "b", "c"}, f.ConstantKeys())
}

// TestDeviceIsAReservedName: a measured metric called "device" cannot shadow the constants
// map; it is an input still, reached as m["device"].
func TestDeviceIsAReservedName(t *testing.T) {
	env, err := NewEnv([]Input{{Key: "device", Type: Double}, {Key: "voltage", Type: Double}})
	require.NoError(t, err)
	r := evalC(t, env, `double(m["device"]) + voltage + device.k`, Double,
		map[string]string{"device": "1", "voltage": "2"}, NewConstant("k", 4))
	// m["device"] must be read through the conversion for its type; the bare name is the map.
	require.True(t, r.OK(), "%v %v", r.Skip, r.Err)
	assert.Equal(t, "7", r.Value)
}

// TestSampleReuseCarriesNoConstants: a released sample holds nothing of its device, so the
// next one sees every constant as absent until it is given its own.
func TestSampleReuseCarriesNoConstants(t *testing.T) {
	env := fixtureEnv(t)
	f, err := env.Compile(Definition{Key: "out", Expression: "voltage * device.k", Type: Double})
	require.NoError(t, err)
	for range 50 {
		s := env.Sample()
		_, _ = s.Set("voltage", "2")
		s.UseConstants([]Constant{NewConstant("k", 3)})
		require.Equal(t, "6", f.Eval(s).Value)
		s.Release()

		s = env.Sample()
		_, _ = s.Set("voltage", "2")
		r := f.Eval(s)
		require.Equal(t, SkipMissingInput, r.Skip, "the previous device's constant leaked")
		s.Release()
	}
}

// TestConstantsConcurrent: constants are read-only data shared by every sample of an event;
// concurrent evaluation of one formula over one slice is race-free.
func TestConstantsConcurrent(t *testing.T) {
	env := fixtureEnv(t)
	f, err := env.Compile(Definition{Key: "out", Expression: "voltage * device.k", Type: Double})
	require.NoError(t, err)
	shared := []Constant{NewConstant("k", 0.5)}
	done := make(chan struct{})
	for g := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 500 {
				s := env.Sample()
				_, _ = s.Set("voltage", fmt.Sprint(g+1))
				s.UseConstants(shared)
				if r := f.Eval(s); !r.OK() {
					panic(r.Skip)
				}
				s.Release()
			}
		}()
	}
	for range 8 {
		<-done
	}
}

func TestCheckProfileConstants(t *testing.T) {
	env := fixtureEnv(t)
	compile := func(first, n int) *Formula {
		var sb strings.Builder
		sb.WriteString("voltage")
		for i := first; i < first+n; i++ {
			fmt.Fprintf(&sb, " + device.c%d", i)
		}
		f, err := env.Compile(Definition{Key: fmt.Sprintf("m%d", first), Expression: sb.String(), Type: Double})
		require.NoError(t, err)
		return f
	}
	// Four formulas of eight distinct constants is exactly the maximum; the same constants
	// read twice count once.
	var fs []*Formula
	for i := range MaxProfileConstants / MaxConstants {
		fs = append(fs, compile(i*MaxConstants, MaxConstants))
	}
	require.NoError(t, CheckProfileConstants(fs))
	require.NoError(t, CheckProfileConstants(append(fs, compile(0, MaxConstants))))
	err := CheckProfileConstants(append(fs, compile(MaxProfileConstants, 1)))
	require.Error(t, err)
	assert.ErrorContains(t, err, "33 distinct device constants")
}
