// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
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

// MeasurementRequestsFor maps a resolved measurements payload to one create request per reading
// of every entry, under the message's base event. It is the one definition of the string to row
// mapping: the persistence worker writes live events through it and a backfill writes the values
// it computes through it, so a derived row is built exactly as a live one is.
//
// A value that is not a finite number is refused as deterministic. NaN and +/-Inf parse, and the
// payload identity cannot canonicalize them (JSON has no such number), so the failure used to
// read as transient and the message was redelivered until its budget ran out. No delivery can
// change the bytes.
func MeasurementRequestsFor(event Event, payload dmmodel.ResolvedMeasurementsPayload) ([]*MeasurementEventCreateRequest, error) {
	requests := make([]*MeasurementEventCreateRequest, 0)
	for _, mxentry := range payload.Entries {
		for _, mx := range mxentry.Entries {
			val := mx.Value
			fval, err := ParseNullableFloat64(&val)
			if err != nil {
				return nil, err
			}
			if math.IsNaN(*fval) || math.IsInf(*fval, 0) {
				return nil, fmt.Errorf("%w: measurement %q: %q is not a finite number",
					ErrDeterministic, mx.Name, val)
			}
			var classifier *uint
			if mx.Classifier != nil {
				c := uint(*mx.Classifier)
				classifier = &c
			}
			requests = append(requests, &MeasurementEventCreateRequest{
				Event:             event,
				EntryOccurredTime: mxentry.OccurredTime,
				Name:              mx.Name,
				Value:             fval,
				Classifier:        classifier,
				Unit:              mx.Unit,
				DataType:          mx.DataType,
				Derived:           mx.Derived,
			})
		}
	}
	return requests, nil
}
