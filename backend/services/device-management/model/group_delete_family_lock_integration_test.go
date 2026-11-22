// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 THE GROUP DELETE'S FAMILY LOCK IS WHAT MAKES ITS IN-USE CHECK TRUE. A rule publish pins a
// group under the membership family lock: it checks the group and writes the rule's scope
// reference in one transaction. The delete takes the same lock before it asks whether any rule
// references the group, so a pin in flight either commits first (and the delete sees it and
// refuses) or waits for the delete (and finds no group). Without the lock the delete asks
// while the pin is uncommitted, sees nothing, and deletes a group a rule then references.
//
// The order test beside this one (TestAGroupDeleteKeepsTheFamilyLockAheadOfTheGroupRow) checks
// WHERE the lock is taken and passes with the lock removed altogether; this one fails without it.
func TestAGroupDeleteWaitsForARulePinningTheGroup(t *testing.T) {
	api, ctx, _ := ownershipPgRig(t, 1)
	_, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "north", MemberType: "device"})
	require.NoError(t, err)
	tenant, _ := core.TenantFromContext(ctx)

	pinned := make(chan struct{})
	commit := make(chan struct{})
	// Released on every way out, so a failed assertion does not leave the pin's transaction
	// open and the test's cleanup waiting on it.
	var once sync.Once
	release := func() { once.Do(func() { close(commit) }) }
	defer release()
	pinDone := make(chan error, 1)
	go func() {
		pinDone <- api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			// What a rule publish does to pin a group: the family lock, then the reference.
			if err := api.lockMembershipFamily(ctx, tx, "device"); err != nil {
				return err
			}
			// The rule token is the run's tenant: (device_profile_id, rule_token) is unique across
			// tenants, and the integration database outlives a run.
			ref := DetectionRuleScopeRef{DeviceProfileId: 1, RuleToken: tenant, GroupToken: "north", GroupVersion: 1}
			ref.TenantId = tenant
			if err := tx.Create(&ref).Error; err != nil {
				return err
			}
			close(pinned)
			<-commit
			return nil
		})
	}()
	select {
	case <-pinned:
	case err := <-pinDone:
		t.Fatalf("the pin failed before it held the family lock and its reference: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := api.DeleteEntityGroup(ctx, "north")
		deleteDone <- err
	}()
	// Give the delete every chance to run ahead of the pin: with the family lock it waits
	// here; without it, it has finished.
	waited := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int64
		err := api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
			Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
				AND wait_event_type = 'Lock' AND query ILIKE '%pg_advisory_xact_lock%'
				AND cardinality(pg_blocking_pids(pid)) > 0`).Scan(&waiting).Error
		if err == nil && waiting > 0 {
			waited = true
			break
		}
		select {
		case err := <-deleteDone:
			release()
			require.NoError(t, <-pinDone)
			t.Fatalf("the group delete finished (err %v) while a rule pinning the group was uncommitted; "+
				"it must wait on the membership family lock", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !waited {
		release()
		require.NoError(t, <-pinDone)
		t.Fatalf("the group delete neither waited on the membership family lock nor finished (it is "+
			"blocked on something else): %v", <-deleteDone)
	}
	release()
	require.NoError(t, <-pinDone)

	err = <-deleteDone
	require.True(t, errors.Is(err, ErrEntityInUse), "the delete after the pin committed: %v, want ErrEntityInUse", err)
	groups, err := api.EntityGroupsByToken(ctx, []string{"north"})
	require.NoError(t, err)
	require.Len(t, groups, 1, "the pinned group is gone")
}
