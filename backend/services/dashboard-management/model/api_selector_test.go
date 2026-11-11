// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceSelectorCreateRejectsInvalidMeasurements(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
		Token:      "bad",
		Definition: `{"schemaVersion":1,"widgets":[{"id":"w","datasource":{"kind":"device","deviceToken":"d","measurements":42}}]}`,
	})
	require.ErrorIs(t, err, ErrInvalidDefinition)
	assert.Contains(t, err.Error(), `widget "w" datasource: measurements`)

	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Dashboard{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestDeviceSelectorUpdateRejectsWithoutChangingDraft(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
		Token:      "draft",
		Definition: `{"schemaVersion":1,"widgets":[]}`,
	})
	require.NoError(t, err)
	before, err := api.DashboardsByToken(ctx, []string{"draft"})
	require.NoError(t, err)
	require.Len(t, before, 1)

	_, err = api.UpdateDashboard(ctx, "draft", &DashboardUpdateRequest{
		Definition: util.OptionalStringOf(`{"schemaVersion":1,"widgets":[{"id":"w","datasource":{"kind":"device","deviceToken":"d","measurements":42}}]}`),
	}, nil)
	require.ErrorIs(t, err, ErrInvalidDefinition)

	after, err := api.DashboardsByToken(ctx, []string{"draft"})
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, before[0].Definition, after[0].Definition)
	assert.Equal(t, before[0].UpdatedAt, after[0].UpdatedAt)
}

func TestDeviceSelectorRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name       string
		datasource string
		field      string
	}{
		{"null measurements", `{"kind":"device","deviceToken":"d","measurements":null}`, "measurements"},
		{"missing measurements", `{"kind":"device","deviceToken":"d"}`, "measurements"},
		{"non-string measurement", `{"kind":"device","deviceToken":"d","measurements":["a",1]}`, "measurements"},
		{"null measurement element", `{"kind":"device","deviceToken":"d","measurements":[null]}`, "measurements"},
		{"numeric device token", `{"kind":"device","deviceToken":5,"measurements":[]}`, "deviceToken"},
		{"null device token", `{"kind":"device","deviceToken":null,"measurements":[]}`, "deviceToken"},
		{"missing device token", `{"kind":"device","measurements":[]}`, "deviceToken"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
				Token:      "bad",
				Definition: `{"schemaVersion":1,"widgets":[{"id":"w","datasource":` + tt.datasource + `}]}`,
			})
			require.ErrorIs(t, err, ErrInvalidDefinition)
			assert.Contains(t, err.Error(), `widget "w" datasource: `+tt.field)
		})
	}
}

func TestDeviceSelectorAcceptsAndPreservesDefinitions(t *testing.T) {
	tests := []struct {
		name       string
		definition string
	}{
		{"string measurements", `{"schemaVersion":1,"widgets":[{"id":"w","datasource":{"kind":"device","deviceToken":"d","measurements":["temperature"]}}]}`},
		{"unfinished selector", `{"schemaVersion":1,"widgets":[{"id":"w","datasource":{"kind":"device","deviceToken":"","measurements":[]}}]}`},
		{"location field", `{"schemaVersion":1,"widgets":[{"id":"w","datasource":{"kind":"device","deviceToken":"d","measurements":["temperature"],"location":{"x":1}}}]}`},
		{"future kind", `{"schemaVersion":1,"widgets":[{"id":"w","datasource":{"kind":"someFuture","x":1}}]}`},
		{"slot kind", `{"schemaVersion":1,"widgets":[{"id":"w","datasource":{"kind":"slot"}}]}`},
		{"anchor kind", `{"widgets":[{"datasource":{"kind":"anchor"}}]}`},
		{"devices kind", `{"widgets":[{"datasource":{"kind":"devices"}}]}`},
		{"related traversal kind", `{"widgets":[{"datasource":{"kind":"relatedTraversal"}}]}`},
		{"absent datasource", `{"schemaVersion":1,"widgets":[{"id":"w"}]}`},
		{"unrelated fields", ` {"schemaVersion":1, "extra":{"keep":true},"widgets":[{"id":"w","extra":[1],"datasource":{"kind":"device","deviceToken":"d","measurements":[],"extra":42}}]} `},
		{"absent widgets", `{"schemaVersion":1}`},
		{"null widgets", `{"widgets":null}`},
		{"non-array widgets", `{"widgets":{"datasource":{"kind":"device"}}}`},
		{"non-object widgets", `{"widgets":[null,42,"device",[]]}`},
		{"non-object datasource", `{"widgets":[{"datasource":null},{"datasource":42},{"datasource":[]},{"datasource":"device"}]}`},
		{"non-string kind", `{"widgets":[{"datasource":{"kind":null}},{"datasource":{"kind":42}},{"datasource":{}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			created, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
				Token:      "draft",
				Definition: tt.definition,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.definition, string(created.Definition))
			found, err := api.DashboardsByToken(ctx, []string{"draft"})
			require.NoError(t, err)
			require.Len(t, found, 1)
			assert.Equal(t, tt.definition, string(found[0].Definition))

			_, err = api.UpdateDashboard(ctx, "draft", &DashboardUpdateRequest{
				Definition: util.OptionalStringOf(tt.definition),
			}, nil)
			require.NoError(t, err)
			found, err = api.DashboardsByToken(ctx, []string{"draft"})
			require.NoError(t, err)
			require.Len(t, found, 1)
			assert.Equal(t, tt.definition, string(found[0].Definition))
		})
	}
}

// TestDeviceSelectorCaseVariantWidgetsKeyDoesNotHideIt pins that only the exact "widgets"
// key is read, as the runtime does: a trailing "Widgets" must not replace the list checked.
func TestDeviceSelectorCaseVariantWidgetsKeyDoesNotHideIt(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
		Token:      "case",
		Definition: `{"widgets":[{"datasource":{"kind":"device","deviceToken":5,"measurements":[]}}],"Widgets":[]}`,
	})
	assert.ErrorIs(t, err, ErrInvalidDefinition)
}
