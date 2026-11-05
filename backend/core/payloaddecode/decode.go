// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/interpreter"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

// internalErrorMessage is the text of a recovered panic. It must never be seen: the fuzz and
// property tests fail on it.
const internalErrorMessage = "internal error evaluating the decoder"

// LoRa is the LoRaWAN context of an uplink.
type LoRa struct {
	FPort, FCnt int64
}

// Input is one uplink as the decoder sees it. A nil slice means the input is ABSENT (a
// decoder that declared it fails with missing_input); an empty non-nil slice is present.
type Input struct {
	// Frame is the raw frame, at most MaxFrameBytes.
	Frame []byte
	// JSON is the canonical JSON of a generic item, at most MaxJSONBytes and MaxJSONDepth deep.
	JSON []byte
	// Decoded is the network server's decoded object, under the same caps.
	Decoded []byte
	// LoRa is set on LoRaWAN envelopes; fPort and fCnt are -1 otherwise.
	LoRa *LoRa
	// Link is rssi / snr / gateways when the envelope had them.
	Link map[string]float64
}

// Reading is one decoded value as stored: the key and the value text.
type Reading struct {
	Key, Value string
}

// Skip is a map-output key that was not stored, with the reason: "undeclared", "collision" or
// "reserved".
type Skip struct {
	Key, Reason string
}

// Result is a successful decode.
type Result struct {
	// Section is the name of the section that matched.
	Section string
	// Ignored is a matched `ignore` section: the uplink is accepted and stores nothing.
	Ignored  bool
	Readings []Reading
	Skipped  []Skip
	// Cost is the actual cost spent, in the same units as Plan.EstimatedCost.
	Cost uint64
}

// state is the pooled per-decode activation.
type state struct {
	plan *Plan
	vals []any
	cost uint64
}

func (s *state) ResolveName(name string) (any, bool) {
	i, ok := s.plan.slots[name]
	if !ok {
		return nil, false
	}
	return s.vals[i], true
}

func (s *state) Parent() interpreter.Activation { return nil }

var (
	emptyDynMap    = types.DefaultTypeAdapter.NativeToValue(map[string]any{})
	emptyDoubleMap = types.DefaultTypeAdapter.NativeToValue(map[string]float64{})
)

// jsonDepth is the deepest nesting of {} and [] in b, ignoring brackets inside strings. It is
// a scan, not a parse, so a hostile document is refused before it is allocated.
func jsonDepth(b []byte) int {
	depth, deepest := 0, 0
	inStr, esc := false, false
	for _, c := range b {
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
			deepest = max(deepest, depth)
		case '}', ']':
			depth--
		}
	}
	return deepest
}

// CheckJSON applies the input caps to a JSON document that will be carried: size first, then
// nesting depth, both before any parse.
func CheckJSON(raw []byte) *DecodeError {
	if len(raw) > MaxJSONBytes {
		return &DecodeError{Reason: ReasonInputTooLarge, Offset: -1, Message: fmt.Sprintf("JSON of %d bytes is above the cap of %d", len(raw), MaxJSONBytes)}
	}
	if d := jsonDepth(raw); d > MaxJSONDepth {
		return &DecodeError{Reason: ReasonInputMalformed, Offset: -1, Message: fmt.Sprintf("JSON nests %d deep, above the cap of %d", d, MaxJSONDepth)}
	}
	return nil
}

// ParseCost is the cost, in the same units as an expression's, of checking and parsing n bytes
// of JSON: one unit a byte, so the largest legal input (MaxJSONBytes) costs 65,536. Measured
// on 64 KiB, the cheapest shape (a few long strings) parses in about 1 ms and the densest
// (nested empty arrays or objects, a long array of zeros, one-character strings) in 4-6 ms,
// which is 60-100 ns a unit: inside the band the natives measure in.
func ParseCost(n int) uint64 { return uint64(max(n, 0)) }

func parseObject(raw []byte, what string) (ref.Val, *DecodeError) {
	if de := CheckJSON(raw); de != nil {
		de.Path = what
		return nil, de
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, &DecodeError{Reason: ReasonInputMalformed, Path: what, Offset: -1, Message: "the value is not a JSON object"}
	}
	return types.DefaultTypeAdapter.NativeToValue(m), nil
}

