// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"gorm.io/gorm"
)

// The two read-only doors a calculated-metric backfill worker reads (event-management owns the
// job; it holds no profile or device records). They describe a PUBLISHED profile version's
// calculated metrics and the devices that version applies to, and nothing else:
//
//   - the plan door answers from the SAME ProfileResolution the live resolver compiles its
//     formulas from (NewProfileResolution over the version's frozen snapshot, then
//     buildCalcPlan). There is no second compiler and no second projection: the order, the
//     measured inputs and the constants are read back out of the plan the resolver would run,
//     so a worker that compiles the answer with calcfield.NewPlan gets that plan.
//   - the roster door is a keyset page over device ids, like the doors it sits beside, listing
//     EVERY device of the profile (one holding no constant is still a device to fill).

const (
	// MaxBackfillRosterPageSize bounds one page of the backfill roster.
	MaxBackfillRosterPageSize = 500
	// MaxBackfillRosterDeviceTokens bounds the device filter of one roster request.
	MaxBackfillRosterDeviceTokens = 1000
)

// CalculatedPlanInput is a measured metric a formula of the version may read.
type CalculatedPlanInput struct {
	Key      string
	DataType string
}

// CalculatedPlanMetric is one calculated metric of a version, as the live plan carries it.
type CalculatedPlanMetric struct {
	Key        string
	Expression string
	DataType   string
	MinValue   *float64
	MaxValue   *float64
	Unit       *string
	// Classifier is the metric definition's id, the classifier stamped on every value of it.
	Classifier uint64
	// NewInVersion is true when the previous published version did not calculate this key.
	NewInVersion bool
	// ChangedSinceEarlier is true when an earlier published version calculated this key
	// under a different expression, data type or bounds.
	ChangedSinceEarlier bool
}

// CalculatedMetricPlan is one published profile version's calculated metrics.
type CalculatedMetricPlan struct {
	VersionToken string
	Version      int32
	// ActiveSince is set only when the version is the profile's active one.
	ActiveSince *time.Time
	Measured    []CalculatedPlanInput
	// Calculated is in the live plan's evaluation order (topological, by key otherwise), then
	// any metric whose data type no formula can produce, by key.
	Calculated   []CalculatedPlanMetric
	ConstantKeys []string
}

// planDescription reads a resolution's calculated metrics back out of the plan buildCalcPlan
// compiled for it: evaluation order, the measured inputs the formulas may read, and the
// constants they read together. It errors when the plan could not be built at all, because a
// resolution in that state computes nothing and a description of it would promise otherwise.
func (r ProfileResolution) planDescription() ([]CalculatedPlanInput, []CalculatedPlanMetric, []string, error) {
	if r.calc == nil {
		return []CalculatedPlanInput{}, []CalculatedPlanMetric{}, []string{}, nil
	}
	if r.calc.plan == nil {
		return nil, nil, nil, fmt.Errorf("the version's calculated metrics cannot be planned: %w", r.calc.planErr)
	}
	inputs := resolvedFormulaInputs(r.metrics)
	measured := make([]CalculatedPlanInput, 0, len(inputs))
	for _, in := range inputs {
		measured = append(measured, CalculatedPlanInput{Key: in.Key, DataType: in.Type.String()})
	}
	describe := func(meta calcMeta) CalculatedPlanMetric {
		out := CalculatedPlanMetric{Key: meta.key, DataType: meta.dataType, Classifier: meta.classifier}
		if meta.unit != nil {
			u := *meta.unit
			out.Unit = &u
		}
		// The definition the plan was built from: the FIRST calculated declaration of the key,
		// the rule buildCalcPlan applies.
		for i := range r.metrics {
			m := &r.metrics[i]
			if m.Calculated() && m.m.MetricKey == meta.key {
				out.Expression = *m.m.Expression
				out.MinValue, out.MaxValue = copyFloat(m.m.MinValue), copyFloat(m.m.MaxValue)
				break
			}
		}
		return out
	}
	calculated := make([]CalculatedPlanMetric, 0, len(r.calc.metas)+len(r.calc.untyped))
	for _, meta := range r.calc.metas {
		calculated = append(calculated, describe(meta))
	}
	for _, meta := range r.calc.untyped {
		calculated = append(calculated, describe(meta))
	}
	constKeys := append([]string{}, r.calc.constKeys...)
	return measured, calculated, constKeys, nil
}

func copyFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// calculatedDecl is what makes two declarations of one calculated key "the same".
type calculatedDecl struct {
	expression string
	dataType   string
	min, max   string
}

func declOf(m *MetricDefinition) calculatedDecl {
	d := calculatedDecl{expression: m.Expression.String, dataType: m.DataType}
	if m.MinValue.Valid {
		d.min = fmt.Sprintf("%v", m.MinValue.Float64)
	}
	if m.MaxValue.Valid {
		d.max = fmt.Sprintf("%v", m.MaxValue.Float64)
	}
	return d
}

// CalculatedMetricPlan describes the calculated metrics of one PUBLISHED version of a profile
// (version nil: the active one). An unknown profile, an unpublished profile and a version that
// was never published (a draft is not a version) are all gorm.ErrRecordNotFound.
func (api *Api) CalculatedMetricPlan(ctx context.Context, profileToken string, version *int32) (*CalculatedMetricPlan, error) {
	profile, err := api.deviceProfileByToken(ctx, profileToken)
	if err != nil {
		return nil, err
	}
	var v int32
	switch {
	case version != nil:
		v = *version
	case profile.ActiveVersion.Valid:
		v = profile.ActiveVersion.Int32
	default:
		return nil, fmt.Errorf("device profile %q has no published version: %w", profileToken, gorm.ErrRecordNotFound)
	}
	var rows []DeviceProfileVersion
	if err := api.RDB.DB(ctx).Where("device_profile_id = ? AND version <= ?", profile.ID, v).
		Order("version").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 || rows[len(rows)-1].Version != v {
		return nil, fmt.Errorf("device profile %q has no published version %d: %w", profileToken, v, gorm.ErrRecordNotFound)
	}
	target := rows[len(rows)-1]
	snap, err := parseProfileSnapshot(target.Snapshot)
	if err != nil {
		return nil, err
	}
	token := fmt.Sprintf("%s@%d", profile.Token, v)
	// The resolution the live resolver would build for this version, from the same frozen
	// snapshot: its plan is the one described.
	res := NewProfileResolution(ProfileScope{ProfileVersionToken: token}, snap.Metrics)
	measured, calculated, constKeys, err := res.planDescription()
	if err != nil {
		return nil, err
	}

	// Flags against the earlier published versions of the profile.
	var previous map[string]bool
	earlier := map[string][]calculatedDecl{}
	for i, row := range rows[:len(rows)-1] {
		es, err := parseProfileSnapshot(row.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("version %d of %q does not parse: %w", row.Version, profileToken, err)
		}
		calc := map[string]bool{}
		for _, m := range es.Metrics {
			if m.Calculated() {
				calc[m.MetricKey] = true
				earlier[m.MetricKey] = append(earlier[m.MetricKey], declOf(m))
			}
		}
		if i == len(rows)-2 {
			previous = calc
		}
	}
	targetDecl := map[string]calculatedDecl{}
	for _, m := range snap.Metrics {
		if m.Calculated() {
			if _, dup := targetDecl[m.MetricKey]; !dup {
				targetDecl[m.MetricKey] = declOf(m)
			}
		}
	}
	for i := range calculated {
		key := calculated[i].Key
		calculated[i].NewInVersion = !previous[key]
		for _, d := range earlier[key] {
			if d != targetDecl[key] {
				calculated[i].ChangedSinceEarlier = true
				break
			}
		}
	}

	out := &CalculatedMetricPlan{VersionToken: token, Version: v, Measured: measured, Calculated: calculated,
		ConstantKeys: constKeys}
	if profile.ActiveVersion.Valid && profile.ActiveVersion.Int32 == v {
		latest, found, err := latestProfileVersion(api.RDB.DB(ctx), profile.ID)
		if err != nil {
			return nil, err
		}
		if found {
			since := resolveActiveSince(profile.ActiveSince, profile.ActiveVersion, latest)
			out.ActiveSince = &since
		}
	}
	return out, nil
}

// BackfillRosterEntry is one device of the profile.
type BackfillRosterEntry struct {
	DeviceId    uint
	DeviceToken string
	CreatedAt   time.Time
	// Constants are the requested names the device's metadata holds; empty for a device that
	// holds none, which is still a device to fill.
	Constants []DeviceConstant
}

