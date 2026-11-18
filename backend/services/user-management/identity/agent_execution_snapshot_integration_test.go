// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package identity

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// hookAfterPolicy runs a committed change through the pool after the first version read.
// Its flag is set before the change, since that change can itself run query callbacks.
func hookAfterPolicy(t *testing.T, db *gorm.DB, change func() error) (arm func(), disarm func(), fired *atomic.Bool) {
	t.Helper()
	var armed atomic.Bool
	fired = &atomic.Bool{}
	const name = "test:snapshot-hook"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(query *gorm.DB) {
		if !armed.Load() || query.Statement.Table != (iam.AgentPolicyVersion{}).TableName() || query.Error != nil {
			return
		}
		if fired.CompareAndSwap(false, true) {
			if err := change(); err != nil {
				query.AddError(err)
			}
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(name)) })
	return func() { armed.Store(true) }, func() { armed.Store(false) }, fired
}

func requireSnapshotBoss(t *testing.T, d ExecutionDecision) {
	t.Helper()
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict, "reasons: %v", d.Reasons)
	require.Equal(t, []string{"boss@example.invalid"}, d.ValidApprovers)
}

// Both real states approve boss. A mixed old-policy/new-membership read refuses,
// so this fails by verdict without repeatable-read isolation, rather than by timing.
func TestExecutionDecisionReadsOneSnapshot(t *testing.T) {
	rdbm := newIdentityPgDB(t)
	x := newExecEnvOn(t, rdbm)
	ctx := context.Background()
	auditors := &iam.Role{Scope: iam.ScopeTenant, Token: "auditors", Authorities: []string{string(auth.AlarmWrite)}}
	require.NoError(t, x.store.CreateRole(ctx, auditors))
	boss, err := x.store.IdentityByEmail(ctx, "boss@example.invalid")
	require.NoError(t, err)
	mem := findMembership(boss.Memberships, agentTenant)
	require.NotNil(t, mem)
	doc, err := agentpolicy.Parse([]byte(`{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["auditors"]}}`))
	require.NoError(t, err)
	canonical, err := doc.Canonical()
	require.NoError(t, err)
	arm, disarm, fired := hookAfterPolicy(t, rdbm.Database, func() error {
		// Background uses the pool, not the decision's snapshot context. These two
		// mutations commit together, so there is no real state that refuses boss.
		return rdbm.Database.WithContext(core.WithSystemContext(context.Background())).Transaction(func(tx *gorm.DB) error {
			var head iam.AgentPolicy
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ?", agentTenant).First(&head).Error; err != nil {
				return err
			}
			v := &iam.AgentPolicyVersion{TenantId: agentTenant, Version: head.PublishedVersion + 1,
				Document: string(canonical), Origin: iam.AgentPolicyOriginPublish, PublishedBy: "test"}
			if err := tx.Create(v).Error; err != nil {
				return err
			}
			if err := tx.Model(&head).Update("published_version", v.Version).Error; err != nil {
				return err
			}
			return tx.Model(mem).Association("TenantRoles").Replace([]iam.Role{*auditors})
		})
	})
	before := x.decide(t, x.request("boss@example.invalid"))
	requireSnapshotBoss(t, before)
	arm()
	hooked := x.decide(t, x.request("boss@example.invalid"))
	disarm()
	require.True(t, fired.Load(), "the policy-version read must trigger the change")
	after := x.decide(t, x.request("boss@example.invalid"))
	requireSnapshotBoss(t, hooked)
	requireSnapshotBoss(t, after)
	require.True(t, hooked.PolicyVersion == before.PolicyVersion || hooked.PolicyVersion == after.PolicyVersion)
	if hooked.PolicyVersion == before.PolicyVersion {
		require.Equal(t, before, hooked)
	} else {
		require.Equal(t, after, hooked)
	}
}

// A revocation after the policy read is invisible to that decision, but visible
// to the next one. Without a snapshot the hooked decision already loses boss.
func TestExecutionDecisionSnapshotSeesNoLaterRevocation(t *testing.T) {
	rdbm := newIdentityPgDB(t)
	x := newExecEnvOn(t, rdbm)
	boss, err := x.store.IdentityByEmail(context.Background(), "boss@example.invalid")
	require.NoError(t, err)
	mem := findMembership(boss.Memberships, agentTenant)
	require.NotNil(t, mem)
	arm, disarm, fired := hookAfterPolicy(t, rdbm.Database, func() error {
		return x.store.SetMembershipEnabled(context.Background(), mem, false)
	})
	requireSnapshotBoss(t, x.decide(t, x.request("boss@example.invalid")))
	arm()
	hooked := x.decide(t, x.request("boss@example.invalid"))
	disarm()
	require.True(t, fired.Load())
	requireSnapshotBoss(t, hooked)
	after := x.decide(t, x.request("boss@example.invalid"))
	require.NotContains(t, after.ValidApprovers, "boss@example.invalid")
	require.Equal(t, agentpolicy.VerdictRefuse, after.Verdict)
}

// Pin the other side: an already committed revocation belongs to the snapshot.
// This prevents a fix that simply preserves previously valid approvers.
func TestExecutionDecisionSnapshotSeesEarlierRevocation(t *testing.T) {
	x := newExecEnvOn(t, newIdentityPgDB(t))
	requireSnapshotBoss(t, x.decide(t, x.request("boss@example.invalid")))
	boss, err := x.store.IdentityByEmail(context.Background(), "boss@example.invalid")
	require.NoError(t, err)
	mem := findMembership(boss.Memberships, agentTenant)
	require.NotNil(t, mem)
	require.NoError(t, x.store.SetMembershipEnabled(context.Background(), mem, false))
	d := x.decide(t, x.request("boss@example.invalid"))
	require.NotContains(t, d.ValidApprovers, "boss@example.invalid")
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
}

// The same callback assertion as SQLite also proves Postgres preloads stay on one tx.
func TestEveryDecisionReadRunsInOneTransactionPg(t *testing.T) {
	x := newExecEnvOn(t, newIdentityPgDB(t))
	assertDecisionTransaction(t, x.m.db.Database, func() {
		requireSnapshotBoss(t, x.decide(t, x.request("boss@example.invalid")))
	})
}

func TestReadSnapshotRefusesWrites(t *testing.T) {
	store := iam.NewStore(newIdentityPgDB(t))
	ctx := context.Background()
	role := &iam.Role{Scope: iam.ScopeTenant, Token: "snapshot-write"}
	err := store.ReadSnapshot(ctx, func(ctx context.Context) error {
		err := store.CreateRole(ctx, role)
		require.ErrorContains(t, err, "read-only transaction")
		return err
	})
	require.ErrorContains(t, err, "read-only transaction")
	_, err = store.CatalogueRoleByScopeToken(ctx, role.Scope, role.Token)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
