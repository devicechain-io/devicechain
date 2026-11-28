// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/ledger"
)

// SpecSchema is the version of the spec document this package parses. It is stored beside every
// frozen spec; a later shape is a new schema, never a reinterpretation of this one.
const SpecSchema = 1

// Platform limits. They are constants of the platform; a tier may later lower them, never raise
// them (ADR-086 §11).
const (
	// MaxIntentsPerTenant bounds the intents a tenant holds that are not RETIRED.
	MaxIntentsPerTenant = 50
	// MaxDevicesPerIntent bounds ceiling.maxDevices, and so the members one intent may target.
	MaxDevicesPerIntent = 100_000
	// MinIntervalSeconds is the supervisory floor: an intent is never reconciled faster than this.
	MinIntervalSeconds = 60
	// MaxIntervalSeconds bounds reconcile.minIntervalSeconds from above (one day).
	MaxIntervalSeconds = 86_400
	// MaxLinesPerReconcile bounds reconcile.maxLinesPerReconcile; it is the bulk-change line cap.
	MaxLinesPerReconcile = 10_000
	// MaxActuationsPerDevicePerDay bounds reconcile.maxActuationsPerDevicePerDay: at the
	// MinIntervalSeconds floor a device cannot be reconciled more often than this in a day.
	MaxActuationsPerDevicePerDay = 86_400 / MinIntervalSeconds
	// DefaultManualDwellSeconds is reconcile.manualDwellSeconds when the document omits it.
	DefaultManualDwellSeconds = 3_600
	// MaxManualDwellSeconds bounds reconcile.manualDwellSeconds.
	MaxManualDwellSeconds = 86_400
	// MaxSpecBytes bounds a spec document as submitted.
	MaxSpecBytes = 16 << 10
	// MaxTokenLen bounds a group token, a configuration key and a device token. It equals
	// core.MaxTokenLen (TestTokenGrammarAgreesWithCore holds them together).
	MaxTokenLen = 128
)

// Class is an intent's class. Only supervisory classes exist (ADR-086): an intent never
// carries a protection or safety function.
type Class string

const (
	ClassSchedule     Class = "SCHEDULE"
	ClassOptimization Class = "OPTIMIZATION"
)

// Valid reports whether c is a class this build accepts.
func (c Class) Valid() bool { return c == ClassSchedule || c == ClassOptimization }

// Attribute value types, the vocabulary of device-management's attribute and configuration
// declarations. A declared configuration key carries exactly one of them.
const (
	ValueTypeString  = "STRING"
	ValueTypeLong    = "LONG"
	ValueTypeDouble  = "DOUBLE"
	ValueTypeBoolean = "BOOLEAN"
	ValueTypeJSON    = "JSON"
)

// ValidValueType reports whether t names an attribute value type.
func ValidValueType(t string) bool {
	switch t {
	case ValueTypeString, ValueTypeLong, ValueTypeDouble, ValueTypeBoolean, ValueTypeJSON:
		return true
	}
	return false
}

// Spec is the v1 intent spec, normalized: defaults are applied and every field is explicit. Its
// json tags are the canonical form's field names and are durable.
type Spec struct {
	Target    Target    `json:"target"`
	Point     Point     `json:"point"`
	Value     Value     `json:"value"`
	Class     Class     `json:"class"`
	Ceiling   Ceiling   `json:"ceiling"`
	Reconcile Reconcile `json:"reconcile"`
}

// Target is a frozen version of a published dynamic device group.
type Target struct {
	GroupToken   string `json:"groupToken"`
	GroupVersion int64  `json:"groupVersion"`
}

// Point is what the intent holds: one SHARED configuration key.
type Point struct {
	ConfigKey string `json:"configKey"`
}

// Value is the intended value: a typed value text, or Unset (the key must be absent). The value
// text is exact: it is compared, digested and posted as written, with no normalization. So it
// must also be the spelling device-management stores for that value (see checkValueText):
// a spelling device-management would rewrite on the way in is refused, because its digest could
// never match a device's stored row, and two spellings of one value would compete as two values.
type Value struct {
	ValueType string  `json:"valueType,omitempty"`
	Value     *string `json:"value,omitempty"`
	Unset     bool    `json:"unset,omitempty"`
}

// Ceiling is the bound an approval will name: the most members the target may have.
type Ceiling struct {
	MaxDevices int64 `json:"maxDevices"`
}

// Reconcile is the pacing the intent asks for.
type Reconcile struct {
	MinIntervalSeconds           int64 `json:"minIntervalSeconds"`
	MaxLinesPerReconcile         int64 `json:"maxLinesPerReconcile"`
	MaxActuationsPerDevicePerDay int64 `json:"maxActuationsPerDevicePerDay"`
	ManualDwellSeconds           int64 `json:"manualDwellSeconds"`
}

// ErrInvalidSpec wraps every refusal of a spec, so a caller can map it to INVALID_SPEC.
var ErrInvalidSpec = errors.New("invalid intent spec")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, fmt.Sprintf(format, args...))
}

