// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "fmt"

// CodeNotAggregatable is the GraphQL extensions.code of a refusal to aggregate a measurement
// whose readings are text.
const CodeNotAggregatable = "NOT_AGGREGATABLE"

// NotAggregatableError refuses an aggregation over a measurement that holds only text readings
// (a STRING metric). Average, minimum, maximum and sum of text mean nothing, and a bucket
// with a count of zero and no value would read as "the metric is flat" rather than "this is not
// a number". It is returned directly, so the GraphQL layer carries its code.
type NotAggregatableError struct {
	Name string
}

func (e *NotAggregatableError) Error() string {
	return fmt.Sprintf("measurement %q holds text readings, which cannot be aggregated: read them with measurementEvents", e.Name)
}

// Extensions gives the error its wire code.
func (e *NotAggregatableError) Extensions() map[string]any {
	return map[string]any{"code": CodeNotAggregatable}
}

// numericBucketsOnly applies the rule to an aggregation's buckets. A bucket whose readings are
// all text has no non-null value, so its count (the number of non-null values) is zero; a numeric
// reading always has a value, so no numeric bucket is dropped. A request that named the
// measurement is refused; one that asked for every measurement gets the numeric ones, so a
// device that also reports a mode or a state does not fail the whole read.
func numericBucketsOnly(buckets []MeasurementBucket, named *string) ([]MeasurementBucket, error) {
	kept := buckets[:0:0]
	for _, b := range buckets {
		if b.Count > 0 {
			kept = append(kept, b)
			continue
		}
		if named != nil {
			return nil, &NotAggregatableError{Name: b.Name}
		}
	}
	if len(kept) == len(buckets) {
		return buckets, nil
	}
	return kept, nil
}
