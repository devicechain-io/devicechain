// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func metricDefinitionPartialFixture(t *testing.T) (*Api, context.Context, *MetricDefinition, *rdbtest.StatementCounter) {
	t.Helper()
	family := metricDefinitionFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	rows, err := api.MetricDefinitionsByToken(ctx, []string{family.Token})
	original := requireOne(t, "MetricDefinition", rows, err)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, original, statements
}

func TestUpdateMetricDefinitionStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, original, _ := metricDefinitionPartialFixture(t)
	fired := false
	const callback = "test:metric_definition_other_writer"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "metric_definitions" {
			return
		}
		// The first entity read has completed (including preloads). Leave its
		// snapshot stale and change a field the request will omit.
		fired = true
		tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).
			Model(&MetricDefinition{}).Where("id = ?", original.ID).
			Update("description", "other writer").Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateMetricDefinition(ctx, original.Token, &MetricDefinitionUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.MetricDefinitionsByToken(ctx, []string{original.Token})
	stored := requireOne(t, "MetricDefinition", rows, err)
	for _, row := range []*MetricDefinition{updated, stored} {
		require.Equal(t, "Renamed", nullStr(row.Name))
		require.Equal(t, "other writer", nullStr(row.Description), "the return value must be reloaded too")
	}
	require.True(t, updated.UpdatedAt.After(original.UpdatedAt))
}

func TestUpdateMetricDefinitionEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := metricDefinitionPartialFixture(t)
	updated, err := api.UpdateMetricDefinition(ctx, original.Token, &MetricDefinitionUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.MetricDefinitionsByToken(ctx, []string{original.Token})
	stored := requireOne(t, "MetricDefinition", rows, err)
	require.Equal(t, original, stored, "the entire row, including updated_at, must be unchanged")
	require.Equal(t, original, updated)
}

func TestUpdateMetricDefinitionRefusedRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := metricDefinitionPartialFixture(t)
	_, err := api.UpdateMetricDefinition(ctx, original.Token, &MetricDefinitionUpdateRequest{
		Name:               dcgraphql.OptionalStringOf("Refused"),
		DeviceProfileToken: dcgraphql.OptionalStringOf("missing-profile"),
	})
	require.Error(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.MetricDefinitionsByToken(ctx, []string{original.Token})
	stored := requireOne(t, "MetricDefinition", rows, err)
	require.Equal(t, original, stored, "refusal must leave the entire row unchanged")
}

func TestUpdateMetricDefinitionReparentWritesOnlyForeignKey(t *testing.T) {
	api, ctx, original, _ := metricDefinitionPartialFixture(t)
	writes := captureAreaBoundaryUpdates(t, api)
	updated, err := api.UpdateMetricDefinition(ctx, original.Token, &MetricDefinitionUpdateRequest{
		DeviceProfileToken: dcgraphql.OptionalStringOf(otherProfileToken),
	})
	require.NoError(t, err)
	require.Len(t, *writes, 1, "associations must not be updated")
	require.Contains(t, (*writes)[0], "`device_profile_id`=")
	require.Contains(t, (*writes)[0], "`updated_at`=")
	for _, column := range []string{"name", "description", "metadata", "metric_key", "data_type", "semantic_property", "semantic_point_class", "enum", "unit", "min_value", "max_value", "descriptor", "expression"} {
		require.NotContains(t, (*writes)[0], "`"+column+"`=")
	}
	rows, err := api.MetricDefinitionsByToken(ctx, []string{original.Token})
	stored := requireOne(t, "metric definition", rows, err)
	for _, row := range []*MetricDefinition{updated, stored} {
		require.NotNil(t, row.DeviceProfile, "the profile must be preloaded")
		require.Equal(t, otherProfileToken, row.DeviceProfile.Token)
		require.Equal(t, row.DeviceProfile.ID, row.DeviceProfileId)
		require.NotEqual(t, original.DeviceProfileId, row.DeviceProfileId)
		require.Equal(t, original.Name, row.Name)
		require.Equal(t, original.Metadata, row.Metadata)
	}
}
