// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

const policyDomain = "devicechain.admission.policy.v1\n"

// ErrInvalidPolicy is wrapped by every error that says a policy cannot be used. A policy that
// does not validate admits nothing.
var ErrInvalidPolicy = errors.New("invalid policy")

// Bound limits one numeric top-level parameter. A nil side is unbounded.
type Bound struct {
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

// Rule is what a policy allows for one action kind.
type Rule struct {
	// MaxTargets is the largest target set one proposal may name. At least 1.
	MaxTargets int `json:"maxTargets"`
	// MaxWindow is the longest window one proposal may ask for. Greater than zero.
	MaxWindow time.Duration `json:"maxWindow"`
	// MinLead is how long before NotBefore the proposal must arrive. Zero admits a window
	// that is already open (for its remainder).
	MinLead time.Duration `json:"minLead"`
	// Bounds names top-level parameters that must be present, numeric and inside the bound.
	// When any is set the parameters must be a JSON object.
	Bounds map[string]Bound `json:"bounds,omitempty"`
}

// Policy is a declarative admission policy. Version is the operator's label; the receipt also
// carries the policy's digest, so two policies sharing a label can still be told apart.
type Policy struct {
	Version string          `json:"version"`
	Rules   map[string]Rule `json:"rules"`
}

func invalidPolicy(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPolicy, fmt.Sprintf(format, args...))
}

func finite(f *float64) bool { return f == nil || (!math.IsNaN(*f) && !math.IsInf(*f, 0)) }

// Validate refuses a policy that could not be applied as written.
func (p Policy) Validate() error {
	if p.Version == "" {
		return invalidPolicy("version is empty")
	}
	if len(p.Rules) == 0 {
		return invalidPolicy("has no rules")
	}
	for action, r := range p.Rules {
		if err := checkName("action", action); err != nil {
			return invalidPolicy("rule key: %v", err)
		}
		if r.MaxTargets < 1 || r.MaxTargets > MaxTargets {
			return invalidPolicy("%s: maxTargets %d is outside 1..%d", action, r.MaxTargets, MaxTargets)
		}
		if r.MaxWindow <= 0 {
			return invalidPolicy("%s: maxWindow must be greater than zero", action)
		}
		if r.MinLead < 0 {
			return invalidPolicy("%s: minLead is negative", action)
		}
		for name, b := range r.Bounds {
			if name == "" {
				return invalidPolicy("%s: a bound has an empty parameter name", action)
			}
			if b.Min == nil && b.Max == nil {
				return invalidPolicy("%s: bound %q sets neither min nor max", action, name)
			}
			if !finite(b.Min) || !finite(b.Max) {
				return invalidPolicy("%s: bound %q is not finite", action, name)
			}
			if b.Min != nil && b.Max != nil && *b.Min > *b.Max {
				return invalidPolicy("%s: bound %q has min above max", action, name)
			}
		}
	}
	return nil
}

// Digest identifies the policy's content. encoding/json writes map keys sorted, so the same
// rules always give the same bytes.
func (p Policy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	return digestOf(policyDomain, p)
}

// Verdict is the answer a receipt carries.
type Verdict string

const (
	Admitted Verdict = "ADMITTED"
	Refused  Verdict = "REFUSED"
)

// ReasonCode classifies one refusal reason for a machine.
type ReasonCode string

const (
	ReasonUnknownAction        ReasonCode = "UNKNOWN_ACTION"
	ReasonTooManyTargets       ReasonCode = "TOO_MANY_TARGETS"
	ReasonWindowTooLong        ReasonCode = "WINDOW_TOO_LONG"
	ReasonWindowElapsed        ReasonCode = "WINDOW_ELAPSED"
	ReasonInsufficientLead     ReasonCode = "INSUFFICIENT_LEAD"
	ReasonParametersNotObject  ReasonCode = "PARAMETERS_NOT_OBJECT"
	ReasonParameterMissing     ReasonCode = "PARAMETER_MISSING"
	ReasonParameterNotNumeric  ReasonCode = "PARAMETER_NOT_NUMERIC"
	ReasonParameterOutOfBounds ReasonCode = "PARAMETER_OUT_OF_BOUNDS"
)

// Reason is one refusal reason.
type Reason struct {
	Code   ReasonCode `json:"code"`
	Detail string     `json:"detail"`
}

// Decision is a policy's answer to one proposal.
type Decision struct {
	Verdict Verdict
	Reasons []Reason
}

