// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The values are pinned here, not just referenced: a ceiling edited by accident is a
// change to what every caller is promised in the GraphQL reference.
func TestCeilingValuesAreTheDocumentedOnes(t *testing.T) {
	require.Equal(t, 256, MaxTrackedRelationshipsPerDevice)
	require.Equal(t, 1000, MaxChildrenPerProfile)
}

func requireLimitRefusal(t *testing.T, err error, max int) {
	t.Helper()
	require.Error(t, err)
	le, ok := limit.As(err)
	require.True(t, ok, "want a limit.Error, got %T: %v", err, err)
	require.Equal(t, max, le.Max)
	require.Equal(t, max+1, le.Got)
}

func tokRef(token string) rdb.TokenReference { return rdb.TokenReference{Token: token} }

// trackedRig seeds a device "src" and n target devices, plus a tracked and an untracked
// relationship type.
func trackedRig(t *testing.T, targets int) (*Api, context.Context) {
	t.Helper()
	api := newPartialUpdateApi(t, &DeviceType{}, &Device{}, &DeviceCredential{},
		&EntityRelationshipType{}, &EntityRelationship{}, &EntityAttribute{}, &Alarm{},
		&EntityGroup{}, &EntityGroupMembership{})
	ctx := partialUpdateCtx()
	dt := &DeviceType{}
	dt.Token = "src-type"
	require.NoError(t, api.RDB.DB(ctx).Create(dt).Error)
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "src", DeviceTypeToken: "src-type"})
	require.NoError(t, err)
	for i := 0; i < targets; i++ {
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{
			Token: fmt.Sprintf("tgt-%d", i), DeviceTypeToken: "src-type"})
		require.NoError(t, err)
	}
	for tok, tracked := range map[string]bool{"tracked": true, "plain": false} {
		_, err := api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{
			Token: tok, Tracked: tracked})
		require.NoError(t, err)
	}
	return api, ctx
}

func relRequest(rel string, i int) *EntityRelationshipCreateRequest {
	return &EntityRelationshipCreateRequest{
		Token: fmt.Sprintf("%s-%d", rel, i), RelationshipType: rel,
		SourceType: "device", Source: "src", TargetType: "device", Target: fmt.Sprintf("tgt-%d", i),
	}
}

func deviceIDOf(t *testing.T, api *Api, ctx context.Context, token string) uint {
	t.Helper()
	ds, err := api.DevicesByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	return ds[0].ID
}

func TestTrackedRelationshipCeilingSingleCreate(t *testing.T) {
	api, ctx := trackedRig(t, MaxTrackedRelationshipsPerDevice+1)
	for i := 0; i < MaxTrackedRelationshipsPerDevice; i++ {
		_, err := api.CreateEntityRelationship(ctx, relRequest("tracked", i))
		require.NoError(t, err, "edge %d is within the ceiling", i)
	}
	_, err := api.CreateEntityRelationship(ctx, relRequest("tracked", MaxTrackedRelationshipsPerDevice))
	requireLimitRefusal(t, err, MaxTrackedRelationshipsPerDevice)

	// An untracked edge from the same full device is not counted and not refused.
	_, err = api.CreateEntityRelationship(ctx, relRequest("plain", 0))
	require.NoError(t, err)

	// Reads stay complete, and a delete makes room again.
	got, err := api.TrackedRelationshipsForDevice(ctx, deviceIDOf(t, api, ctx, "src"))
	require.NoError(t, err)
	require.Len(t, got.Results, MaxTrackedRelationshipsPerDevice)
	_, err = api.RemoveEntityRelationship(ctx, "tracked-0")
	require.NoError(t, err)
	_, err = api.CreateEntityRelationship(ctx, relRequest("tracked", MaxTrackedRelationshipsPerDevice))
	require.NoError(t, err)
}

func TestTrackedRelationshipCeilingBulkCreateCountsExistingEdges(t *testing.T) {
	api, ctx := trackedRig(t, MaxTrackedRelationshipsPerDevice+1)
	reqs := make([]*EntityRelationshipCreateRequest, 0, MaxTrackedRelationshipsPerDevice)
	for i := 0; i < MaxTrackedRelationshipsPerDevice; i++ {
		reqs = append(reqs, relRequest("tracked", i))
	}
	created, err := api.CreateEntityRelationships(ctx, reqs)
	require.NoError(t, err)
	require.Len(t, created, MaxTrackedRelationshipsPerDevice)

	_, err = api.CreateEntityRelationships(ctx,
		[]*EntityRelationshipCreateRequest{relRequest("tracked", MaxTrackedRelationshipsPerDevice)})
	requireLimitRefusal(t, err, MaxTrackedRelationshipsPerDevice)
}

func TestTrackedRelationshipCeilingBulkRefusesAnOverlongBatchWhole(t *testing.T) {
	api, ctx := trackedRig(t, MaxTrackedRelationshipsPerDevice+1)
	reqs := make([]*EntityRelationshipCreateRequest, 0, MaxTrackedRelationshipsPerDevice+1)
	for i := 0; i <= MaxTrackedRelationshipsPerDevice; i++ {
		reqs = append(reqs, relRequest("tracked", i))
	}
	_, err := api.CreateEntityRelationships(ctx, reqs)
	requireLimitRefusal(t, err, MaxTrackedRelationshipsPerDevice)
	got, err := api.TrackedRelationshipsForDevice(ctx, deviceIDOf(t, api, ctx, "src"))
	require.NoError(t, err)
	require.Empty(t, got.Results, "the batch is all-or-nothing")
}

