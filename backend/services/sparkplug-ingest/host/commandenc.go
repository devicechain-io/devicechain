// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// The strict command encoder (G19 S2).
//
// A requested value is encoded EXACTLY into the datatype the BIRTH declared for the metric, or
// refused before anything reaches the wire. "Exactly" means the value's class must match the
// datatype (a number for a number, true/false for a Boolean, a string for a string), an integer
// must be integral and inside the type's range, and nothing is silently coerced: 300 into Int8,
// 1.5 into an integer and -1 into an unsigned type are all refusals, not wraps or truncations.
//
// The wire facts below were read from the Sparkplug 3.0.0 text (eclipse-sparkplug/sparkplug, tag
// v3.0.0, specification/src/main/asciidoc/chapters), and are cited by their TCK ids where the
// spec gives one. Where the spec is silent, the source is named.

// Command wire constants. Each is asserted by name in the tests.
const (
	// commandQoS: [tck-id-topics-ncmd-mqtt] NCMD (and [tck-id-topics-dcmd-mqtt] DCMD) MUST be
	// published with MQTT QoS equal to 0; also [tck-id-payloads-ncmd-qos] and
	// [tck-id-payloads-dcmd-qos].
	commandQoS byte = 0
	// commandRetain: [tck-id-topics-ncmd-mqtt] / [tck-id-topics-dcmd-mqtt] retain equal to false;
	// also [tck-id-payloads-ncmd-retain] and [tck-id-payloads-dcmd-retain].
	commandRetain = false
)

// maxCommandString bounds a String/Text/UUID command value. A write to an output is not a bulk
// transfer, and an unbounded string is an unbounded publish.
const maxCommandString = 8 * 1024

// maxExactFloatInt is 2^53: below it every integer is exactly a float64, and AT it the float64 is
// ambiguous (2^53 and 2^53+1 decode to the same value), so it is refused too.
// A JSON number above it has already lost precision by the time it is a float64, so a 64-bit
// value that large must arrive as a string of digits.
const maxExactFloatInt = 1 << 53

// ErrUnsupportedDatatype is the refusal for a datatype commands do not carry in this release
// (Unknown, DataSet, Bytes, File, Template, PropertySet*, the arrays). It is a recognized
// vocabulary member that is not executable, never a silent drop.
var ErrUnsupportedDatatype = errors.New("datatype not supported for commands in this release")

// ErrValueRefused is wrapped by every refusal of a value for a datatype: wrong class, out of
// range, lossy, malformed.
var ErrValueRefused = errors.New("value refused for the metric's declared datatype")

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrValueRefused, fmt.Sprintf(format, a...))
}

type intRange struct{ min, max *big.Int }

func rng(min, max string) intRange {
	lo, _ := new(big.Int).SetString(min, 10)
	hi, _ := new(big.Int).SetString(max, 10)
	return intRange{lo, hi}
}