// Admit evaluates a proposal against a policy at now. It returns an error only when the
// proposal is malformed or the policy invalid; every other "no" is a Refused decision whose
// reasons are listed in a fixed order, all of them rather than the first.
func Admit(now time.Time, p Proposal, pol Policy) (Decision, error) {
	if _, err := p.canonical(); err != nil {
		return Decision{}, err
	}
	if err := pol.Validate(); err != nil {
		return Decision{}, err
	}
	rule, ok := pol.Rules[p.Action]
	if !ok {
		// Fail closed: an action the policy does not name is refused, never assumed allowed.
		return refuse([]Reason{{ReasonUnknownAction, fmt.Sprintf("the policy has no rule for %q", p.Action)}}), nil
	}
	var reasons []Reason
	add := func(code ReasonCode, format string, args ...any) {
		reasons = append(reasons, Reason{code, fmt.Sprintf(format, args...)})
	}
	if n := len(p.Targets); n > rule.MaxTargets {
		add(ReasonTooManyTargets, "%d targets, at most %d allowed", n, rule.MaxTargets)
	}
	if w := p.NotAfter.Sub(p.NotBefore); w > rule.MaxWindow {
		add(ReasonWindowTooLong, "window of %s, at most %s allowed", w, rule.MaxWindow)
	}
	if !p.NotAfter.After(now) {
		add(ReasonWindowElapsed, "the window ended at %s", utcText(p.NotAfter))
	}
	if rule.MinLead > 0 && p.NotBefore.Sub(now) < rule.MinLead {
		add(ReasonInsufficientLead, "the window opens %s after submission, at least %s required",
			p.NotBefore.Sub(now), rule.MinLead)
	}
	reasons = append(reasons, checkBounds(p.Parameters, rule.Bounds)...)
	if len(reasons) > 0 {
		return refuse(reasons), nil
	}
	return Decision{Verdict: Admitted}, nil
}

func refuse(reasons []Reason) Decision { return Decision{Verdict: Refused, Reasons: reasons} }

// checkBounds applies the numeric bounds. The parameters have already been canonicalised once
// (so they are valid, duplicate-free JSON); here they are decoded with UseNumber so a number's
// literal is parsed exactly once, by strconv.
func checkBounds(raw json.RawMessage, bounds map[string]Bound) []Reason {
	if len(bounds) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(canonicalOrNull(raw)))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return []Reason{{ReasonParametersNotObject, "the policy bounds parameters, so they must be a JSON object"}}
	}
	names := make([]string, 0, len(bounds))
	for n := range bounds {
		names = append(names, n)
	}
	sort.Strings(names)
	var reasons []Reason
	for _, name := range names {
		b := bounds[name]
		v, ok := obj[name]
		if !ok {
			reasons = append(reasons, Reason{ReasonParameterMissing, fmt.Sprintf("%q is required", name)})
			continue
		}
		// Decoded into `any`, not into json.Number: encoding/json fills a json.Number from a
		// QUOTED string too, which would let "40" pass as the number 40 while the head-end
		// receives a string.
		var val any
		vd := json.NewDecoder(bytes.NewReader(v))
		vd.UseNumber()
		num, isNum := json.Number(""), false
		if err := vd.Decode(&val); err == nil {
			num, isNum = val.(json.Number)
		}
		if !isNum {
			reasons = append(reasons, Reason{ReasonParameterNotNumeric, fmt.Sprintf("%q is not a number", name)})
			continue
		}
		dec, ok := exactDecimal(num.String())
		if !ok {
			// An exponent too large to compare cheaply: refused rather than rounded.
			reasons = append(reasons, Reason{ReasonParameterOutOfBounds, fmt.Sprintf("%q is out of range", name)})
			continue
		}
		if (b.Min != nil && dec.Cmp(exactFloat(*b.Min)) < 0) || (b.Max != nil && dec.Cmp(exactFloat(*b.Max)) > 0) {
			reasons = append(reasons, Reason{ReasonParameterOutOfBounds,
				fmt.Sprintf("%q is %s, outside %s", name, num.String(), boundText(b))})
		}
	}
	return reasons
}

// maxExponent bounds the decimal exponent a bounded parameter may carry. Exact comparison
// builds the number as a rational, and 1e100000000 would build a hundred-million-digit one.
const maxExponent = 400

// exactDecimal parses a JSON number literal exactly. Comparing through float64 would round
// first: 29.999999999999999999 becomes 30 and passes a floor of 30 it is below.
func exactDecimal(lit string) (*big.Rat, bool) {
	if i := strings.IndexAny(lit, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimPrefix(lit[i+1:], "+"))
		if err != nil || exp > maxExponent || exp < -maxExponent {
			return nil, false
		}
	}
	r, ok := new(big.Rat).SetString(lit)
	return r, ok
}

// exactFloat is a bound's exact value. Validate has already refused NaN and infinities.
func exactFloat(f float64) *big.Rat { return new(big.Rat).SetFloat64(f) }

func canonicalOrNull(raw json.RawMessage) []byte {
	c, err := canonicalJSON(raw)
	if err != nil {
		return []byte("null")
	}
	return c
}

func boundText(b Bound) string {
	lo, hi := "-inf", "+inf"
	if b.Min != nil {
		lo = strconv.FormatFloat(*b.Min, 'g', -1, 64)
	}
	if b.Max != nil {
		hi = strconv.FormatFloat(*b.Max, 'g', -1, 64)
	}
	return "[" + lo + ", " + hi + "]"
}
