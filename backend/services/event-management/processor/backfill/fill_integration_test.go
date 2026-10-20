// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package backfill

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// sampleMessages are the readings every fill test seeds: nanosecond instants, one in a +02:00
// zone, a multi-entry message whose readings share an instant (one sample), a message whose
// readings do not (two samples, each short of an input), and a message with too few inputs. They
// span three days, so they land in several chunks.
func sampleMessages(now time.Time) []message {
	plus2 := time.FixedZone("plus2", 7200)
	at := func(d time.Duration, ns int) time.Time {
		return now.Add(-d).Truncate(time.Second).Add(time.Duration(ns))
	}
	t1 := at(2*time.Hour, 123456789)
	t2 := at(3*time.Hour, 987654321).In(plus2)
	t4 := at(26*time.Hour, 500)
	t5 := at(27*time.Hour, 0)
	return []message{
		{[]entry{{t1, "voltage", "230.5"}, {t1, "current", "10.25"}}},
		{[]entry{{t2, "voltage", "100"}, {t2, "current", "0.5"}}},
		{[]entry{{at(4*time.Hour, 0), "voltage", "12.5"}}},
		{[]entry{{t4, "voltage", "240"}, {t4, "current", "5"}, {t4, "fan", "3"}}},
		{[]entry{{t5, "voltage", "10"}, {t5.Add(time.Second), "current", "2"}}},
		{[]entry{{at(50*time.Hour, 7), "voltage", "220"}, {at(50*time.Hour, 7), "current", "1.5"}}},
		{[]entry{{at(51*time.Hour, 0), "voltage", "0"}, {at(51*time.Hour, 0), "current", "0"}}},
	}
}

// The end-to-end fill produces EXACTLY the derived rows the live path stores, for a device that
// has none: the same instants, values, classifiers, units and data types, including a
// nanosecond instant, one with a zone offset, and a message of several same-instant entries.
// The oracle is the live resolver itself (a device-management ProfileResolution), and its rows
// are written through the real persist path.
func TestIntegrationFillProducesWhatLiveStores(t *testing.T) {
	r := newRig(t, "e2e")
	now := time.Now().UTC()
	msgs := sampleMessages(now)
	md := datatypes.JSON(`{"gain": 2}`)
	r.seed("d-live", msgs, newLiveOracle(), &md)
	r.seed("d-bf", msgs, nil, nil)
	liveRows := r.derived("d-live")
	require.NotEmpty(t, liveRows)
	require.Empty(t, r.derived("d-bf"))

	created := now.Add(-100 * 24 * time.Hour)
	r.roster.devices = []RosterEntry{device(1, "d-live", created, fptr(2)), device(2, "d-bf", created, fptr(2))}
	r.build()
	r.newJob("j1", now.Add(-72*time.Hour), now)
	j := r.runUntilDone("j1")

	require.Equal(t, model.BackfillSucceeded, j.Status, "failure: %v", j.Failure)
	assert.Equal(t, withoutDevice(liveRows), withoutDevice(r.derived("d-bf")),
		"a filled device holds exactly the rows live stored")
	assert.Equal(t, liveRows, r.derived("d-live"), "a device that already had them is not touched")

	assert.Equal(t, int64(len(liveRows)), j.Written)
	assert.Equal(t, int64(len(liveRows)), j.AlreadyPresent, "the live device's rows were found, not rewritten")
	assert.Equal(t, j.Written+j.AlreadyPresent, j.Computed)
	assert.Equal(t, int64(2), j.DevicesDone)
	require.NotNil(t, j.DevicesTotal)
	assert.Equal(t, int64(2), *j.DevicesTotal)
	skips, err := j.SkippedCounts()
	require.NoError(t, err)
	assert.NotEmpty(t, skips, "the short messages are counted under a reason: %v", skips)
	t.Logf("job: %+v", j)
}
