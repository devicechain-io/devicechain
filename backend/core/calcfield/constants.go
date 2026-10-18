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
)

// Per-device constants (ADR-082, S6b): a number a DEVICE carries, not one of its readings — a
// calibration factor, a CT ratio, a tank's height — that a formula reads as `device.name`.
//
// A constant is declared by being referenced: the formula's own `device.k`, `device["k"]`,
// `has(device.k)` and `"k" in device` ARE the declaration, so there is no second list to drift
// from the formula. Every constant is a DOUBLE (a whole number is simply a double with no
// fraction; int(device.k) reads it as an int), so the checker types `device.k` without knowing
// which constants any device has. Which it has is a fact about one device, found at evaluation.
//
// Constants are INPUTS in every rule that matters: an unguarded constant that a device lacks
// skips the formula as missing_input BEFORE it runs, one that is not a finite number skips as
// invalid_input or non_finite, and `has(device.k) ? device.k : 1.0` is the author's explicit
// default. They are not state: the caller reads them from the device record it already holds
// and hands them over with the sample, so a value is frozen into the computed value at the
// moment it is computed and a later edit to the device never rewrites it.

// VarDevice is the map the per-device constants are reachable through.
const VarDevice = "device"

// MaxConstants bounds how many distinct constants one formula may read. A formula is small
// arithmetic over one sample; a dozen constants is a table lookup written as a formula, and
// each is one more value to supply and explain when a device lacks it.
const MaxConstants = 8

// MaxProfileConstants bounds how many distinct constants one profile's formulas read together.
// The resolver reads each of them out of a device's metadata for every event of a profile that
// has any, so the number is a per-event cost.
const MaxProfileConstants = 32

// CheckProfileConstants refuses a profile whose formulas read more than MaxProfileConstants
// distinct device constants between them.
func CheckProfileConstants(formulas []*Formula) error {
	seen := map[string]struct{}{}
	for _, f := range formulas {
		for _, k := range f.constants {
			seen[k] = struct{}{}
		}
	}
	if len(seen) > MaxProfileConstants {
		return fmt.Errorf("the profile's calculated metrics read %d distinct device constants together, above the maximum of %d",
			len(seen), MaxProfileConstants)
	}
	return nil
}

// MaxConstantKeyBytes bounds a constant's name. It is a device metadata key, the same kind of
// name a metric key is.
const MaxConstantKeyBytes = 64

// Constant is one device constant as the caller read it: a finite number, or the reason it is
// not usable. Build one with NewConstant, ConstantFromString or InvalidConstant, once per event;
// the value is boxed once there, so handing the same slice to every sample entry of the event
// copies and allocates nothing.
//
// A constant the device does not have is simply not in the slice: absent and unusable are
// different skip reasons, as for an input.
type Constant struct {
	key string
	val ref.Val // nil when bad is set
	bad SkipReason
}

// Key is the constant's name.
func (c Constant) Key() string { return c.key }

// NewConstant is the constant key with value v. A v that is NaN or ±Inf is kept as an unusable
// constant (a formula that reads it is skipped as non_finite), never as a number.
func NewConstant(key string, v float64) Constant {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return Constant{key: key, bad: SkipNonFinite}
	}
	return Constant{key: key, val: types.Double(v)}
}

// InvalidConstant is a constant the device has that is not a number — a string, a boolean, an
// object. A formula that reads it is skipped as invalid_input.
func InvalidConstant(key string) Constant {
	return Constant{key: key, bad: SkipInvalidInput}
}

// ConstantFromString is the constant key from its decimal text, parsed as a DOUBLE by the same
// rule an input uses. Text that is not a number is an invalid constant, and one that parses to
// NaN or ±Inf is a non-finite one: never a zero.
func ConstantFromString(key, raw string) Constant {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return InvalidConstant(key)
	}
	return NewConstant(key, f)
}

// Number is the constant's value, or false when it is not a usable number.
func (c Constant) Number() (float64, bool) {
	if c.val == nil {
		return 0, false
	}
	return float64(c.val.(types.Double)), true
}

// UseConstants gives the sample the constants of its device. The slice is held, not copied —
// the caller must not change it while the sample is in use — and cleared by Reset and Release.
func (s *Sample) UseConstants(cs []Constant) { s.consts = cs }

// constant finds a constant by key.
func (s *Sample) constant(key string) (Constant, bool) {
	for i := range s.consts {
		if s.consts[i].key == key {
			return s.consts[i], true
		}
	}
	return Constant{}, false
}

// constantReason is why constant key, which a formula reads, cannot be: SkipNone when it can.
func (s *Sample) constantReason(key string) SkipReason {
	c, ok := s.constant(key)
	switch {
	case !ok:
		return SkipMissingInput
	case c.bad != SkipNone:
		return c.bad
	}
	return SkipNone
}

// constantValue returns constant key's value for an evaluation that READS it or, when it is
// absent or unusable, the CEL error saying so.
func (s *Sample) constantValue(key string) ref.Val {
	if r := s.constantReason(key); r != SkipNone {
		return types.WrapErr(&inputError{key: VarDevice + "." + key, reason: r})
	}
	c, _ := s.constant(key)
	return c.val
}

// constMapType is the runtime type of `device`. Like sampleMapType it carries only the traits
// the allowlist lets an expression use: a constant-key read, a has() test and `"k" in device`.
var constMapType = types.NewObjectType("calcfield.device",
	traits.IndexerType, traits.FieldTesterType, traits.ContainerType)

// constMap is `device`: every constant of the Sample, by key. One pointer wide, so binding it
// in ResolveName boxes without allocating.
type constMap struct{ s *Sample }

func (m constMap) key(k ref.Val) (string, bool) {
	s, ok := k.(types.String)
	return string(s), ok
}

// Get implements traits.Indexer: a READ of a constant.
func (m constMap) Get(k ref.Val) ref.Val {
	key, ok := m.key(k)
	if !ok {
		return types.NewErr("no such key: %v", k)
	}
	return m.s.constantValue(key)
}

// IsSet implements traits.FieldTester for has(device.key): present means usable.
func (m constMap) IsSet(k ref.Val) ref.Val {
	key, ok := m.key(k)
	return types.Bool(ok && m.s.constantReason(key) == SkipNone)
}

// Contains implements traits.Container for `"key" in device`.
func (m constMap) Contains(k ref.Val) ref.Val { return m.IsSet(k) }

// ConvertToNative implements ref.Val; never converted.
func (m constMap) ConvertToNative(reflect.Type) (any, error) {
	return nil, fmt.Errorf("calcfield: the device constants cannot be converted")
}

// ConvertToType implements ref.Val.
func (m constMap) ConvertToType(t ref.Type) ref.Val {
	if t == types.TypeType {
		return constMapType
	}
	return types.NewErr("calcfield: the device constants cannot be converted to %v", t)
}

// Equal implements ref.Val; refused at compile, so never reached by a formula.
func (m constMap) Equal(ref.Val) ref.Val { return types.NoSuchOverloadErr() }

// Type implements ref.Val.
func (m constMap) Type() ref.Type { return constMapType }

// Value implements ref.Val.
func (m constMap) Value() any { return m.s }
