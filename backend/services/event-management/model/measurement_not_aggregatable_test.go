// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNumericBucketsOnly(t *testing.T) {
	numeric := MeasurementBucket{Name: "temp", Count: 3}
	text := MeasurementBucket{Name: "mode", Count: 0}
	named := "mode"

	// every name: the text-only bucket is left out, the numeric one stays
	got, err := numericBucketsOnly([]MeasurementBucket{numeric, text, numeric}, nil)
	require.NoError(t, err)
	assert.Equal(t, []MeasurementBucket{numeric, numeric}, got)

	// nothing to drop: the same slice comes back
	all := []MeasurementBucket{numeric}
	got, err = numericBucketsOnly(all, nil)
	require.NoError(t, err)
	assert.Equal(t, all, got)

	// a named request for the text measurement is refused, with the typed error
	_, err = numericBucketsOnly([]MeasurementBucket{text}, &named)
	var na *NotAggregatableError
	require.True(t, errors.As(err, &na))
	assert.Equal(t, "mode", na.Name)
	assert.Equal(t, CodeNotAggregatable, na.Extensions()["code"])

	// a named request for a numeric measurement is untouched
	temp := "temp"
	got, err = numericBucketsOnly([]MeasurementBucket{numeric}, &temp)
	require.NoError(t, err)
	assert.Equal(t, []MeasurementBucket{numeric}, got)

	// no buckets is no buckets, not a refusal
	got, err = numericBucketsOnly(nil, &named)
	require.NoError(t, err)
	assert.Empty(t, got)
}
