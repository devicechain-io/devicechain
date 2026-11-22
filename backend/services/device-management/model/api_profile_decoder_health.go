// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The accounting half of payload decoders: what the resolver writes (a replica's absolute tally for
// a minute, a stored failing uplink) and what the tenant reads back (health and failure samples).

const (
	// MaxDecoderSamplesPerProfile is how many failing uplinks a profile keeps, newest first. A new one
	// trims the oldest on insert.
	MaxDecoderSamplesPerProfile = 20
	// DecoderHealthRetention is how long health rows are kept: the failed-events retention horizon,
	// so a failure count and the records it counts expire together.
	DecoderHealthRetention = 7 * 24 * time.Hour
	// MaxDecoderSampleBytes bounds a stored sample's input. A sample is the canonical JSON of an
	// uplink (at most 64 KiB of json, 64 KiB of decoded object and a 4 KiB frame in hex); anything
	// larger is refused rather than stored truncated, because a truncated sample is not the input.
	MaxDecoderSampleBytes = 160 << 10
	// MaxDecoderSampleListing caps one page of samples.
	MaxDecoderSampleListing = 100
	// MaxDecoderHealthWindows caps the windows one health read sums (a week of minutes is 10080).
	maxDecoderHealthWindowMinutes = 7 * 24 * 60
)

// ErrDecoderSampleTooLarge is a sample above MaxDecoderSampleBytes.
var ErrDecoderSampleTooLarge = errors.New("the failing uplink is too large to keep as a sample")

// DecoderHealthWrite is one replica's cumulative tally for one version of a profile in one minute.
// Every count is ABSOLUTE for (replica, window): the write SETs it, so applying it twice is applying
// it once.
type DecoderHealthWrite struct {
	ProfileToken  string
	Version       int32
	WindowStart   time.Time
	Replica       string
	Decoded       int64
	Ignored       int64
	Failed        int64
	Suppressed    int64
	ChargeSkew    int64
	LastFailureAt *time.Time
	LastError     string
}

// WriteDecoderHealth upserts the replica's tallies for the tenant in ctx. A row whose profile is
// gone (deleted since the decode) is skipped, and counted in the second result, rather than failing
// the rest: its rows would cascade away with the profile anyway.
//
// This is the service's own write (no caller identity), reached only from the resolver's flusher.
func (api *Api) WriteDecoderHealth(ctx context.Context, rows []DecoderHealthWrite) (written, skipped int, err error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return 0, 0, errors.New("no tenant in the request context")
	}
	ids := map[string]uint{}
	for _, r := range rows {
		id, known := ids[r.ProfileToken]
		if !known {
			p, perr := api.deviceProfileByToken(ctx, r.ProfileToken)
			switch {
			case errors.Is(perr, gorm.ErrRecordNotFound):
				ids[r.ProfileToken] = 0
				skipped++
				continue
			case perr != nil:
				return written, skipped, perr
			}
			id = p.ID
			ids[r.ProfileToken] = id
		}
		if id == 0 {
			skipped++
			continue
		}
		// The failure time is stored in UTC, like the window: the last error is the row with the
		// latest failure, and a store that keeps a time in the zone it was handed (SQLite keeps text)
		// would otherwise order a later failure written in a western zone before an earlier one.
		var lastFailureAt *time.Time
		if r.LastFailureAt != nil {
			t := r.LastFailureAt.UTC()
			lastFailureAt = &t
		}
		row := ProfileDecoderHealth{
			DeviceProfileId: id, ProfileVersion: r.Version, WindowStart: r.WindowStart.UTC(), Replica: r.Replica,
			Decoded: r.Decoded, Ignored: r.Ignored, Failed: r.Failed, Suppressed: r.Suppressed,
			ChargeSkew: r.ChargeSkew, LastFailureAt: lastFailureAt, LastError: r.LastError,
		}
		row.TenantId = tenant
		err := api.RDB.DB(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tenant_id"}, {Name: "device_profile_id"}, {Name: "profile_version"},
				{Name: "window_start"}, {Name: "replica"}},
			DoUpdates: clause.AssignmentColumns([]string{"decoded", "ignored", "failed", "suppressed",
				"charge_skew", "last_failure_at", "last_error"}),
		}).Create(&row).Error
		if err != nil {
			return written, skipped, err
		}
		written++
	}
	return written, skipped, nil
}

