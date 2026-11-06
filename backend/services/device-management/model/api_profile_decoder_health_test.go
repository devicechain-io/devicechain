// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The accounting store: absolute upserts, the sample cap, and the tenant boundary of every read.

// healthFixture is the decoder fixture plus the unique index the migration declares, which the
// SQLite AutoMigrate of the live model does not carry and the upsert's conflict target needs.
func newHealthFixture(t *testing.T) *decoderFixture {
	t.Helper()
	f := newDecoderFixture(t)
	require.NoError(t, f.api.RDB.DB(f.ctx).Exec(
		`CREATE UNIQUE INDEX uix_profile_decoder_healths_window ON profile_decoder_healths
		 (tenant_id, device_profile_id, profile_version, window_start, replica)`).Error)
	return f
}

func writerCtx(ctx context.Context) context.Context {
	return asCaller(ctx, nil, auth.DeviceRead, auth.DeviceWrite)
}

func TestHealthIsWrittenAsTheReplicasAbsoluteValueAndReadBackSummed(t *testing.T) {
	f := newHealthFixture(t)
	w := time.Now().UTC().Truncate(time.Minute)
	row := func(replica string, decoded, failed int64) DecoderHealthWrite {
		fa := time.Now().UTC()
		return DecoderHealthWrite{ProfileToken: "meter", Version: 1, WindowStart: w, Replica: replica,
			Decoded: decoded, Failed: failed, Suppressed: 2, LastFailureAt: &fa, LastError: "decode_failed: eval_error: " + replica}
	}
	n, skipped, err := f.api.WriteDecoderHealth(f.ctx, []DecoderHealthWrite{row("pod-a", 10, 1), row("pod-b", 5, 0)})
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Zero(t, skipped)

	// pod-a's later flush carries its cumulative value; it replaces, it does not add.
	_, _, err = f.api.WriteDecoderHealth(f.ctx, []DecoderHealthWrite{row("pod-a", 12, 3)})
	require.NoError(t, err)
	_, _, err = f.api.WriteDecoderHealth(f.ctx, []DecoderHealthWrite{row("pod-a", 12, 3)})
	require.NoError(t, err)

	h, err := f.api.PayloadDecoderHealth(writerCtx(f.ctx), "meter", 60)
	require.NoError(t, err)
	assert.EqualValues(t, 17, h.Decoded, "12 from pod-a (replaced, twice written) + 5 from pod-b")
	assert.EqualValues(t, 3, h.Failed)
	assert.EqualValues(t, 4, h.Suppressed)
	assert.Equal(t, 2, h.Windows)
	require.NotNil(t, h.LastFailureAt)
	assert.True(t, strings.HasPrefix(h.LastError, "decode_failed: eval_error: pod-"))
}

