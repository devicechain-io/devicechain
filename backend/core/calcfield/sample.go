// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"fmt"
	"math"
	"reflect"
	"strconv"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/interpreter"
)

// Sample is the input values of one sample entry, as the activation every formula of an Env
// evaluates against. It is slice-backed — indexed by the Env's input positions, so filling it
// builds no map — and pooled: take one with Env.Sample, fill it, evaluate every formula, and
// Release it.
//
// A Sample is NOT safe for concurrent use while it is being filled or reset. Evaluation only
// reads it.
type Sample struct {
	env *Env
	// vals holds each input's value, boxed once at Set; nil means absent.
	vals []ref.Val
	// bad is why an input that was set holds no value: SkipInvalidInput when it did not parse
	// as its declared type, SkipNonFinite for a DOUBLE that parsed to NaN or ±Inf. SkipNone
	// for an input that is present or simply absent.
	bad []SkipReason
	// consts are the device's constants (UseConstants), held not copied; nil for a profile
	// whose formulas read none.
	consts []Constant
}

// Sample takes an empty Sample for this Env from its pool.
func (e *Env) Sample() *Sample {
	return e.pool.Get().(*Sample)
}

// Release clears s and returns it to its Env's pool. s must not be used afterwards.
func (s *Sample) Release() {
	s.Reset()
	s.env.pool.Put(s)
}

// Reset clears every value, leaving s empty for the next sample entry.
func (s *Sample) Reset() {
	clear(s.vals)
	clear(s.bad)
	s.consts = nil
}

// Set records the raw string value of the input with the given key, parsing it once as the
// input's declared type with the same parsers the measurement validator uses (ParseFloat,
// base-10 ParseInt, ParseBool). A key that is not an input is ignored and reports false.
// A value that does not parse leaves the input marked invalid and returns the parse error: a
// formula that reads it is skipped as SkipInvalidInput, never fed a zero.
func (s *Sample) Set(key, raw string) (bool, error) {
	i, ok := s.env.index[key]
	if !ok {
		return false, nil
	}
	return true, s.SetAt(i, raw)
}

// clearAt makes input i absent again, as if it had never been set.
func (s *Sample) clearAt(i int) { s.vals[i], s.bad[i] = nil, SkipNone }

// SetAt is Set by input position (Env.Index), for a caller that resolved the position once.
// A DOUBLE that parses to NaN or ±Inf is refused the same way, and a formula that reads it is
// skipped as SkipNonFinite.
func (s *Sample) SetAt(i int, raw string) error {
	in := s.env.inputs[i]
	s.vals[i], s.bad[i] = nil, SkipNone
	switch in.Type {
	case Double:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			s.bad[i] = SkipInvalidInput
			return fmt.Errorf("calcfield: input %q: %q is not a valid DOUBLE", in.Key, raw)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			// The measurement validator admits "NaN" and "Inf" (ParseFloat does), but no double
			// in an evaluation is ever non-finite: a comparison would turn one into a plausible
			// false (NaN > 80) or true (+Inf > 80).
			s.bad[i] = SkipNonFinite
			return fmt.Errorf("calcfield: input %q: %q is not a finite DOUBLE", in.Key, raw)
		}
		s.vals[i] = types.Double(f)
	case Integer:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			s.bad[i] = SkipInvalidInput
			return fmt.Errorf("calcfield: input %q: %q is not a valid INT", in.Key, raw)
		}
		s.vals[i] = types.Int(n)
	case Boolean:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			s.bad[i] = SkipInvalidInput
			return fmt.Errorf("calcfield: input %q: %q is not a valid BOOLEAN", in.Key, raw)
		}
		s.vals[i] = types.Bool(b)
	}
	return nil
}

// inputError is the evaluation error of a READ of an absent or unparseable input. It
// propagates through CEL like any other error, so it decides the skip reason exactly when the
// failed read reaches the result, and not when && or || absorbed it into a value.
type inputError struct {
	key string
	// reason is SkipMissingInput, SkipInvalidInput or SkipNonFinite.
	reason SkipReason
}

