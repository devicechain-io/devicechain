// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// Pin the shared ListOf guarantee at the GraphQL boundary too: neither an empty
// list nor a nonempty page makes a failed COUNT a successful response.
func TestConnectorsCountFailureOverTheWire(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "nonempty"
		if empty {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			ctx := connectorWireCtx(t)
			ctx = auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.ConnectorRead)}})
			api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
			if !empty {
				for _, token := range []string{"first", "second"} {
					if _, err := api.CreateConnector(ctx, &model.ConnectorCreateRequest{
						Token: token, Type: "mqtt",
						Config: `{"urls":["tcp://broker:1883"],"topic":"alerts"}`,
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			const doc = `{ connectors(criteria: {pageNumber: 1, pageSize: 1}) {
			  results { token } pagination { totalRecords }
			} }`
			wantErr := errors.New("connector list COUNT unavailable")
			countAttempts := 0
			const callback = "test:connector_count_failure"
			db := api.RDB.Database
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
				if _, count := q.Statement.Dest.(*int64); count && q.Statement.Table == "connectors" {
					countAttempts++
					q.AddError(wantErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			res := schema.Exec(ctx, doc, "", nil)
			if countAttempts != 1 || len(res.Errors) != 1 || res.Errors[0].Message != wantErr.Error() {
				t.Fatalf("COUNT attempts=%d, errors=%v, data=%s", countAttempts, res.Errors, res.Data)
			}
			var failed map[string]json.RawMessage
			if err := json.Unmarshal(res.Data, &failed); err != nil {
				t.Fatal(err)
			}
			if page, ok := failed["connectors"]; ok && string(page) != "null" {
				t.Fatalf("failed COUNT returned a successful page: %s", page)
			}
			if err := db.Callback().Query().Remove(callback); err != nil {
				t.Fatal(err)
			}
			// The same query succeeds once COUNT recovers. Two rows with a page
			// size of one distinguish the real total from len(page).
			res = schema.Exec(ctx, doc, "", nil)
			if len(res.Errors) != 0 {
				t.Fatal(res.Errors)
			}
			var data struct {
				Connectors struct {
					Results    []struct{ Token string }
					Pagination struct{ TotalRecords int }
				}
			}
			if err := json.Unmarshal(res.Data, &data); err != nil {
				t.Fatal(err)
			}
			wantRows, wantTotal := 1, 2
			if empty {
				wantRows, wantTotal = 0, 0
			}
			if len(data.Connectors.Results) != wantRows || data.Connectors.Pagination.TotalRecords != wantTotal {
				t.Fatalf("recovered list: %s", res.Data)
			}
		})
	}
}
