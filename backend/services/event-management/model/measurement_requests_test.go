// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The string to row mapping lives here so the persistence worker and a backfill build a row the
// same way. The worker's own tests (processor) pin its behaviour through it.
func TestMeasurementRequestForMapsEveryField(t *testing.T) {
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cls, unit, dt := uint64(7), "W", "DOUBLE"
	ev := Event{DeviceToken: "d-1"}

	plain, err := MeasurementRequestFor(ev, at, MeasurementReading{Name: "a", Value: "1.5"})
	require.NoError(t, err)
	assert.Equal(t, "a", plain.Name)
	assert.Equal(t, 1.5, *plain.Value)
	assert.Nil(t, plain.Classifier)
	assert.Nil(t, plain.Unit)
	assert.Nil(t, plain.DataType)
	assert.False(t, plain.Derived)
	assert.Equal(t, at, plain.EntryOccurredTime)
	assert.Equal(t, "d-1", plain.Event.DeviceToken)

	derived, err := MeasurementRequestFor(ev, at.Add(time.Second),
		MeasurementReading{Name: "power", Value: "-9", Classifier: &cls, Unit: &unit, DataType: &dt, Derived: true})
	require.NoError(t, err)
	assert.Equal(t, -9.0, *derived.Value)
	assert.Equal(t, uint(7), *derived.Classifier)
	assert.Equal(t, &unit, derived.Unit)
	assert.Equal(t, &dt, derived.DataType)
	assert.True(t, derived.Derived)
	assert.Equal(t, at.Add(time.Second), derived.EntryOccurredTime)
}

func TestMeasurementRequestForRefusesWhatCannotBeStored(t *testing.T) {
	for _, bad := range []string{"NaN", "+Inf", "-Inf", "abc", ""} {
		_, err := MeasurementRequestFor(Event{}, time.Time{}, MeasurementReading{Name: "m", Value: bad})
		require.Error(t, err, bad)
		assert.True(t, errors.Is(err, ErrDeterministic), bad)
	}
}

// A STRING reading is stored as text with no number: Value stays nil, TextValue carries it, and a
// numeric type or a derived value is unchanged.
func TestMeasurementRequestForKeepsAStringReadingAsText(t *testing.T) {
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	str := "STRING"
	req, err := MeasurementRequestFor(Event{DeviceToken: "d-1"}, at, MeasurementReading{Name: "mode", Value: "AUTO", DataType: &str})
	require.NoError(t, err)
	assert.Nil(t, req.Value)
	require.NotNil(t, req.TextValue)
	assert.Equal(t, "AUTO", *req.TextValue)
	assert.Equal(t, &str, req.DataType)

	// even text that parses as a number stays text: the declared type decides
	num, err := MeasurementRequestFor(Event{}, at, MeasurementReading{Name: "code", Value: "42", DataType: &str})
	require.NoError(t, err)
	assert.Nil(t, num.Value)
	assert.Equal(t, "42", *num.TextValue)

	// a numeric reading never carries text, and a non-numeric one is still refused
	dbl := "DOUBLE"
	d, err := MeasurementRequestFor(Event{}, at, MeasurementReading{Name: "t", Value: "1.5", DataType: &dbl})
	require.NoError(t, err)
	assert.Nil(t, d.TextValue)
	_, err = MeasurementRequestFor(Event{}, at, MeasurementReading{Name: "t", Value: "AUTO", DataType: &dbl})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDeterministic))
}

// The identity of a numeric row, and so of every row stored before text values existed, does not
// move; a text value is part of the identity of a STRING row.
func TestTextValueIsInThePayloadIdentityOnlyWhenPresent(t *testing.T) {
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	str := "STRING"
	a, b := "AUTO", "MANUAL"
	base := func(text *string) *MeasurementEventCreateRequest {
		return &MeasurementEventCreateRequest{Event: Event{EventId: []byte("e1")}, EntryOccurredTime: at,
			Name: "mode", DataType: &str, TextValue: text}
	}
	none, err := DeriveMeasurementPayloadId(base(nil))
	require.NoError(t, err)
	ida, err := DeriveMeasurementPayloadId(base(&a))
	require.NoError(t, err)
	idb, err := DeriveMeasurementPayloadId(base(&b))
	require.NoError(t, err)
	assert.NotEqual(t, ida, idb)
	assert.NotEqual(t, none, ida)
	again, err := DeriveMeasurementPayloadId(base(&a))
	require.NoError(t, err)
	assert.Equal(t, ida, again)
}

// The migration follows the previous chain; its SQL (nullable, no default, re-runnable) is exercised against
// a real database by hack/migration-diff.sh verify and replay.
func TestMeasurementTextValueMigrationRemainsAfterRollup1h(t *testing.T) {
	for i, migration := range Migrations {
		if migration.ID == NewMeasurementTextValueSchema().ID {
			require.Greater(t, i, 0)
			assert.Equal(t, NewRollup1hSchema().ID, Migrations[i-1].ID)
			return
		}
	}
	t.Fatal("measurement text-value migration is missing")
}

// A STRING value the store cannot hold is refused HERE, as deterministic. PostgreSQL rejects a NUL
// byte and invalid UTF-8 (22021) for the whole insert, so letting one through gives up every other
// reading of the message, numeric ones included.
func TestMeasurementRequestForRefusesAStringValueThatCannotBeStored(t *testing.T) {
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	str := DataTypeString
	bad := map[string]string{
		"NUL":          "ab\x00cd",
		"invalid UTF8": "ab\xffcd",
		"truncated":    "ab\xe2\x82",
		"over bound":   strings.Repeat("a", MaxTextValueBytes+1),
	}
	for name, v := range bad {
		_, err := MeasurementRequestFor(Event{}, at, MeasurementReading{Name: "mode", Value: v, DataType: &str})
		require.Error(t, err, name)
		assert.True(t, errors.Is(err, ErrDeterministic), name)
	}
	// the counterweight: the bound itself, multi-byte text and the empty string are stored
	for name, v := range map[string]string{
		"at the bound": strings.Repeat("a", MaxTextValueBytes),
		"multibyte":    "temp: 21°C ✓",
		"empty":        "",
	} {
		req, err := MeasurementRequestFor(Event{}, at, MeasurementReading{Name: "mode", Value: v, DataType: &str})
		require.NoError(t, err, name)
		assert.Equal(t, v, *req.TextValue, name)
	}
	// and a derived reading is not a text reading: its value is still parsed as a number
	_, err := MeasurementRequestFor(Event{}, at, MeasurementReading{Name: "d", Value: "ab", DataType: &str, Derived: true})
	require.Error(t, err)
}

// The identity of a numeric reading, pinned as a literal: the digest was measured on the tree before
// text values existed, and any change to the preimage of a row with no text moves it.
func TestNumericPayloadIdentityIsFrozen(t *testing.T) {
	cls := uint(7)
	unit, dt := "C", "DOUBLE"
	v := 21.5
	req := &MeasurementEventCreateRequest{
		Event:             Event{EventId: []byte("e1")},
		EntryOccurredTime: time.Date(2026, 11, 22, 12, 0, 0, 123, time.UTC),
		Name:              "t", Value: &v, Classifier: &cls, Unit: &unit, DataType: &dt,
	}
	id, err := DeriveMeasurementPayloadId(req)
	require.NoError(t, err)
	assert.Equal(t, "849edbcecaea539c96a49498752bd868732f1165735e3034d0f3413e6bcbd4bd", fmt.Sprintf("%x", id))
}