func TestHealthForADeletedProfileIsSkippedNotFatal(t *testing.T) {
	f := newHealthFixture(t)
	n, skipped, err := f.api.WriteDecoderHealth(f.ctx, []DecoderHealthWrite{
		{ProfileToken: "gone", Version: 1, WindowStart: time.Now().UTC().Truncate(time.Minute), Replica: "pod-a", Decoded: 1},
		{ProfileToken: "meter", Version: 1, WindowStart: time.Now().UTC().Truncate(time.Minute), Replica: "pod-a", Decoded: 1},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, skipped)
}

func TestHealthAndSamplesOfOneTenantAreInvisibleToAnother(t *testing.T) {
	f := newHealthFixture(t)
	_, _, err := f.api.WriteDecoderHealth(f.ctx, []DecoderHealthWrite{
		{ProfileToken: "meter", Version: 1, WindowStart: time.Now().UTC().Truncate(time.Minute), Replica: "pod-a", Decoded: 9}})
	require.NoError(t, err)
	ok, err := f.api.StoreDecoderSample(f.ctx, DecoderSampleWrite{ProfileToken: "meter", Version: 1, DeviceToken: "d1",
		Reason: "eval_error", Input: []byte(`{"frameHex":"00"}`)}, time.Now())
	require.NoError(t, err)
	require.True(t, ok)

	other := writerCtx(putest.TenantContext("globex")())
	// globex has no profile "meter": not found, indistinguishable from one that never existed.
	_, err = f.api.PayloadDecoderHealth(other, "meter", 60)
	require.Error(t, err)
	_, err = f.api.PayloadDecoderSamples(other, "meter", 10)
	require.Error(t, err)

	// And acme's own rows are there.
	s, err := f.api.PayloadDecoderSamples(writerCtx(f.ctx), "meter", 10)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.Equal(t, "d1", s[0].DeviceToken)
}

func TestWritesRefuseAContextWithNoTenant(t *testing.T) {
	f := newHealthFixture(t)
	_, _, err := f.api.WriteDecoderHealth(context.Background(), []DecoderHealthWrite{{ProfileToken: "meter"}})
	require.Error(t, err)
	_, err = f.api.StoreDecoderSample(context.Background(), DecoderSampleWrite{ProfileToken: "meter"}, time.Now())
	require.Error(t, err)
	require.Error(t, f.api.TrimDecoderHealth(context.Background(), time.Now()))
}

func TestAProfileKeepsItsNewest20Samples(t *testing.T) {
	f := newHealthFixture(t)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < MaxDecoderSamplesPerProfile+7; i++ {
		_, err := f.api.StoreDecoderSample(f.ctx, DecoderSampleWrite{ProfileToken: "meter", Version: 1,
			DeviceToken: "dev-" + string(rune('a'+i%26)), Reason: "eval_error",
			Error: "n=" + time.Duration(i).String(), Input: []byte(`{}`)}, base.Add(time.Duration(i)*time.Second))
		require.NoError(t, err)
	}
	got, err := f.api.PayloadDecoderSamples(writerCtx(f.ctx), "meter", 100)
	require.NoError(t, err)
	require.Len(t, got, MaxDecoderSamplesPerProfile)
	assert.Equal(t, "n="+time.Duration(MaxDecoderSamplesPerProfile+6).String(), got[0].Error, "newest first")
	assert.Equal(t, "n="+time.Duration(7).String(), got[len(got)-1].Error, "the 7 oldest were trimmed")
}

func TestAnOversizedSampleIsRefusedNotTruncated(t *testing.T) {
	f := newHealthFixture(t)
	_, err := f.api.StoreDecoderSample(f.ctx, DecoderSampleWrite{ProfileToken: "meter",
		Input: make([]byte, MaxDecoderSampleBytes+1)}, time.Now())
	require.ErrorIs(t, err, ErrDecoderSampleTooLarge)
}

func TestSamplesAndHealthNeedDeviceWriteAndSamplesNeverListTheirBytes(t *testing.T) {
	f := newHealthFixture(t)
	_, err := f.api.StoreDecoderSample(f.ctx, DecoderSampleWrite{ProfileToken: "meter", Version: 1, DeviceToken: "d1",
		Reason: "eval_error", Input: []byte(`{"frameHex":"deadbeef"}`)}, time.Now())
	require.NoError(t, err)

	reader := asCaller(f.ctx, nil, auth.DeviceRead)
	_, err = f.api.PayloadDecoderHealth(reader, "meter", 60)
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = f.api.PayloadDecoderSamples(reader, "meter", 10)
	assert.ErrorIs(t, err, auth.ErrForbidden)

	scoped := asCaller(f.ctx, []string{"customer:acme-corp"}, auth.DeviceRead, auth.DeviceWrite)
	_, err = f.api.PayloadDecoderSamples(scoped, "meter", 10)
	assert.ErrorIs(t, err, auth.ErrScopeUnsupported)

	// The listing carries no input: DecoderSampleSummary has no field for it.
	s, err := f.api.PayloadDecoderSamples(writerCtx(f.ctx), "meter", 10)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.NotZero(t, s[0].ID)
}

func TestTrimDeletesOnlyTheTenantsRowsPastTheCutoff(t *testing.T) {
	f := newHealthFixture(t)
	old := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Minute)
	fresh := time.Now().UTC().Truncate(time.Minute)
	_, _, err := f.api.WriteDecoderHealth(f.ctx, []DecoderHealthWrite{
		{ProfileToken: "meter", Version: 1, WindowStart: old, Replica: "pod-a", Decoded: 1},
		{ProfileToken: "meter", Version: 1, WindowStart: fresh, Replica: "pod-a", Decoded: 2},
	})
	require.NoError(t, err)
	require.NoError(t, f.api.TrimDecoderHealth(core.WithTenant(context.Background(), partialUpdateTenant),
		time.Now().Add(-DecoderHealthRetention)))
	var n int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&ProfileDecoderHealth{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}