var intRanges = map[uint32]intRange{
	datatypeInt8:     rng("-128", "127"),
	datatypeInt16:    rng("-32768", "32767"),
	datatypeInt32:    rng("-2147483648", "2147483647"),
	datatypeInt64:    rng("-9223372036854775808", "9223372036854775807"),
	datatypeUInt8:    rng("0", "255"),
	datatypeUInt16:   rng("0", "65535"),
	datatypeUInt32:   rng("0", "4294967295"),
	datatypeUInt64:   rng("0", "18446744073709551615"),
	datatypeDateTime: rng("0", "18446744073709551615"),
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var digitsRE = regexp.MustCompile(`^-?[0-9]+$`)

// EncodeMetricValue returns the function that sets a metric's value arm to value, encoded for the
// BIRTH-declared datatype, or refuses. value is what a JSON decode (or a typed caller) produces:
// bool, string, json.Number, float32/64, or any Go integer.
//
// Integer carriers. The spec fixes the protobuf type of each datatype (Int8/16/32 and
// UInt8/16/32 ride int_value, a uint32; Int64/UInt64 ride long_value, a uint64) but never says
// how a NEGATIVE signed value is laid into the unsigned field. The Eclipse Tahu reference
// implementation does, and the two codecs that sit under almost every deployed node agree:
// Tahu Java (SparkplugBPayloadEncoder: setIntValue((Byte) v), a sign-extending widening to a
// 32-bit int) and Tahu C (tahu.c: int_value = *(int8_t *)value, a sign-extending conversion to
// uint32). So a signed value is its two's complement SIGN-EXTENDED to the carrier's full width:
// Int8 -1 is int_value 0xFFFFFFFF, not 0xFF. (Tahu Python alone truncates to the type's width.)
// Decoders differ in what they do with the carrier: Tahu Java narrows by the declared width, Tahu
// JS reinterprets it as int32, and Tahu C and Python leave it to the application. A sign-extended
// value reads back correctly in every one that decodes it; a width-truncated one would not (JS
// would read 255 for Int8 -1).
func EncodeMetricValue(datatype uint32, value any) (func(*sppb.Payload_Metric), error) {
	switch datatype {
	case datatypeInt8, datatypeInt16, datatypeInt32, datatypeUInt8, datatypeUInt16, datatypeUInt32:
		n, err := exactInteger(datatype, value, false)
		if err != nil {
			return nil, err
		}
		var wire uint32
		if n.Sign() < 0 {
			// Two's complement sign-extended to 32 bits: int32 holds every signed 8/16/32 value.
			wire = uint32(int32(n.Int64()))
		} else {
			wire = uint32(n.Uint64())
		}
		return func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_IntValue{IntValue: wire} }, nil

	case datatypeInt64, datatypeUInt64, datatypeDateTime:
		// DateTime is epoch milliseconds, a uint64 on the wire.
		n, err := exactInteger(datatype, value, datatype != datatypeDateTime)
		if err != nil {
			return nil, err
		}
		var wire uint64
		if n.Sign() < 0 {
			wire = uint64(n.Int64())
		} else {
			wire = n.Uint64()
		}
		return func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_LongValue{LongValue: wire} }, nil

	case datatypeFloat, datatypeDouble:
		f, err := exactFloat(value)
		if err != nil {
			return nil, err
		}
		if datatype == datatypeFloat {
			if math.Abs(f) > math.MaxFloat32 {
				return nil, refuse("%v overflows Float", f)
			}
			f32 := float32(f)
			if f != 0 && f32 == 0 {
				return nil, refuse("%v underflows Float to zero", f)
			}
			return func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_FloatValue{FloatValue: f32} }, nil
		}
		return func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_DoubleValue{DoubleValue: f} }, nil

	case datatypeBoolean:
		b, ok := value.(bool)
		if !ok {
			return nil, refuse("a Boolean takes true or false, got %s", className(value))
		}
		return func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_BooleanValue{BooleanValue: b} }, nil

	case datatypeString, datatypeText, datatypeUUID:
		s, ok := value.(string)
		if !ok {
			return nil, refuse("a string type takes a string, got %s", className(value))
		}
		if !utf8.ValidString(s) {
			return nil, refuse("not valid UTF-8")
		}
		if len(s) > maxCommandString {
			return nil, refuse("%d bytes exceeds the %d byte limit", len(s), maxCommandString)
		}
		if datatype == datatypeUUID && !uuidRE.MatchString(s) {
			return nil, refuse("not a UUID (8-4-4-4-12 hex)")
		}
		return func(m *sppb.Payload_Metric) { m.Value = &sppb.Payload_Metric_StringValue{StringValue: s} }, nil
	}
	return nil, fmt.Errorf("%w: datatype %d", ErrUnsupportedDatatype, datatype)
}

func className(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case json.Number, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "a number"
	}
	return fmt.Sprintf("%T", v)
}

// exactInteger reads value as an integer of datatype's range. A number must be integral; a string
// of digits is accepted only when allowString (the 64-bit types, where a JSON number cannot carry
// the whole range).
func exactInteger(datatype uint32, value any, allowString bool) (*big.Int, error) {
	var n *big.Int
	var err error
	switch v := value.(type) {
	case int:
		n = big.NewInt(int64(v))
	case int8:
		n = big.NewInt(int64(v))
	case int16:
		n = big.NewInt(int64(v))
	case int32:
		n = big.NewInt(int64(v))
	case int64:
		n = big.NewInt(v)
	case uint:
		n = new(big.Int).SetUint64(uint64(v))
	case uint8:
		n = new(big.Int).SetUint64(uint64(v))
	case uint16:
		n = new(big.Int).SetUint64(uint64(v))
	case uint32:
		n = new(big.Int).SetUint64(uint64(v))
	case uint64:
		n = new(big.Int).SetUint64(v)
	case float32:
		if n, err = integralFloat(datatype, float64(v)); err != nil {
			return nil, err
		}
	case float64:
		if n, err = integralFloat(datatype, v); err != nil {
			return nil, err
		}
	case json.Number:
		s := v.String()
		if digitsRE.MatchString(s) {
			n, _ = new(big.Int).SetString(s, 10)
			break
		}
		// A decimal or exponent form is judged EXACTLY, never through a float64: 1.0000000000000001
		// rounds to 1 and 1e-400 underflows to 0, and either would be sent as an integer value the
		// operator did not ask for.
		if n, err = integralDecimal(s); err != nil {
			return nil, err
		}
	case string:
		if !allowString {
			return nil, refuse("this type takes a number, got a string")
		}
		if !digitsRE.MatchString(v) {
			return nil, refuse("a string for a 64-bit integer must be digits only")
		}
		n, _ = new(big.Int).SetString(v, 10)
	default:
		return nil, refuse("an integer type takes a number, got %s", className(value))
	}
	r := intRanges[datatype]
	if n.Cmp(r.min) < 0 || n.Cmp(r.max) > 0 {
		return nil, refuse("%s is outside [%s, %s]", n, r.min, r.max)
	}
	return n, nil
}