// Decode runs the decoder over one uplink. It never panics and never returns a partial result:
// any failure is a *DecodeError and no readings. Input caps are applied before any evaluation.
func (p *Plan) Decode(in Input) (res *Result, derr *DecodeError) {
	defer func() {
		if r := recover(); r != nil {
			res, derr = nil, &DecodeError{Reason: ReasonEvalError, Offset: -1, Message: internalErrorMessage}
		}
	}()
	// Caps first, before any work.
	if len(in.Frame) > MaxFrameBytes {
		return nil, &DecodeError{Reason: ReasonInputTooLarge, Path: "frame", Offset: -1, Message: fmt.Sprintf("frame of %d bytes is above the cap of %d", len(in.Frame), MaxFrameBytes)}
	}
	if len(in.JSON) > MaxJSONBytes || len(in.Decoded) > MaxJSONBytes {
		return nil, &DecodeError{Reason: ReasonInputTooLarge, Offset: -1, Message: fmt.Sprintf("JSON is above the cap of %d bytes", MaxJSONBytes)}
	}
	var missing bool
	switch p.input {
	case InputFrame:
		missing = in.Frame == nil
	case InputJSON:
		missing = in.JSON == nil
	case InputDecoded:
		missing = in.Decoded == nil
	}
	if missing {
		return nil, &DecodeError{Reason: ReasonMissingInput, Offset: -1, Message: fmt.Sprintf("the decoder needs the %s input, which this uplink does not carry", p.input)}
	}

	s := p.pool.Get().(*state)
	s.plan, s.cost = p, 0
	defer func() {
		// Drop the uplink before pooling the state, so a pooled activation retains nothing.
		clear(s.vals)
		p.pool.Put(s)
	}()
	s.vals[slotFrame] = types.Bytes(in.Frame)
	fp, fc := int64(-1), int64(-1)
	if in.LoRa != nil {
		fp, fc = in.LoRa.FPort, in.LoRa.FCnt
	}
	s.vals[slotFPort], s.vals[slotFCnt] = types.Int(fp), types.Int(fc)
	s.vals[slotJSON], s.vals[slotDecoded] = emptyDynMap, emptyDynMap
	if p.usesJSON && in.JSON != nil {
		s.cost += ParseCost(len(in.JSON))
		v, de := parseObject(in.JSON, "json")
		if de != nil {
			return nil, de
		}
		s.vals[slotJSON] = v
	}
	if p.usesDecoded && in.Decoded != nil {
		s.cost += ParseCost(len(in.Decoded))
		v, de := parseObject(in.Decoded, "decoded")
		if de != nil {
			return nil, de
		}
		s.vals[slotDecoded] = v
	}
	if len(in.Link) == 0 {
		s.vals[slotLink] = emptyDoubleMap
	} else {
		s.vals[slotLink] = types.DefaultTypeAdapter.NativeToValue(in.Link)
	}

	for _, l := range p.lets {
		v, de := s.eval(l.expr)
		if de != nil {
			return nil, de
		}
		s.vals[l.slot] = v
	}

	for si := range p.sections {
		sec := &p.sections[si]
		if sec.when != nil {
			ok, de := s.evalBool(sec.when)
			if de != nil {
				return nil, de
			}
			if !ok {
				continue
			}
		}
		if sec.ignore {
			return &Result{Section: sec.name, Ignored: true, Cost: s.cost}, nil
		}
		return s.runSection(sec)
	}
	return nil, &DecodeError{Reason: ReasonNoSection, Offset: -1, Message: "no section matched this uplink", Cost: s.cost}
}

type reading struct {
	Reading
	row   *row
	fromM bool
	// val is a map entry's value, formatted only if the entry survives collision resolution:
	// a scalar wins its key, and the losing entry is skipped, never validated.
	val   ref.Val
	typ   calcfield.Type
	typed bool
}