// BackfillRosterPage is one keyset page. Total is set on the first page only.
type BackfillRosterPage struct {
	Entries    []BackfillRosterEntry
	NextCursor *uint64
	Total      *int64
}

// CalculatedBackfillRoster returns one keyset page of every device whose type adopts the
// profile, in id order, with the constants asked for. deviceTokens narrows the walk; a token
// that is not a device of the profile is refused by name rather than skipped.
func (api *Api) CalculatedBackfillRoster(ctx context.Context, profileToken string, constantKeys []string,
	deviceTokens []string, afterId uint64, limit int) (*BackfillRosterPage, error) {
	if err := checkReconcilePageSize("backfill roster", limit, MaxBackfillRosterPageSize); err != nil {
		return nil, err
	}
	if len(constantKeys) > calcfield.MaxProfileConstants {
		return nil, &InvalidArgumentError{Msg: fmt.Sprintf("constantKeys may name at most %d constants, got %d",
			calcfield.MaxProfileConstants, len(constantKeys))}
	}
	for _, k := range constantKeys {
		if k == "" || len(k) > calcfield.MaxConstantKeyBytes {
			return nil, &InvalidArgumentError{Msg: fmt.Sprintf("a constant name must be 1 to %d bytes",
				calcfield.MaxConstantKeyBytes)}
		}
	}
	if len(deviceTokens) > MaxBackfillRosterDeviceTokens {
		return nil, &InvalidArgumentError{Msg: fmt.Sprintf("deviceTokens may name at most %d devices, got %d",
			MaxBackfillRosterDeviceTokens, len(deviceTokens))}
	}
	profile, err := api.deviceProfileByToken(ctx, profileToken)
	if err != nil {
		return nil, err
	}
	base := func() *gorm.DB {
		q := api.RDB.DB(ctx).Model(&Device{}).
			// The tenant equality makes the joined table tenant-safe by the query itself
			// (hack/check-raw-joins.sh).
			Joins("JOIN device_types ON device_types.id = devices.device_type_id AND device_types.tenant_id = devices.tenant_id "+
				"AND device_types.deleted_at IS NULL").
			Where("device_types.profile_id = ?", profile.ID)
		if len(deviceTokens) > 0 {
			q = q.Where("devices.token IN ?", deviceTokens)
		}
		return q
	}
	if len(deviceTokens) > 0 {
		var found []string
		if err := base().Distinct().Pluck("devices.token", &found).Error; err != nil {
			return nil, err
		}
		have := make(map[string]bool, len(found))
		for _, t := range found {
			have[t] = true
		}
		var missing []string
		for _, t := range deviceTokens {
			if !have[t] {
				missing = append(missing, t)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			if len(missing) > 20 {
				missing = missing[:20]
			}
			return nil, &InvalidArgumentError{Msg: fmt.Sprintf("not devices of profile %q: %s", profileToken,
				strings.Join(missing, ", "))}
		}
	}
	type row struct {
		Id        uint
		Token     string
		CreatedAt time.Time
		Metadata  []byte
	}
	rows := make([]row, 0, limit)
	q := base().Select("devices.id AS id, devices.token AS token, devices.created_at AS created_at, devices.metadata AS metadata")
	if afterId > 0 {
		q = q.Where("devices.id > ?", afterId)
	}
	if err := q.Order("devices.id").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	page := &BackfillRosterPage{Entries: make([]BackfillRosterEntry, 0, len(rows))}
	for _, r := range rows {
		entry := BackfillRosterEntry{DeviceId: r.Id, DeviceToken: r.Token, CreatedAt: r.CreatedAt.UTC(),
			Constants: []DeviceConstant{}}
		if len(constantKeys) > 0 && len(r.Metadata) > 0 {
			for _, c := range readConstants(r.Metadata, constantKeys) {
				dc := DeviceConstant{Key: c.Key()}
				if v, ok := c.Number(); ok {
					dc.Number = &v
				}
				entry.Constants = append(entry.Constants, dc)
			}
		}
		page.Entries = append(page.Entries, entry)
	}
	if len(rows) > 0 {
		page.NextCursor = fullPageCursor(len(rows), limit, rows[len(rows)-1].Id)
	}
	if afterId == 0 {
		var total int64
		if err := base().Count(&total).Error; err != nil {
			return nil, err
		}
		page.Total = &total
	}
	return page, nil
}