// Validate is the fail-closed check of a normalized spec. Parse calls it; so do Canonical,
// SpecDigest and Diff, so an unvalidated spec cannot reach a digest.
func (s Spec) Validate() error {
	if err := validateToken("target.groupToken", s.Target.GroupToken); err != nil {
		return err
	}
	if s.Target.GroupVersion < 1 || s.Target.GroupVersion > math.MaxInt32 {
		return invalid("target.groupVersion must be between 1 and %d, got %d", math.MaxInt32, s.Target.GroupVersion)
	}
	if err := validateToken("point.configKey", s.Point.ConfigKey); err != nil {
		return err
	}
	if err := s.Value.validate(); err != nil {
		return err
	}
	if !s.Class.Valid() {
		return invalid("class must be %s or %s, got %q", ClassSchedule, ClassOptimization, s.Class)
	}
	if err := inRange("ceiling.maxDevices", s.Ceiling.MaxDevices, 1, MaxDevicesPerIntent); err != nil {
		return err
	}
	r := s.Reconcile
	if err := inRange("reconcile.minIntervalSeconds", r.MinIntervalSeconds, MinIntervalSeconds, MaxIntervalSeconds); err != nil {
		return err
	}
	if err := inRange("reconcile.maxLinesPerReconcile", r.MaxLinesPerReconcile, 1, MaxLinesPerReconcile); err != nil {
		return err
	}
	if err := inRange("reconcile.maxActuationsPerDevicePerDay", r.MaxActuationsPerDevicePerDay, 1, MaxActuationsPerDevicePerDay); err != nil {
		return err
	}
	if err := inRange("reconcile.manualDwellSeconds", r.ManualDwellSeconds, 0, MaxManualDwellSeconds); err != nil {
		return err
	}
	// The canonical form escapes every control character as six bytes, so a document under the
	// limit can canonicalize over it. Bounding the canonical form as well keeps every valid spec's
	// canonical bytes parseable.
	canonical, err := ledger.Canonical(s)
	if err != nil {
		return invalid("the spec has no canonical form: %v", err)
	}
	if len(canonical) > MaxSpecBytes {
		return invalid("the canonical spec is %d bytes; at most %d are accepted", len(canonical), MaxSpecBytes)
	}
	return nil
}

func inRange(field string, v, lo, hi int64) error {
	if v < lo || v > hi {
		return invalid("%s must be between %d and %d, got %d", field, lo, hi, v)
	}
	return nil
}

func (v Value) validate() error {
	if v.Unset {
		if v.ValueType != "" || v.Value != nil {
			return invalid("value: unset carries neither valueType nor value")
		}
		return nil
	}
	if v.Value == nil {
		return invalid("value: value is required unless unset is true")
	}
	if !ValidValueType(v.ValueType) {
		return invalid("value.valueType must be one of STRING, LONG, DOUBLE, BOOLEAN, JSON, got %q", v.ValueType)
	}
	return checkValueText(v.ValueType, *v.Value)
}

// checkValueText refuses a value text the declared type cannot carry, and a text device-management
// would store in another spelling. Device-management normalizes every configuration write
// (normalizeAttributeValue in model/api_attributes.go): LONG to strconv.FormatInt base 10, DOUBLE
// to strconv.FormatFloat 'f' -1, BOOLEAN to strconv.FormatBool; STRING and JSON are stored as
// written. An intent's value is digested as written and compared with the digest of the stored
// row, so "TRUE", "040", "+40", "1.0", "1e3" or "0x1p2" would read as DRIFTED forever. Each is
// refused here, at authoring, with the spelling to use instead: LONG is exactly FormatInt of a
// base-10 int64, DOUBLE exactly FormatFloat of a finite float64, BOOLEAN "true" or "false", JSON
// exactly one JSON value.
func checkValueText(valueType, text string) error {
	if !utf8.ValidString(text) {
		return invalid("value.value is not valid UTF-8")
	}
	if strings.IndexByte(text, 0) >= 0 {
		return invalid("value.value holds a NUL character, which no stored value can")
	}
	switch valueType {
	case ValueTypeString:
		return nil
	case ValueTypeLong:
		i, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return invalid("value.value %q is not a LONG", text)
		}
		return requireStored(valueType, text, strconv.FormatInt(i, 10))
	case ValueTypeDouble:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return invalid("value.value %q is not a finite DOUBLE", text)
		}
		return requireStored(valueType, text, strconv.FormatFloat(f, 'f', -1, 64))
	case ValueTypeBoolean:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return invalid("value.value %q is not a BOOLEAN", text)
		}
		return requireStored(valueType, text, strconv.FormatBool(b))
	case ValueTypeJSON:
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return invalid("value.value is not a JSON value")
		}
		if _, err := dec.Token(); err != io.EOF {
			return invalid("value.value holds more than one JSON value")
		}
	default:
		return invalid("value.valueType %q is unknown", valueType)
	}
	return nil
}

// requireStored refuses text unless it is stored, the spelling device-management keeps for the
// same value.
func requireStored(valueType, text, stored string) error {
	if text != stored {
		return invalid("value.value %q is not how a %s is stored; write %q", text, valueType, stored)
	}
	return nil
}

// validateToken applies the platform's token grammar (core.ValidateToken's): letters, digits,
// hyphen and underscore, starting with a letter or digit, at most MaxTokenLen bytes. It is
// repeated here, rather than imported, because package core is not pure;
// TestTokenGrammarAgreesWithCore holds the two to the same answer.
func validateToken(field, token string) error {
	if !validTokenGrammar(token) {
		return invalid("%s %q must be 1 to %d letters, digits, hyphens and underscores, starting with a letter or digit",
			field, token, MaxTokenLen)
	}
	return nil
}

func validTokenGrammar(token string) bool {
	if token == "" || len(token) > MaxTokenLen {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// Canonical returns the canonical bytes of a valid spec: ledger.Canonical over the normalized
// spec (keys sorted by UTF-8 bytes, no whitespace, every field explicit except an absent value
// half). A spec that does not validate has no canonical form.
func Canonical(s Spec) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return ledger.Canonical(s)
}

// SpecDigest is "sha256:" + hex SHA-256 of Canonical(s): the identity an intent version, and an
// approval of it, binds.
func SpecDigest(s Spec) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return ledger.Digest(s)
}