func (s *state) runSection(sec *section) (*Result, *DecodeError) {
	p := s.plan
	out := make([]reading, 0, p.maxReadings)
	var skipped []Skip
	for ri := range sec.rows {
		r := &sec.rows[ri]
		if r.when != nil {
			ok, de := s.evalBool(r.when)
			if de != nil {
				return nil, de
			}
			if !ok {
				continue
			}
		}
		v, de := s.eval(r.value)
		if de != nil {
			return nil, de
		}
		if !r.isMap {
			txt, err := formatValue(r.key, v, r.typ, r.typed)
			if err != nil {
				return nil, s.fail(ReasonInvalidOutput, r.path+".expr", err.Error())
			}
			out = append(out, reading{Reading: Reading{r.key, txt}, row: r})
			continue
		}
		m, ok := v.(traits.Mapper)
		if !ok {
			return nil, s.fail(ReasonEvalError, r.value.path, "the map output did not produce a map")
		}
		// Size first: iterating copies every key, and a row that declared maxKeys 1 must not pay
		// for a map of thousands before being refused.
		if n, ok := m.Size().(types.Int); ok && int64(n) > int64(r.maxKeys) {
			return nil, s.fail(ReasonMapOverflow, r.value.path, fmt.Sprintf("the map has more than the declared maxKeys of %d", r.maxKeys))
		}
		keys := make([]string, 0, r.maxKeys)
		for it := m.Iterator(); it.HasNext() == types.True; {
			k, ok := it.Next().(types.String)
			if !ok {
				return nil, s.fail(ReasonEvalError, r.value.path, "a map output key is not a string")
			}
			if len(keys) == r.maxKeys {
				return nil, s.fail(ReasonMapOverflow, r.value.path, fmt.Sprintf("the map has more than the declared maxKeys of %d", r.maxKeys))
			}
			keys = append(keys, string(k))
		}
		s.cost += uint64(len(keys))
		// Sorted: a map has no order, and a stored value must not depend on one.
		slices.Sort(keys)
		for _, k := range keys {
			full := r.prefix + k
			if strings.HasPrefix(full, ReservedKeyPrefix) {
				skipped = append(skipped, Skip{full, "reserved"})
				continue
			}
			typ, typed := calcfield.Double, false
			if p.metrics != nil {
				t, declared := p.metrics.MetricType(full)
				if !declared {
					skipped = append(skipped, Skip{full, "undeclared"})
					continue
				}
				typ, typed = t, true
			}
			out = append(out, reading{Reading: Reading{Key: full}, row: r, fromM: true, val: m.Get(types.String(k)), typ: typ, typed: typed})
		}
	}

	// A scalar wins a key a map also yields; the map's entry is skipped and counted.
	scalar := map[string]struct{}{}
	for _, rd := range out {
		if !rd.fromM {
			scalar[rd.Key] = struct{}{}
		}
	}
	res := &Result{Section: sec.name, Skipped: skipped, Readings: make([]Reading, 0, len(out))}
	seen := make(map[string]struct{}, len(out))
	for _, rd := range out {
		if _, dup := seen[rd.Key]; dup {
			res.Skipped = append(res.Skipped, Skip{rd.Key, "collision"})
			continue
		}
		if _, sc := scalar[rd.Key]; sc && rd.fromM {
			res.Skipped = append(res.Skipped, Skip{rd.Key, "collision"})
			continue
		}
		seen[rd.Key] = struct{}{}
		if rd.fromM {
			txt, err := formatValue(rd.Key, rd.val, rd.typ, rd.typed)
			if err != nil {
				return nil, s.fail(ReasonInvalidOutput, rd.row.value.path, err.Error())
			}
			rd.Value = txt
		}
		if p.metrics != nil {
			if err := p.metrics.ValidateMeasurement(rd.Key, rd.Value); err != nil {
				path := rd.row.path + ".expr"
				if rd.fromM {
					path = rd.row.path + ".map"
				}
				return nil, s.fail(ReasonInvalidOutput, path, fmt.Sprintf("%s: %v", rd.Key, err))
			}
		}
		res.Readings = append(res.Readings, rd.Reading)
	}
	res.Cost = s.cost
	if len(res.Readings) == 0 {
		return nil, s.fail(ReasonNoOutputs, sec.path, "the section produced no readings")
	}
	return res, nil
}

func (s *state) fail(r Reason, path, msg string) *DecodeError {
	return &DecodeError{Reason: r, Path: path, Message: msg, Offset: -1, Cost: s.cost}
}

// eval runs one expression and accumulates its actual cost.
func (s *state) eval(e *expr) (ref.Val, *DecodeError) {
	v, det, err := e.prog.Eval(s)
	if det != nil {
		if c := det.ActualCost(); c != nil {
			s.cost += *c
		}
	}
	if err != nil {
		return nil, s.classify(e, err)
	}
	return v, nil
}