func (e *inputError) Error() string {
	switch e.reason {
	case SkipInvalidInput:
		return fmt.Sprintf("calcfield: input %q did not parse as its declared type", e.key)
	case SkipNonFinite:
		return fmt.Sprintf("calcfield: input %q is not finite", e.key)
	}
	return fmt.Sprintf("calcfield: input %q is absent", e.key)
}

// absentReason is why input i, which holds no value, cannot be read.
func (s *Sample) absentReason(i int) SkipReason {
	if r := s.bad[i]; r != SkipNone {
		return r
	}
	return SkipMissingInput
}

// value returns input i's value for an evaluation that READS it or, when it is absent or
// unparseable, the CEL error saying so.
func (s *Sample) value(i int) ref.Val {
	if v := s.vals[i]; v != nil {
		return v
	}
	return types.WrapErr(&inputError{key: s.env.inputs[i].Key, reason: s.absentReason(i)})
}

// present reports whether input i holds a usable value, for a presence test: has(m.x) on an
// absent x is the guard working, not a missing input.
func (s *Sample) present(i int) bool { return s.vals[i] != nil }

// ResolveName implements interpreter.Activation. `m` resolves to the sample map; an input
// identifier resolves to its value or, when absent or unparseable, to the inputError CEL then
// propagates as the attribute's error.
func (s *Sample) ResolveName(name string) (any, bool) {
	switch name {
	case VarM:
		return sampleMap{s: s}, true
	case VarDevice:
		return constMap{s: s}, true
	}
	i, ok := s.env.index[name]
	if !ok {
		return nil, false
	}
	return s.value(i), true
}

// Parent implements interpreter.Activation: a Sample has no enclosing scope.
func (*Sample) Parent() interpreter.Activation { return nil }

// sampleMapType is the runtime type of `m`. It is not a CEL map — it carries only the traits
// the allowlist lets an expression use: a constant-key read (Indexer), a has() presence test
// (FieldTester) and `"k" in m` (Container). Whole-map operations are refused at compile.
var sampleMapType = types.NewObjectType("calcfield.sample",
	traits.IndexerType, traits.FieldTesterType, traits.ContainerType)

// sampleMap is `m`: every input of the Sample, by key. It is one pointer wide, so binding it
// in ResolveName boxes without allocating.
type sampleMap struct{ s *Sample }

// position resolves a CEL key value to an input position.
func (m sampleMap) position(key ref.Val) (int, bool) {
	k, ok := key.(types.String)
	if !ok {
		return 0, false
	}
	i, ok := m.s.env.index[string(k)]
	return i, ok
}

// Get implements traits.Indexer: a READ of an input.
func (m sampleMap) Get(key ref.Val) ref.Val {
	i, ok := m.position(key)
	if !ok {
		return types.NewErr("no such key: %v", key)
	}
	return m.s.value(i)
}

// IsSet implements traits.FieldTester for has(m.key).
func (m sampleMap) IsSet(key ref.Val) ref.Val {
	i, ok := m.position(key)
	return types.Bool(ok && m.s.present(i))
}

// Contains implements traits.Container for `"key" in m`.
func (m sampleMap) Contains(key ref.Val) ref.Val { return m.IsSet(key) }

// ConvertToNative implements ref.Val. `m` is never converted; the allowlist confines it to
// the three uses above.
func (m sampleMap) ConvertToNative(reflect.Type) (any, error) {
	return nil, fmt.Errorf("calcfield: the sample map cannot be converted")
}

// ConvertToType implements ref.Val.
func (m sampleMap) ConvertToType(t ref.Type) ref.Val {
	if t == types.TypeType {
		return sampleMapType
	}
	return types.NewErr("calcfield: the sample map cannot be converted to %v", t)
}

// Equal implements ref.Val; refused at compile, so never reached by a formula.
func (m sampleMap) Equal(ref.Val) ref.Val { return types.NoSuchOverloadErr() }

// Type implements ref.Val.
func (m sampleMap) Type() ref.Type { return sampleMapType }

// Value implements ref.Val.
func (m sampleMap) Value() any { return m.s }
