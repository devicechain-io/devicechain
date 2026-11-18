// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEventsCountFreeGraphQL(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Event{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})
	for i := 0; i < 3; i++ {
		require.NoError(t, db.WithContext(core.WithTenant(context.Background(), "A")).Create(&model.Event{EventId: []byte{byte(i + 1)}, DeviceToken: "device", OccurredTime: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC), Source: "test"}).Error)
	}
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	ctx := scopedCtx(api, "A", nil, string(auth.EventRead))
	doc := `query($criteria: EventSearchCriteria!) { events(criteria: $criteria) { pagination { totalRecords hasMore pageStart pageEnd } results { deviceToken } } }`
	criteria := map[string]interface{}{"pageNumber": int32(1), "pageSize": int32(2), "includeTotal": false}
	resp := schema.Exec(ctx, doc, "", map[string]interface{}{"criteria": criteria})
	require.Empty(t, resp.Errors)
	var data struct {
		Events struct {
			Pagination struct {
				TotalRecords       *int32
				HasMore            *bool
				PageStart, PageEnd int32
			}
			Results []struct{ DeviceToken string }
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.Nil(t, data.Events.Pagination.TotalRecords)
	require.NotNil(t, data.Events.Pagination.HasMore)
	require.True(t, *data.Events.Pagination.HasMore)
	require.EqualValues(t, 1, data.Events.Pagination.PageStart)
	require.EqualValues(t, 2, data.Events.Pagination.PageEnd)
	require.Len(t, data.Events.Results, 2)
	delete(criteria, "includeTotal")
	resp = schema.Exec(ctx, doc, "", map[string]interface{}{"criteria": criteria})
	require.Empty(t, resp.Errors)
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.NotNil(t, data.Events.Pagination.TotalRecords)
	require.EqualValues(t, 3, *data.Events.Pagination.TotalRecords)
	require.NotNil(t, data.Events.Pagination.HasMore)
	require.True(t, *data.Events.Pagination.HasMore)
	criteria["includeTotals"] = false
	resp = schema.Exec(ctx, doc, "", map[string]interface{}{"criteria": criteria})
	require.NotEmpty(t, resp.Errors)
	require.Contains(t, resp.Errors[0].Message, "includeTotals")
}

func TestNonEventPaginationHasMore(t *testing.T) {
	r := SearchResultsPaginationResolver{M: rdb.SearchResultsPagination{PageEnd: 2, TotalRecords: 3}}
	require.Equal(t, true, *r.HasMore())
	r.M.PageEnd = 3
	require.Equal(t, false, *r.HasMore())
	r.M.TotalRecordsAbsent = true
	require.Nil(t, r.HasMore())
}