// maxDecimalLen and maxDecimalExp bound the decimal text integralDecimal will expand: an exponent
// is a request to build a number of that many digits, and the value here is a command argument, not
// a bulk transfer. Every integer type is far below 10^40, so a larger exponent can only be refused.
const (
	maxDecimalLen = 128
	maxDecimalExp = 1000
)

var jsonNumberRE = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// integralDecimal reads a JSON decimal or exponent number exactly and returns it only when it is a
// whole number (7.0 and 1e3 are; 1.0000000000000001 and 1e-400 are not).
func integralDecimal(s string) (*big.Int, error) {
	if len(s) > maxDecimalLen || !jsonNumberRE.MatchString(s) {
		return nil, refuse("%q is not a number this command accepts", clip(s))
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp := strings.TrimPrefix(s[i+1:], "+")
		neg := strings.HasPrefix(exp, "-")
		digits := strings.TrimLeft(strings.TrimPrefix(exp, "-"), "0")
		if len(digits) > 4 || (len(digits) > 0 && func() bool { e, _ := strconv.Atoi(digits); return e > maxDecimalExp }()) {
			if neg {
				return nil, refuse("%q is not integral", clip(s))
			}
			return nil, refuse("%q is outside any integer range", clip(s))
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, refuse("%q is not a number", clip(s))
	}
	if !r.IsInt() {
		return nil, refuse("%s is not integral", clip(s))
	}
	return new(big.Int).Set(r.Num()), nil
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// integralFloat converts a float to an integer only when that loses nothing.
func integralFloat(datatype uint32, f float64) (*big.Int, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, refuse("not a finite number")
	}
	if f != math.Trunc(f) {
		return nil, refuse("%v is not integral", f)
	}
	if math.Abs(f) >= maxExactFloatInt {
		if datatype == datatypeInt64 || datatype == datatypeUInt64 {
			return nil, refuse("%v is beyond 2^53: send a 64-bit integer as a string of digits", f)
		}
		return nil, refuse("%v is beyond 2^53 and cannot be an exact integer", f)
	}
	return big.NewInt(int64(f)), nil
}

func exactFloat(value any) (float64, error) {
	var f float64
	switch v := value.(type) {
	case float64:
		f = v
	case float32:
		f = float64(v)
	case json.Number:
		var err error
		if f, err = v.Float64(); err != nil {
			return 0, refuse("%q is not a number", v.String())
		}
	case int:
		f = float64(v)
	case int8:
		f = float64(v)
	case int16:
		f = float64(v)
	case int32:
		f = float64(v)
	case int64:
		f = float64(v)
	case uint:
		f = float64(v)
	case uint8:
		f = float64(v)
	case uint16:
		f = float64(v)
	case uint32:
		f = float64(v)
	case uint64:
		f = float64(v)
	default:
		return 0, refuse("a floating-point type takes a number, got %s", className(value))
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, refuse("not a finite number")
	}
	return f, nil
}

// BuildMetricCommand encodes one metric write into an NCMD/DCMD payload.
//
//   - [tck-id-payloads-ncmd-timestamp] / [tck-id-payloads-dcmd-timestamp]: the payload carries a
//     timestamp denoting when the message was published (ts, milliseconds since the epoch).
//   - [tck-id-payloads-ncmd-seq] / [tck-id-payloads-dcmd-seq]: NO sequence number.
//   - [tck-id-payloads-alias-data-cmd-requirement]: "NDATA, DDATA, NCMD, and DCMD messages MUST
//     only include an alias and the metric name MUST be excluded" - so a metric the BIRTH
//     aliased is addressed by alias alone, and a node that does not alias is addressed by name.
//   - the datatype is ALWAYS set. This is a recorded deviation from
//     [tck-id-payloads-metric-datatype-not-req] (the datatype SHOULD NOT be included in NCMD/DCMD
//     metrics): it makes the encoding explicit and costs a conformant node nothing.
//   - the metric timestamp is omitted ([tck-id-payloads-name-cmd-requirement]: it MAY be included
//     in NCMD/DCMD metrics).
//
// ref must be a current ref (see RevalidateMetric); value is encoded against ref.Datatype.
func BuildMetricCommand(ref MetricRef, value any, ts int64) ([]byte, error) {
	set, err := EncodeMetricValue(ref.Datatype, value)
	if err != nil {
		return nil, err
	}
	m := &sppb.Payload_Metric{Datatype: proto.Uint32(ref.Datatype)}
	if ref.HasAlias {
		m.Alias = proto.Uint64(ref.Alias)
	} else {
		if strings.TrimSpace(ref.Name) == "" {
			return nil, errors.New("a metric with no alias needs a name")
		}
		m.Name = proto.String(ref.Name)
	}
	set(m)
	return codec.Encode(&sppb.Payload{Timestamp: proto.Uint64(uint64(ts)), Metrics: []*sppb.Payload_Metric{m}})
}