func (s *state) evalBool(e *expr) (bool, *DecodeError) {
	v, de := s.eval(e)
	if de != nil {
		return false, de
	}
	b, ok := v.(types.Bool)
	if !ok {
		return false, s.fail(ReasonEvalError, e.path, "the condition did not produce a bool")
	}
	return bool(b), nil
}

// isCostLimit reports cel-go's own typed cancellation for the runtime cost limit, not a
// message that merely contains its words (a JSON key can be named anything).
func isCostLimit(err error) bool {
	var ec interpreter.EvalCancelledError
	return errors.As(err, &ec) && ec.Cause == interpreter.CostLimitExceeded
}

// truncateUTF8 cuts s to at most n bytes on a rune boundary, and makes sure what is left is
// valid UTF-8: a record with a split rune is refused by the database that stores it.
func truncateUTF8(s string, n int) string {
	if len(s) > n {
		s = s[:n]
		for len(s) > 0 && !utf8.ValidString(s[max(0, len(s)-utf8.UTFMax):]) {
			s = s[:len(s)-1]
		}
	}
	return strings.ToValidUTF8(s, "�")
}

// classify maps an evaluation error to a DecodeError. Its text is built from the expression
// path, the function name and the offset only.
func (s *state) classify(e *expr, err error) *DecodeError {
	de := &DecodeError{Reason: ReasonEvalError, Path: e.path, Offset: -1, Cost: s.cost}
	var ne *nativeError
	switch {
	case isCostLimit(err):
		de.Reason = ReasonCostExceeded
		de.Message = fmt.Sprintf("the expression spent more than its estimated cost of %d", e.cost)
	case errors.As(err, &ne):
		de.Message, de.Offset = ne.Error(), ne.Offset
	case errors.Is(err, errNonFinite):
		de.Message = "a double operation produced a value that is not finite"
	case strings.HasPrefix(err.Error(), "internal error"):
		// cel-go recovered a panic inside a native or a binding. It is a bug here, never an
		// author's mistake, and its text (a Go runtime message) is not for a tenant.
		de.Message = internalErrorMessage
	default:
		de.Message = truncateUTF8(err.Error(), 200)
	}
	return de
}

// formatValue renders one output as the target metric's type: an INT metric takes an integral
// value within int64 and is rendered with FormatInt; a DOUBLE metric uses FormatDouble; a
// BOOLEAN metric takes a bool or 0/1. With no declared type the value renders as its own kind.
// It does not apply the metric's min/max, storable magnitude or enum rules; that is the
// validation hook.
func formatValue(key string, v ref.Val, typ calcfield.Type, typed bool) (string, error) {
	var (
		isInt, isBool bool
		n             int64
		f             float64
	)
	switch x := v.(type) {
	case types.Int:
		isInt, n, f = true, int64(x), float64(x)
	case types.Double:
		f = float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", fmt.Errorf("%s: the value is not finite", key)
		}
	case types.Bool:
		isBool = true
		if x {
			n, f = 1, 1
		}
		isInt = true
	default:
		return "", fmt.Errorf("%s: the value is a %s, not a number or a bool", key, v.Type().TypeName())
	}
	if !typed {
		typ = calcfield.Double
		if isInt {
			return calcfield.FormatInt(n), nil
		}
	}
	switch typ {
	case calcfield.Integer:
		if isInt {
			return calcfield.FormatInt(n), nil
		}
		if f != math.Trunc(f) || f < -(1<<63) || f >= 1<<63 {
			return "", fmt.Errorf("%s: %s is not an integer within int64, which the INT metric needs", key, calcfield.FormatDouble(f))
		}
		return calcfield.FormatInt(int64(f)), nil
	case calcfield.Boolean:
		if isBool || (isInt && (n == 0 || n == 1)) || (!isInt && (f == 0 || f == 1)) {
			if f != 0 {
				return "1", nil
			}
			return "0", nil
		}
		return "", fmt.Errorf("%s: %s is not 0, 1 or a bool, which the BOOLEAN metric needs", key, calcfield.FormatDouble(f))
	case calcfield.Double:
		return calcfield.FormatDouble(f), nil
	}
	return "", fmt.Errorf("%s: the metric has an unknown type %s", key, typ)
}
