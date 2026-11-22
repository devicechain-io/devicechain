// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrDeterministic marks a failure that no amount of redelivery can fix: bad data, such as a
// non-numeric or non-finite measurement. The persistence worker abandons such an event on its
// first failure (see processor.ErrDeterministic, which is this value); a caller that is not
// redelivering anything, such as a backfill, treats it as a refusal of that one reading.
var ErrDeterministic = errors.New("deterministic persistence failure")

// ParseNullableFloat64 parses an optional numeric string: nil stays nil, and text that is not a
// number is refused as deterministic.
func ParseNullableFloat64(val *string) (*float64, error) {
	if val == nil {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(*val, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not numeric: %v", ErrDeterministic, *val, err)
	}
	return &parsed, nil
}

// MeasurementReading is one reading of a resolved measurements entry, as the resolved event
// carries it: the value as text. Its fields are the wire entry's, with the same names and types
// in the same order, so the persistence worker turns a wire entry into one with a plain Go
// conversion, and the compiler refuses that conversion the day the two shapes differ. It is
// declared here, not imported, so model does not depend on the service that owns the wire type.
type MeasurementReading struct {
	Name       string
	Value      string
	Classifier *uint64
	Unit       *string
	DataType   *string
	Derived    bool
}

// DataTypeString is the declared data type of a reading whose value is text. A reading of this
// type is stored in text_value and never in the numeric column.
const DataTypeString = "STRING"

// MaxTextValueBytes bounds a STRING reading's value. The column is unbounded text, so without
// a bound one reading could carry as much as the message does; 4 KiB is room for any state,
// mode or short code a device reports, and a longer value is a payload that belongs elsewhere.
const MaxTextValueBytes = 4096

// checkTextValue refuses, as deterministic, a STRING value that cannot be stored or that is
// over the bound. Each refusal is one no redelivery changes, and each must stop HERE: a NUL
// byte is rejected by PostgreSQL (22021) for the whole insert, and an invalid UTF-8 sequence
// likewise (22021), which would give up every other reading of the same message, numeric ones
// included.
func checkTextValue(name, v string) error {
	if len(v) > MaxTextValueBytes {
		return fmt.Errorf("%w: measurement %q: a STRING value is %d bytes, over the %d-byte bound",
			ErrDeterministic, name, len(v), MaxTextValueBytes)
	}
	if !utf8.ValidString(v) {
		return fmt.Errorf("%w: measurement %q: a STRING value is not valid UTF-8", ErrDeterministic, name)
	}
	if strings.IndexByte(v, 0) >= 0 {
		return fmt.Errorf("%w: measurement %q: a STRING value contains a NUL byte", ErrDeterministic, name)
	}
	return nil
}

// MeasurementRequestFor maps one reading, taken at the entry instant at, to its create request
// under the message's base event. It is the one definition of the string to row mapping: the
// persistence worker writes live events through it and a backfill writes the values it computes
// through it, so a derived row is built exactly as a live one is.
//
// A value that is not a finite number is refused as deterministic. NaN and +/-Inf parse, and the
// payload identity cannot canonicalize them (JSON has no such number), so the failure used to
// read as transient and the message was redelivered until its budget ran out. No delivery can
// change the bytes.
func MeasurementRequestFor(event Event, at time.Time, mx MeasurementReading) (*MeasurementEventCreateRequest, error) {
	val := mx.Value
	// A STRING reading is not a number: its value is stored as text and Value stays nil, so
	// nothing that aggregates the numeric column sees it. Only a STRING one — a numeric type
	// whose value does not parse is still a refusal below.
	if mx.DataType != nil && *mx.DataType == DataTypeString && !mx.Derived {
		if err := checkTextValue(mx.Name, val); err != nil {
			return nil, err
		}
		var classifier *uint
		if mx.Classifier != nil {
			c := uint(*mx.Classifier)
			classifier = &c
		}
		return &MeasurementEventCreateRequest{
			Event:             event,
			EntryOccurredTime: at,
			Name:              mx.Name,
			Classifier:        classifier,
			Unit:              mx.Unit,
			DataType:          mx.DataType,
			TextValue:         &val,
		}, nil
	}
	fval, err := ParseNullableFloat64(&val)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(*fval) || math.IsInf(*fval, 0) {
		return nil, fmt.Errorf("%w: measurement %q: %q is not a finite number", ErrDeterministic, mx.Name, val)
	}
	var classifier *uint
	if mx.Classifier != nil {
		c := uint(*mx.Classifier)
		classifier = &c
	}
	return &MeasurementEventCreateRequest{
		Event:             event,
		EntryOccurredTime: at,
		Name:              mx.Name,
		Value:             fval,
		Classifier:        classifier,
		Unit:              mx.Unit,
		DataType:          mx.DataType,
		Derived:           mx.Derived,
	}, nil
}
