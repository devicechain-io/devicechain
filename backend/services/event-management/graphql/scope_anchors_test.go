// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// scopedCtx is a request context holding a verified tenant access token that grants
// authorities and, when anchors is non-nil, is scoped to them.
func scopedCtx(api *model.Api, tenant string, anchors []string, authorities ...string) context.Context {
	ctx := core.WithTenant(context.Background(), tenant)
	ctx = auth.WithClaims(ctx, &auth.Claims{
		Tenant:       tenant,
		Username:     "member@example.com",
		TokenType:    auth.TokenTypeAccess,
		Authorities:  authorities,
		ScopeAnchors: anchors,
	})
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

// A device co-owned by Acme and Globex records both anchors on its event. Acme's member
// reads the event — it was recorded while the device belonged to Acme — but the event's
// anchors list only customer:acme: telling Acme that Globex also owns the device is a
// disclosure about another customer, not about Acme's own data. Globex sees the mirror
// image, and the unscoped control sees both, so the narrowing is the scope's doing.
func TestAScopedMemberSeesOnlyItsOwnAnchorsOnACoOwnedEvent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Event{}, &model.EventAnchor{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})

	const tenant = "tenant1"
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	event := model.Event{DeviceToken: "co-owned", EventType: eventtype.Measurement, OccurredTime: at, ProcessedTime: at, Source: "mqtt"}
	event.EventId = model.DeriveEventId(tenant, &event, nil)
	write := core.WithTenant(context.Background(), tenant)
	require.NoError(t, db.WithContext(write).Create(&event).Error)
	for _, token := range []string{"acme", "globex"} {
		require.NoError(t, db.WithContext(write).Create(&model.EventAnchor{
			EventId: event.EventId, DeviceToken: "co-owned", EventType: eventtype.Measurement,
			OccurredTime: at, AnchorType: "customer", AnchorToken: token,
		}).Error)
	}

	anchorsSeen := func(anchors []string) []string {
		t.Helper()
		r := &SchemaResolver{}
		found, err := r.Events(scopedCtx(api, tenant, anchors, string(auth.EventRead)), emptyCriteria())
		require.NoError(t, err)
		results := found.Results()
		require.Len(t, results, 1, "the co-owned event belongs to each owner's history")
		refs, err := results[0].Anchors()
		require.NoError(t, err)
		out := make([]string, 0, len(refs))
		for _, ref := range refs {
			out = append(out, ref.Type()+":"+ref.Token())
		}
		sort.Strings(out)
		return out
	}

	assert.Equal(t, []string{"customer:acme", "customer:globex"}, anchorsSeen(nil),
		"control: the unscoped member sees both anchors")
	assert.Equal(t, []string{"customer:acme"}, anchorsSeen([]string{"customer:acme"}),
		"Acme's member must not learn that Globex also owns the device")
	assert.Equal(t, []string{"customer:globex"}, anchorsSeen([]string{"customer:globex"}),
		"Globex's member must not learn that Acme also owns the device")
}
