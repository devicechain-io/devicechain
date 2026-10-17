// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 A WRITER QUEUES ITS DEVICES ONCE, LAST. The outbox rows are the one lock every ownership
// writer takes, so they must be taken in one place, in one order, after every other lock —
// a writer that queues some devices, does more work, and then queues more can hold the first
// set while waiting for the second, and a writer that took the second set first (a bulk edge
// create takes its devices in ascending id) closes the cycle. The deadlock detector then fails
// one of the two requests.
//
// The group delete is the writer with two sets: the access scope's members (lost their
// anchor) and the devices with an edge TO the group (lost an edge). Here a second writer
// holds the edge device and is about to take the member — the moment a bulk create of
// edges from both devices would be in, had the group delete's first set landed between its
// two rows — and the group delete must not be waiting on it while holding the member.
func TestAGroupDeleteQueuesItsDevicesOnceAndLast(t *testing.T) {
	r := newScopeAlarmRig(t)
	// acme-pump has the lower id: a bulk writer takes it first. It gets an edge TO the group.
	// globex-pump is the access scope's member.
	r.region(t, "globex-pump", "north")
	dynamic, selector := "dynamic", `attr["region"] == "north"`
	_, err := r.api.CreateEntityGroup(r.operator, &EntityGroupCreateRequest{Token: "north",
		MemberType: "device", MembershipMode: &dynamic, Selector: &selector})
	require.NoError(t, err)
	_, err = r.api.PublishEntityGroup(r.operator, "north", nil, nil, "tester")
	require.NoError(t, err)
	_, err = r.api.SetEntityGroupAccessScope(r.operator, "north", true)
	require.NoError(t, err)
	_, err = r.api.CreateEntityRelationshipType(r.operator, &EntityRelationshipTypeCreateRequest{Token: "monitors"})
	require.NoError(t, err)
	_, err = r.api.CreateEntityRelationship(r.operator, &EntityRelationshipCreateRequest{
		Token: "acme-pump-monitors-north", SourceType: "device", Source: "acme-pump",
		TargetType: "group", Target: "north", RelationshipType: "monitors"})
	require.NoError(t, err)
	edgeDevice, member := r.devices["acme-pump"], r.devices["globex-pump"]
	require.Less(t, edgeDevice, member, "the rig's ids are not in the order this test needs")

	pub := &countingOwnership{tenant: r.tenant}
	r.api.DeviceOwnershipPublisher = pub
	_, err = r.api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)

	// The other writer: holds the edge device's row, as a bulk create of edges from both
	// devices would after its first row, and takes the member's row once the group delete
	// is in flight.
	held := make(chan struct{})
	takeSecond := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- r.api.RDB.DB(r.operator).Transaction(func(tx *gorm.DB) error {
			if _, err := queueOwnershipOnTx(tx, []uint{edgeDevice}); err != nil {
				return err
			}
			close(held)
			<-takeSecond
			_, err := queueOwnershipOnTx(tx, []uint{member})
			return err
		})
	}()
	<-held

	deleteDone := make(chan error, 1)
	go func() {
		_, err := r.api.DeleteEntityGroup(r.operator, "north")
		deleteDone <- err
	}()
	// The group delete is now waiting on the edge device's row (it must be: the other
	// writer holds it). Let the other writer take the member's row: with the member held
	// by the group delete, that is the cycle.
	require.Eventually(t, func() bool {
		var waiting int64
		err := r.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
			Raw("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query ILIKE '%pending_ownership_facts%'").
			Scan(&waiting).Error
		return err == nil && waiting > 0
	}, 10*time.Second, 20*time.Millisecond, "the group delete never waited on the held row")
	close(takeSecond)

	require.NoError(t, <-writerDone, "the other writer was failed")
	require.NoError(t, <-deleteDone, "the group delete was failed")
	rows := queuedFor(t, r.api, r.operator)
	require.Len(t, rows, 2, "both devices are queued once each")
	_, err = r.api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	byToken := map[string][]string{}
	for _, f := range pub.facts {
		byToken[f.DeviceToken] = f.Anchors
	}
	require.Equal(t, []string{"customer:globex"}, byToken["globex-pump"], "the member still carries the deleted group")
	require.Equal(t, []string{"customer:acme"}, byToken["acme-pump"])
}
