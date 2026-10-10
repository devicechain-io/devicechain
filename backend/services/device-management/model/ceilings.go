// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// Count ceilings on two collections that were read whole because nothing bounded them.
//
// Both were measured before they were capped: ceilingMetrics exports the observed size
// of each, so an operator can see how close real data comes. The numbers are generous
// on purpose. They are a backstop against a runaway writer, not a tuning knob.
//
// 🔴 A ceiling refuses WRITES only. A device or profile already over it keeps every row
// and every read stays complete, because truncating a read would silently drop event
// anchors or profile vocabulary. Such an owner can still be reduced by deleting.
const (
	// MaxTrackedRelationshipsPerDevice caps the tracked relationships whose source is
	// one device. Event resolution reads the whole set for every event it resolves and
	// denormalizes every target onto the event, so this is the width of that read.
	MaxTrackedRelationshipsPerDevice = 256

	// MaxChildrenPerProfile caps each kind of definition a device profile declares:
	// metric definitions, command definitions and detection rules, EACH counted on its
	// own. The three are read whole to build a profile's publish snapshot and for the
	// profile's resolver fields.
	MaxChildrenPerProfile = 1000

	// MaxRelationshipBatch caps the edges one createEntityRelationships call may carry.
	MaxRelationshipBatch = 1000
)

// Labels of the profile-children histogram. A closed set, so the series count is fixed.
const (
	childKindMetric  = "metric"
	childKindCommand = "command"
	childKindRule    = "rule"
)

// ceilingBuckets span 1 to 2048 so both ceilings sit inside the range with room above.
var ceilingBuckets = []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1000, 2048}

// ceilingMetrics exports the observed size of the two collections. A nil receiver is a
// no-op, so a test that wires no microservice needs no metrics.
type ceilingMetrics struct {
	tracked  prometheus.Observer
	children *prometheus.HistogramVec
}

func newCeilingMetrics(ms *core.Microservice) *ceilingMetrics {
	return &ceilingMetrics{
		tracked: ms.NewHistogramVec("tracked_relationships_per_device",
			"Tracked relationships found on a device each time its tracked set is loaded from the database "+
				"(a cache hit is not counted). Writes are refused above 256.",
			[]string{}, ceilingBuckets).WithLabelValues(),
		children: ms.NewHistogramVec("profile_children",
			"Definitions found on a device profile each time one kind is read whole: kind=metric, command "+
				"or rule. Writes are refused above 1000 per kind.",
			[]string{"kind"}, ceilingBuckets),
	}
}

// EnableCeilingMetrics exports the observed collection sizes through ms. Call it once.
func (api *Api) EnableCeilingMetrics(ms *core.Microservice) {
	api.ceilings = newCeilingMetrics(ms)
}

func (m *ceilingMetrics) observeTracked(n int) {
	if m != nil {
		m.tracked.Observe(float64(n))
	}
}

func (m *ceilingMetrics) observeChildren(kind string, n int) {
	if m != nil {
		m.children.WithLabelValues(kind).Observe(float64(n))
	}
}

// checkTrackedCeiling refuses adding more tracked relationships to a device when the
// result would exceed MaxTrackedRelationshipsPerDevice. A no-op unless the edge is
// tracked and sourced at a device. db is the connection to count on (the transaction,
// on the bulk path).
func (api *Api) checkTrackedCeiling(ctx context.Context, db *gorm.DB, rt *EntityRelationshipType,
	sourceType string, sourceId uint, adding int) error {
	if rt == nil || !rt.Tracked || sourceType != string(entity.TypeDevice) || adding <= 0 {
		return nil
	}
	var existing int64
	if err := db.Model(&EntityRelationship{}).
		Where("source_type = ? AND source_id = ? AND relationship_type_id IN (?)",
			sourceType, sourceId,
			api.RDB.DB(ctx).Model(&EntityRelationshipType{}).Select("id").Where("tracked = ?", true)).
		Count(&existing).Error; err != nil {
		return err
	}
	if total := int(existing) + adding; total > MaxTrackedRelationshipsPerDevice {
		return limit.Exceeded("tracked relationships per device", total, MaxTrackedRelationshipsPerDevice)
	}
	return nil
}

// checkTrackedFlipCeiling refuses marking a relationship type tracked when that would put
// any device over MaxTrackedRelationshipsPerDevice: one grouped query over the device
// edges of every tracked type plus this one, on the tenant-scoped handle.
func (api *Api) checkTrackedFlipCeiling(ctx context.Context, typeId uint) error {
	var over []uint
	if err := api.RDB.DB(ctx).Model(&EntityRelationship{}).
		Where("source_type = ? AND (relationship_type_id = ? OR relationship_type_id IN (?))",
			string(entity.TypeDevice), typeId,
			api.RDB.DB(ctx).Model(&EntityRelationshipType{}).Select("id").Where("tracked = ?", true)).
		Group("source_id").Having("COUNT(*) > ?", MaxTrackedRelationshipsPerDevice).
		Limit(1).Pluck("source_id", &over).Error; err != nil {
		return err
	}
	if len(over) > 0 {
		return limit.Exceeded("tracked relationships per device", MaxTrackedRelationshipsPerDevice+1,
			MaxTrackedRelationshipsPerDevice)
	}
	return nil
}

// checkProfileChildCeiling refuses one more definition of the given kind on a profile
// that already holds MaxChildrenPerProfile of them.
func (api *Api) checkProfileChildCeiling(ctx context.Context, model any, kind string, profileId uint) error {
	var existing int64
	if err := api.RDB.DB(ctx).Model(model).Where("device_profile_id = ?", profileId).
		Count(&existing).Error; err != nil {
		return err
	}
	api.ceilings.observeChildren(kind, int(existing))
	if total := int(existing) + 1; total > MaxChildrenPerProfile {
		return limit.Exceeded("profile "+kind+" definitions", total, MaxChildrenPerProfile)
	}
	return nil
}
