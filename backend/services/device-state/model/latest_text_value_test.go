// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func str(s string) *string { return &s }

// A STRING reading's text is kept in text_value, the numeric column stays NULL, and a newer
// reading of either kind replaces the whole row's value pair: a text reading never leaves a
// stale number beside it, nor a number a stale text.
func TestALatestTextReadingIsKeptAndReplacedAsAPair(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	dt := str(DataTypeString)

	require.NoError(t, api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{
		{Name: "mode", TextValue: str("heating"), DataType: dt, OccurredTime: t0}}))
	got := mustReading(t, api, ctx, "d1", "mode")
	require.NotNil(t, got.TextValue)
	require.Equal(t, "heating", *got.TextValue)
	require.False(t, got.Value.Valid, "a text reading leaves the numeric value NULL")

	// An older or equal text reading does not replace it.
	require.NoError(t, api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{
		{Name: "mode", TextValue: str("idle"), DataType: dt, OccurredTime: t0}}))
	require.Equal(t, "heating", *mustReading(t, api, ctx, "d1", "mode").TextValue)

	// A newer text reading does.
	require.NoError(t, api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{
		{Name: "mode", TextValue: str("idle"), DataType: dt, OccurredTime: t0.Add(time.Second)}}))
	require.Equal(t, "idle", *mustReading(t, api, ctx, "d1", "mode").TextValue)

	// A newer numeric reading under the same name clears the text.
	require.NoError(t, api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{
		{Name: "mode", Value: num(3), OccurredTime: t0.Add(2 * time.Second)}}))
	got = mustReading(t, api, ctx, "d1", "mode")
	require.Nil(t, got.TextValue)
	require.Equal(t, 3.0, got.Value.Float64)
}

// Two readings of one name in one batch fold to the newer, text and all.
func TestCoalescingCarriesTheNewerReadingsText(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	rows := coalesceMeasurements("d1", []LatestMeasurementInput{
		{Name: "mode", TextValue: str("a"), OccurredTime: t0},
		{Name: "mode", TextValue: str("b"), OccurredTime: t0.Add(time.Second)},
		{Name: "mode", TextValue: str("c"), OccurredTime: t0.Add(-time.Second)},
	})
	require.Len(t, rows, 1)
	require.Equal(t, "b", *rows[0].TextValue)
}

func TestStorableTextValue(t *testing.T) {
	require.True(t, StorableTextValue(""))
	require.True(t, StorableTextValue("heating é中"))
	require.True(t, StorableTextValue(strings.Repeat("a", MaxTextValueBytes)))
	require.False(t, StorableTextValue(strings.Repeat("a", MaxTextValueBytes+1)))
	require.False(t, StorableTextValue("a\x00b"), "PostgreSQL refuses a NUL byte (22021)")
	require.False(t, StorableTextValue("a\xffb"), "PostgreSQL refuses invalid UTF-8 (22021)")
}