// TrimDecoderHealth deletes every tenant's health rows whose window started before the cutoff.
//
// 🔴 THE CROSS-TENANT READ IS THE LIST OF TENANTS WITH EXPIRED ROWS, AND NOTHING ELSE (the pattern
// of the ownership drain): each delete runs under its own tenant, so the tenant predicate is on
// every statement that touches a row. Finding tenants this way, and not from this process's recent
// traffic, is what expires the rows of a tenant that stopped decoding or of a process that
// restarted. A failed tenant does not stop the others; the first error is returned.
func (api *Api) TrimDecoderHealth(ctx context.Context, before time.Time) error {
	var tenants []string
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&ProfileDecoderHealth{}).
		Where("window_start < ?", before.UTC()).Distinct().Order("tenant_id").
		Pluck("tenant_id", &tenants).Error; err != nil {
		return err
	}
	var first error
	for _, tenant := range tenants {
		err := api.RDB.DB(core.WithTenant(ctx, tenant)).
			Where("tenant_id = ? AND window_start < ?", tenant, before.UTC()).
			Delete(&ProfileDecoderHealth{}).Error
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// DecoderSampleWrite is one failing uplink to keep.
type DecoderSampleWrite struct {
	ProfileToken string
	Version      int32
	DeviceToken  string
	Reason       string
	Error        string
	// Input is the canonical JSON of the uplink in the shape a decoder vector uses.
	Input []byte
}

// StoreDecoderSample keeps one failing uplink of the tenant in ctx and trims the profile to its
// newest MaxDecoderSamplesPerProfile. It returns false, with no error, when the profile is gone.
func (api *Api) StoreDecoderSample(ctx context.Context, s DecoderSampleWrite, now time.Time) (bool, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return false, errors.New("no tenant in the request context")
	}
	if len(s.Input) > MaxDecoderSampleBytes {
		return false, ErrDecoderSampleTooLarge
	}
	p, err := api.deviceProfileByToken(ctx, s.ProfileToken)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	row := ProfileDecoderSample{
		DeviceProfileId: p.ID, ProfileVersion: s.Version, CreatedAt: now.UTC(),
		DeviceToken: s.DeviceToken, Reason: s.Reason, Error: s.Error, Input: s.Input,
	}
	row.TenantId = tenant
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize the writers of one profile's samples. Without it two replicas at the cap can each
		// insert, each miss the other's uncommitted row when trimming, and each delete the same oldest
		// one, leaving one over the cap. The lock is held only for this insert and trim.
		var locked DeviceProfile
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
			Where("id = ? AND tenant_id = ?", p.ID, tenant).First(&locked).Error; err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		keep := tx.Model(&ProfileDecoderSample{}).Select("id").
			Where("tenant_id = ? AND device_profile_id = ?", tenant, p.ID).
			Order("created_at DESC, id DESC").Limit(MaxDecoderSamplesPerProfile)
		return tx.Where("tenant_id = ? AND device_profile_id = ? AND id NOT IN (?)", tenant, p.ID, keep).
			Delete(&ProfileDecoderSample{}).Error
	})
	return err == nil, err
}

// DecoderHealth is a profile's decode tally over a span, summed across replicas and windows.
type DecoderHealth struct {
	Decoded, Ignored, Failed, Suppressed, ChargeSkew int64
	LastFailureAt                                    *time.Time
	LastError                                        string
	// Windows is the number of replica-minutes the sums cover.
	Windows int
	Since   time.Time
}

