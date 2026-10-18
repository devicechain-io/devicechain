// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"testing"
)

// benchFormulas are four calculated metrics of the kind a meter profile carries: arithmetic
// with an int literal, a unit conversion, a math function, and a guarded boolean.
var benchFormulas = []Definition{
	{Key: "power_kw", Expression: "voltage * current / 1000", Type: Double},
	{Key: "temp_f", Expression: "temp * 1.8 + 32", Type: Double},
	{Key: "apparent", Expression: "math.sqrt(voltage * voltage + current * current)", Type: Double},
	{Key: "over_temp", Expression: "temp > 80 && (has(m.fan) ? fan == 0 : true)", Type: Boolean},
}

// benchSample is one reading's raw values, as the resolver receives them.
var benchSample = []struct{ key, value string }{
	{"voltage", "230.5"}, {"current", "12.25"}, {"temp", "81.25"}, {"fan", "0"},
}

func benchCompile(b *testing.B, n int) (*Env, []*Formula) {
	b.Helper()
	env := fixtureEnv(b)
	fs := make([]*Formula, n)
	for i := range fs {
		f, err := env.Compile(benchFormulas[i])
		if err != nil {
			b.Fatal(err)
		}
		fs[i] = f
	}
	return env, fs
}

// benchEval is the per-sample work the resolver will add for n calculated metrics: take a
// pooled Sample, parse and fill the inputs, evaluate and format each formula, release.
func benchEval(b *testing.B, n int) {
	env, fs := benchCompile(b, n)
	idx := make([]int, len(benchSample))
	for i, kv := range benchSample {
		idx[i], _ = env.Index(kv.key)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s := env.Sample()
		for i, kv := range benchSample {
			_ = s.SetAt(idx[i], kv.value)
		}
		for _, f := range fs {
			if r := f.Eval(s); !r.OK() {
				b.Fatalf("%s skipped: %s %v", f.Key(), r.Skip, r.Err)
			}
		}
		s.Release()
	}
}

// BenchmarkEval_One / _Four: the whole per-sample cost of 1 and 4 calculated metrics.
func BenchmarkEval_One(b *testing.B)  { benchEval(b, 1) }
func BenchmarkEval_Four(b *testing.B) { benchEval(b, 4) }

// BenchmarkSampleFill isolates parsing and filling the four inputs, with no evaluation.
func BenchmarkSampleFill(b *testing.B) {
	env := fixtureEnv(b)
	idx := make([]int, len(benchSample))
	for i, kv := range benchSample {
		idx[i], _ = env.Index(kv.key)
	}
	b.ReportAllocs()
	for b.Loop() {
		s := env.Sample()
		for i, kv := range benchSample {
			_ = s.SetAt(idx[i], kv.value)
		}
		s.Release()
	}
}

// BenchmarkFormatDouble is the stored-value formatting of one double result.
func BenchmarkFormatDouble(b *testing.B) {
	v := 2.823625
	b.ReportAllocs()
	for b.Loop() {
		_ = FormatDouble(v)
	}
}

// BenchmarkCompile is the once-per-cache-fill cost: an Env for the profile plus four
// formulas. It is paid when a published profile resolution is built, never per event.
func BenchmarkCompile(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		env, err := NewEnv(fixtureInputs)
		if err != nil {
			b.Fatal(err)
		}
		for _, def := range benchFormulas {
			if _, err := env.Compile(def); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkEval_Const is BenchmarkEval_One with the formula reading a device constant
// (`voltage * current / 1000 * device.calibration`), the constants built once per event as
// the resolver builds them and handed to every sample: the per-sample cost of a constant
// over a formula that has none.
func BenchmarkEval_Const(b *testing.B) {
	env := fixtureEnv(b)
	f, err := env.Compile(Definition{Key: "power_kw", Type: Double,
		Expression: "voltage * current / 1000 * device.calibration"})
	if err != nil {
		b.Fatal(err)
	}
	idx := make([]int, len(benchSample))
	for i, kv := range benchSample {
		idx[i], _ = env.Index(kv.key)
	}
	consts := []Constant{NewConstant("calibration", 1.02)}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s := env.Sample()
		for i, kv := range benchSample {
			_ = s.SetAt(idx[i], kv.value)
		}
		s.UseConstants(consts)
		if r := f.Eval(s); !r.OK() {
			b.Fatalf("skipped: %s %v", r.Skip, r.Err)
		}
		s.Release()
	}
}