// seedChildren inserts rows directly, so a boundary test does not pay one API call each.
func seedChildren(t *testing.T, api *Api, ctx context.Context, rows []any) {
	t.Helper()
	for _, r := range rows {
		require.NoError(t, api.RDB.DB(ctx).Create(r).Error)
	}
}

func profileRig(t *testing.T) (*Api, context.Context, *DeviceProfile) {
	t.Helper()
	api := newPartialUpdateApi(t, deviceProfileTables...)
	ctx := partialUpdateCtx()
	p, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "p"})
	require.NoError(t, err)
	return api, ctx, p
}

func seedMetrics(t *testing.T, api *Api, ctx context.Context, p *DeviceProfile, n int) {
	rows := make([]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, &MetricDefinition{TokenReference: tokRef(fmt.Sprintf("m-%d", i)),
			DeviceProfile: p, MetricKey: fmt.Sprintf("k%d", i), DataType: "DOUBLE"})
	}
	seedChildren(t, api, ctx, rows)
}

func TestProfileChildCeilingMetrics(t *testing.T) {
	api, ctx, p := profileRig(t)
	seedMetrics(t, api, ctx, p, MaxChildrenPerProfile-1)
	_, err := api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{
		Token: "m-last", DeviceProfileToken: "p", MetricKey: "last", DataType: "DOUBLE"})
	require.NoError(t, err, "the last permitted definition is accepted")
	_, err = api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{
		Token: "m-over", DeviceProfileToken: "p", MetricKey: "over", DataType: "DOUBLE"})
	requireLimitRefusal(t, err, MaxChildrenPerProfile)

	got, err := api.MetricDefinitionsByDeviceProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, got, MaxChildrenPerProfile, "reads stay complete")
}

func TestProfileChildCeilingCommands(t *testing.T) {
	api, ctx, p := profileRig(t)
	rows := make([]any, 0, MaxChildrenPerProfile-1)
	for i := 0; i < MaxChildrenPerProfile-1; i++ {
		rows = append(rows, &CommandDefinition{TokenReference: tokRef(fmt.Sprintf("c-%d", i)),
			DeviceProfile: p, CommandKey: fmt.Sprintf("cmd%d", i)})
	}
	seedChildren(t, api, ctx, rows)
	_, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
		Token: "c-last", DeviceProfileToken: "p", CommandKey: "last"})
	require.NoError(t, err)
	_, err = api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
		Token: "c-over", DeviceProfileToken: "p", CommandKey: "over"})
	requireLimitRefusal(t, err, MaxChildrenPerProfile)
}

func TestProfileChildCeilingRules(t *testing.T) {
	api, ctx, p := profileRig(t)
	def := `{"type":"threshold","metric":"temp","op":">","value":30}`
	rows := make([]any, 0, MaxChildrenPerProfile-1)
	for i := 0; i < MaxChildrenPerProfile-1; i++ {
		rows = append(rows, &DetectionRule{TokenReference: tokRef(fmt.Sprintf("r-%d", i)),
			DeviceProfile: p, Definition: []byte(def), Enabled: true})
	}
	seedChildren(t, api, ctx, rows)
	create := func(tok string) error {
		_, err := api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{
			Token: tok, DeviceProfileToken: "p", Definition: def, Enabled: true})
		return err
	}
	require.NoError(t, create("r-last"))
	requireLimitRefusal(t, create("r-over"), MaxChildrenPerProfile)
}

// Each kind is counted on its own: a profile full of metrics still takes a command.
func TestProfileChildCeilingCountsKindsSeparately(t *testing.T) {
	api, ctx, p := profileRig(t)
	seedMetrics(t, api, ctx, p, MaxChildrenPerProfile)
	_, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
		Token: "c-1", DeviceProfileToken: "p", CommandKey: "reboot"})
	require.NoError(t, err)
}

func histogramOf(t *testing.T, reg *prometheus.Registry, name string) (count uint64, sum float64) {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == name {
			for _, m := range f.GetMetric() {
				count += m.GetHistogram().GetSampleCount()
				sum += m.GetHistogram().GetSampleSum()
			}
		}
	}
	return count, sum
}

func TestTrackedRelationshipMetricIsEmitted(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(reg)

	api, ctx := trackedRig(t, 3)
	api.EnableCeilingMetrics(ms)
	capi := NewCachedApi(api, &Caches{RelationshipsBySource: msgtest.NewMemoryKV().NewCache()})
	for i := 0; i < 3; i++ {
		_, err := api.CreateEntityRelationship(ctx, relRequest("tracked", i))
		require.NoError(t, err)
	}
	id := deviceIDOf(t, api, ctx, "src")
	for i := 0; i < 2; i++ { // one database load and one cache hit: both observed
		got, err := capi.TrackedRelationshipsForDevice(ctx, id)
		require.NoError(t, err)
		require.Len(t, got.Results, 3)
	}

	count, sum := histogramOf(t, reg, "devicechain_devicemanagement_tracked_relationships_per_device")
	require.Equal(t, uint64(2), count)
	require.Equal(t, 6.0, sum)
}

func TestProfileChildrenMetricIsEmitted(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(reg)
	api, ctx, p := profileRig(t)
	api.EnableCeilingMetrics(ms)
	_, err := api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{
		Token: "m-1", DeviceProfileToken: "p", MetricKey: "a", DataType: "DOUBLE"})
	require.NoError(t, err)
	_, err = api.MetricDefinitionsByDeviceProfile(ctx, p.ID)
	require.NoError(t, err)

	count, sum := histogramOf(t, reg, "devicechain_devicemanagement_profile_children")
	require.Equal(t, uint64(2), count, "one observation on the write check, one on the read")
	require.Equal(t, 1.0, sum, "0 existing at the write check, 1 found by the read")
}
