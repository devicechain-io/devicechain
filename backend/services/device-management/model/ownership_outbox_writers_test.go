// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The ownership writers the edge-writer sweep (scope_ownership_test.go) and the access-scope
// suite do not reach: the two attribute-driven moves that are not a plain attribute SET, and
// the shape of the queue statement itself.

// A device that leaves an access scope because its attribute was DELETED is announced, as
// one whose attribute was changed is.
func TestADeviceLeavingByADeletedAttributeIsAnnounced(t *testing.T) {
	api, ctx, pub := accessFixture(t)
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	from := pub.count()
	removed, err := api.DeleteEntityAttribute(ctx, "device", "d-arid", string(AttributeScopeShared), "climate")
	require.NoError(t, err)
	require.True(t, removed)
	fact, ok := lastFactFor(pub, from, "d-arid")
	require.True(t, ok, "a device leaving the scope by a deleted attribute was not announced")
	require.Empty(t, fact.Anchors)
	require.NotNil(t, fact.Anchors)
}

// A configuration restore writes attributes, and an attribute can move a device into or out
// of an access scope: the restore's move is announced like any other.
func TestARestoreThatMovesADeviceIntoAnAccessScopeIsAnnounced(t *testing.T) {
	api, ctx := configTestApi(t)
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(ctx)).AutoMigrate(&EntityRelationshipType{}))
	pub := &captureOwnership{api: api, t: t}
	api.DeviceOwnershipPublisher = pub
	seedDeclaredDevice(t, api, ctx, "prof", "dev", []ConfigurationKey{{Key: "climate", ValueType: "STRING"}})
	setShared(t, api, ctx, "dev", "climate", AttributeValueString, "arid")
	arid := desiredOf(t, api, ctx, "dev")
	publishAridGroup(t, api, ctx, "arid")
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	fact, ok := lastFactFor(pub, 0, "dev")
	require.True(t, ok)
	require.Equal(t, []string{"group:arid"}, fact.Anchors)

	setShared(t, api, ctx, "dev", "climate", AttributeValueString, "humid")
	from := pub.count()
	fact, ok = lastFactFor(pub, 0, "dev")
	require.True(t, ok)
	require.Empty(t, fact.Anchors, "the device did not leave the scope")

	_, err = api.RestoreDeviceConfiguration(ctx, "dev", arid.Revision)
	require.NoError(t, err)
	fact, ok = lastFactFor(pub, from, "dev")
	require.True(t, ok, "a restore moving the device into the scope was not announced")
	require.Equal(t, []string{"group:arid"}, fact.Anchors)
}

// 🔴 THE QUEUE STATEMENT NAMES EACH DEVICE ONCE, IN ASCENDING ID. That is the lock order two
// writers queueing overlapping devices share (ownership_outbox.go), and the engine refuses an
// upsert that names one key twice — so it is asserted on the statement's text, not inferred
// from the rows it leaves.
func TestTheQueueStatementTakesEachDeviceOnceInAscendingId(t *testing.T) {
	api, ctx, _ := ownershipFixture(t)
	counter := rdbtest.NewStatementCounter("pending_ownership_facts")
	counter.Record(true)
	counted := api.RDB.Database.Session(&gorm.Session{Logger: counter})
	require.NoError(t, counted.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := queueOwnershipOnTx(tx, []uint{30, 10, 20, 10, 0, 30})
		return err
	}))
	sqls := counter.MarkedSQL()
	require.Len(t, sqls, 1, "queueing six ids took %d statements", len(sqls))
	// The rendered row literal: ("acme",<device_id>,<generation>,<requested_at>).
	ids := regexp.MustCompile(`\("acme",(\d+),`).FindAllStringSubmatch(sqls[0], -1)
	got := make([]string, 0, len(ids))
	for _, m := range ids {
		got = append(got, m[1])
	}
	require.Equal(t, []string{"10", "20", "30"}, got, "statement: %s", sqls[0])
	require.Len(t, queuedOwnership(t, api), 3)
}

// A refused publish ends the pass at once — the broker is shared, so no other tenant's fact
// is tried against it — and every row stays.
func TestARefusedPublishEndsThePassBeforeTheNextTenant(t *testing.T) {
	api, ctx, pub := ownershipFixture(t)
	pub.api = nil
	globex := core.WithTenant(context.Background(), "globex")
	d := &Device{TokenReference: rdb.TokenReference{Token: "g-1"}}
	require.NoError(t, api.RDB.DB(globex).Create(d).Error)
	require.NoError(t, api.RDB.DB(globex).Transaction(func(tx *gorm.DB) error {
		_, err := queueOwnershipOnTx(tx, []uint{d.ID})
		return err
	}))
	assignDozer(t, api, ctx, "acme-mining")
	require.Len(t, queuedOwnership(t, api), 2)

	pub.fail = errors.New("broker unavailable")
	res, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.Error(t, err)
	require.Equal(t, 1, pub.attempts, "the pass went on to another tenant after the broker refused a fact")
	require.Equal(t, 2, res.Tenants)
	require.Len(t, queuedOwnership(t, api), 2, "a refused fact was dropped")
}

// A publish moves an access scope's pointer, and a rollback moves it back: a device in only
// one of the two versions changes anchors either way, and each move announces the device
// that left as well as the one that entered.
func TestAPublishAndARollbackAnnounceTheDevicesTheyMove(t *testing.T) {
	api, ctx, pub := accessFixture(t)
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	fact, ok := lastFactFor(pub, 0, "d-arid")
	require.True(t, ok)
	require.Equal(t, []string{"group:arid"}, fact.Anchors)

	// v2 selects the humid device instead: d-arid leaves, d-humid enters.
	_, err = api.UpdateEntityGroup(ctx, "arid", &EntityGroupUpdateRequest{Selector: dcgraphql.OptionalStringOf(`attr["climate"] == "humid"`)})
	require.NoError(t, err)
	from := pub.count()
	v2, err := api.PublishEntityGroup(ctx, "arid", nil, nil, "tester")
	require.NoError(t, err)
	require.EqualValues(t, 2, v2.Version)
	fact, ok = lastFactFor(pub, from, "d-arid")
	require.True(t, ok, "the device the publish moved OUT was not announced")
	require.Empty(t, fact.Anchors)
	fact, ok = lastFactFor(pub, from, "d-humid")
	require.True(t, ok, "the device the publish moved IN was not announced")
	require.Equal(t, []string{"group:arid"}, fact.Anchors)

	// Back to v1: the reverse.
	from = pub.count()
	_, err = api.RollbackEntityGroup(ctx, "arid", 1)
	require.NoError(t, err)
	fact, ok = lastFactFor(pub, from, "d-humid")
	require.True(t, ok, "the device the rollback moved OUT was not announced")
	require.Empty(t, fact.Anchors)
	fact, ok = lastFactFor(pub, from, "d-arid")
	require.True(t, ok, "the device the rollback moved IN was not announced")
	require.Equal(t, []string{"group:arid"}, fact.Anchors)
}
