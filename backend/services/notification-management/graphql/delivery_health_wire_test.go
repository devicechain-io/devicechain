// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

// deliveryHealth is computed per channel and can fail on its own: the caller may hold
// notification:write but not notification:read, or the ledger read may fail. Its failure
// must cost that one field, never the channel it hangs off — and on a mutation, never the
// result of a write that has already committed.
//
// newWireCtx migrates no delivery-ledger table, so the health read fails with a database
// error here, which is the second case for free.
func TestDeliveryHealthFailureNullsOnlyTheField(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	// A writer without notification:read creates a channel and asks for its health.
	ctx := newWireCtx(t)
	writer := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationWrite)}})
	res := schema.Exec(writer, `mutation {
		createNotificationChannel(request: {token: "smtp-a", channelType: "smtp", enabled: true}) {
			token
			deliveryHealth { consecutiveFailures }
		}
	}`, "", nil)
	var created struct {
		CreateNotificationChannel *struct {
			Token          string           `json:"token"`
			DeliveryHealth *json.RawMessage `json:"deliveryHealth"`
		} `json:"createNotificationChannel"`
	}
	if err := json.Unmarshal(res.Data, &created); err != nil {
		t.Fatalf("decode %s: %v", res.Data, err)
	}
	if created.CreateNotificationChannel == nil || created.CreateNotificationChannel.Token != "smtp-a" {
		t.Fatalf("the committed create was reported as null because a health read failed: data=%s errors=%v",
			res.Data, res.Errors)
	}
	if len(res.Errors) == 0 {
		t.Fatalf("a refused health read must still say why: data=%s", res.Data)
	}

	// A reader lists channels; every health read fails (no ledger table); the list stands.
	reader := context.Context(ctx)
	res = schema.Exec(reader, `{
		notificationChannels(criteria: {pageNumber: 1, pageSize: 10}) {
			results { token deliveryHealth { consecutiveFailures } }
		}
	}`, "", nil)
	var listed struct {
		NotificationChannels *struct {
			Results []struct {
				Token string `json:"token"`
			} `json:"results"`
		} `json:"notificationChannels"`
	}
	if err := json.Unmarshal(res.Data, &listed); err != nil {
		t.Fatalf("decode %s: %v", res.Data, err)
	}
	if listed.NotificationChannels == nil || len(listed.NotificationChannels.Results) != 1 {
		t.Fatalf("a failing health read nulled the channel list: data=%s errors=%v", res.Data, res.Errors)
	}
}