// PayloadDecoderHealth is the tally of the profile's decoder over the last `minutes` (default 60,
// at most a week). It needs DeviceWrite: the last error is the tenant's own decoder text, but the
// reading is part of the authoring surface a caller limited to part of the tenant has no business
// with.
func (api *Api) PayloadDecoderHealth(ctx context.Context, profileToken string, minutes int) (*DecoderHealth, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("no tenant in the request context")
	}
	if minutes <= 0 {
		minutes = 60
	}
	if minutes > maxDecoderHealthWindowMinutes {
		minutes = maxDecoderHealthWindowMinutes
	}
	p, err := api.deviceProfileByToken(ctx, profileToken)
	if err != nil {
		return nil, err
	}
	since := time.Now().UTC().Add(-time.Duration(minutes) * time.Minute).Truncate(time.Minute)
	// 🔴 SUMMED IN THE DATABASE. A week at the maximum span is one row per replica per minute (about
	// 10,000 per replica), and reading every one of them into the service to add five numbers made
	// the read grow with the span and the replica count. Two statements, both on the
	// (tenant_id, device_profile_id, window_start) index: one aggregate, and the one row that
	// carries the last error.
	window := func() *gorm.DB {
		return api.RDB.DB(ctx).Model(&ProfileDecoderHealth{}).
			Where("tenant_id = ? AND device_profile_id = ? AND window_start >= ?", tenant, p.ID, since)
	}
	var sums struct {
		Windows                                          int
		Decoded, Ignored, Failed, Suppressed, ChargeSkew int64
	}
	if err := window().Select("COUNT(*) AS windows, " +
		"COALESCE(SUM(decoded), 0) AS decoded, COALESCE(SUM(ignored), 0) AS ignored, " +
		"COALESCE(SUM(failed), 0) AS failed, COALESCE(SUM(suppressed), 0) AS suppressed, " +
		"COALESCE(SUM(charge_skew), 0) AS charge_skew").Scan(&sums).Error; err != nil {
		return nil, err
	}
	out := &DecoderHealth{Since: since, Windows: sums.Windows, Decoded: sums.Decoded, Ignored: sums.Ignored,
		Failed: sums.Failed, Suppressed: sums.Suppressed, ChargeSkew: sums.ChargeSkew}
	// The last error is the one recorded with the latest failure. Among rows whose failures are
	// equally late, the earliest row (window_start, then id) keeps it, which is what the tally did
	// when it walked the rows in that order and replaced only on a strictly later failure.
	var last []ProfileDecoderHealth
	if err := window().Select("last_failure_at", "last_error").Where("last_failure_at IS NOT NULL").
		Order("last_failure_at DESC, window_start ASC, id ASC").Limit(1).Find(&last).Error; err != nil {
		return nil, err
	}
	if len(last) == 1 && last[0].LastFailureAt != nil {
		t := *last[0].LastFailureAt
		out.LastFailureAt, out.LastError = &t, last[0].LastError
	}
	return out, nil
}

// DecoderSampleSummary is a stored failure without its bytes.
type DecoderSampleSummary struct {
	ID             uint
	ProfileVersion int32
	CreatedAt      time.Time
	DeviceToken    string
	Reason         string
	Error          string
}

// PayloadDecoderSamples lists the profile's stored failing uplinks, newest first, WITHOUT their
// input. The bytes are reachable only by naming a sample to the test panel (testPayloadDecoder
// sampleId), under the same DeviceWrite bar; the listing is what a caller picks from.
func (api *Api) PayloadDecoderSamples(ctx context.Context, profileToken string, limit int) ([]DecoderSampleSummary, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("no tenant in the request context")
	}
	if limit <= 0 || limit > MaxDecoderSampleListing {
		limit = MaxDecoderSamplesPerProfile
	}
	p, err := api.deviceProfileByToken(ctx, profileToken)
	if err != nil {
		return nil, err
	}
	var rows []ProfileDecoderSample
	err = api.RDB.DB(ctx).
		Select("id", "profile_version", "created_at", "device_token", "reason", "error").
		Where("tenant_id = ? AND device_profile_id = ?", tenant, p.ID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]DecoderSampleSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, DecoderSampleSummary{ID: r.ID, ProfileVersion: r.ProfileVersion, CreatedAt: r.CreatedAt,
			DeviceToken: r.DeviceToken, Reason: r.Reason, Error: r.Error})
	}
	return out, nil
}
