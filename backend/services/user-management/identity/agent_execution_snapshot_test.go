// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// assertDecisionTransaction observes every query, row and raw read, including preloads.
// Without a snapshot they use the pool; separate transactions also fail the identity check.
func assertDecisionTransaction(t *testing.T, db *gorm.DB, run func()) {
	t.Helper()
	type statement struct {
		transaction bool
		pool        string
	}
	var reads []statement
	armed := false
	record := func(db *gorm.DB) {
		if armed {
			_, ok := db.Statement.ConnPool.(gorm.TxCommitter)
			reads = append(reads, statement{transaction: ok, pool: fmt.Sprintf("%p", db.Statement.ConnPool)})
		}
	}
	const name = "test:decision-transaction"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, record))
	require.NoError(t, db.Callback().Row().After("gorm:row").Register(name, record))
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register(name, record))
	defer func() {
		require.NoError(t, db.Callback().Query().Remove(name))
		require.NoError(t, db.Callback().Row().Remove(name))
		require.NoError(t, db.Callback().Raw().Remove(name))
	}()
	armed = true
	run()
	armed = false
	require.NotEmpty(t, reads)
	for i, read := range reads {
		require.True(t, read.transaction, "statement %d escaped the transaction", i)
		require.Equal(t, reads[0].pool, read.pool, "statement %d used another connection", i)
	}
}

// Every decision's reads must share one transaction; previously every read used the pool.
func TestEveryDecisionReadRunsInOneTransaction(t *testing.T) {
	x := newExecEnv(t)
	x.addApprover(t, "second@example.invalid")
	req := x.request("boss@example.invalid", "second@example.invalid")
	assertDecisionTransaction(t, x.m.db.Database, func() {
		d := x.decide(t, req)
		require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
		require.Equal(t, req.Approvers, d.ValidApprovers)
	})
	sa := tenantServiceAccountRequest(t, x)
	assertDecisionTransaction(t, x.m.db.Database, func() {
		d := x.decide(t, sa)
		require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
		require.Equal(t, sa.Approvers, d.ValidApprovers)
	})
	// A tenant SSO proposer also reads the connection, identity link and served tenants.
	ctx := context.Background()
	conn := &iam.IdpConnection{
		Token: "snapshot-sso", Name: "Snapshot SSO", Kind: iam.IdpKindTenant, Enabled: true,
		Issuer: "https://idp.example.invalid", ClientID: "snapshot-client", SessionMaxAgeSeconds: 3600,
	}
	require.NoError(t, x.store.CreateIdpConnection(ctx, conn))
	require.NoError(t, x.store.ReplaceConnectionTenants(ctx, conn, []string{agentTenant}))
	id, err := x.store.IdentityByEmail(ctx, agentPerson)
	require.NoError(t, err)
	require.NoError(t, x.store.CreateIdentityLink(ctx, &iam.IdentityLink{
		IdentityID: id.ID, ConnectionID: conn.ID, Subject: "snapshot-proposer", CreatedBy: "test",
	}))
	rec, err := x.m.loadSession(x.sid, agentPerson)
	require.NoError(t, err)
	rec.Method = SessionMethodSso
	rec.ConnectionID = conn.ID
	rec.Anchor = x.m.clock()
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	_, err = x.kvr.Put(sessionKey(x.sid), b)
	require.NoError(t, err)
	d := x.decide(t, req)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict, "reasons: %v", d.Reasons)
	require.Equal(t, req.Approvers, d.ValidApprovers)
	assertDecisionTransaction(t, x.m.db.Database, func() {
		d := x.decide(t, req)
		require.Equal(t, agentpolicy.VerdictApprove, d.Verdict, "reasons: %v", d.Reasons)
		require.Equal(t, req.Approvers, d.ValidApprovers)
	})
	agent := &auth.Claims{Tenant: agentTenant, Username: agentPerson, ClientId: agentClient,
		Scope: auth.ScopeActAlarms, Authorities: []string{string(auth.AlarmPropose)}, TokenType: auth.TokenTypeAccess}
	var version int
	assertDecisionTransaction(t, x.m.db.Database, func() {
		d, err := x.m.AgentActuationDecision(context.Background(), agent, agentpolicy.Request{Kind: req.Kind, TargetCount: 1})
		require.NoError(t, err)
		require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
		version = d.PolicyVersion
	})
	assertDecisionTransaction(t, x.m.db.Database, func() {
		_, err := x.m.AgentActuationDetail(context.Background(), agent, req.Kind, version)
		require.NoError(t, err)
	})
}

type sessionCountingKV struct {
	nats.KeyValue
	counts map[string]int
	key    string
	gets   int
	fault  error
}

func (kv *sessionCountingKV) Get(key string) (nats.KeyValueEntry, error) {
	if kv.counts != nil {
		kv.counts[key]++
	}
	if key == kv.key {
		kv.gets++
		if kv.fault != nil {
			return nil, kv.fault
		}
	}
	return kv.KeyValue.Get(key)
}

// Normal approval reaches tenant admission; previously it loaded the same KV record twice.
func TestProposerSessionReadOnceAndPassedThrough(t *testing.T) {
	x := newExecEnv(t)
	x.addApprover(t, "second@example.invalid")
	kv := &sessionCountingKV{KeyValue: x.kvr, key: sessionKey(x.sid), counts: map[string]int{}}
	x.m.refreshKV = kv
	req := x.request("boss@example.invalid", "second@example.invalid")
	// Every named session must already have been read when the first snapshot SQL runs.
	const callback = "test:sessions-before-snapshot"
	require.NoError(t, x.m.db.Database.Callback().Query().Before("gorm:query").Register(callback, func(*gorm.DB) {
		require.Equal(t, 1, kv.gets)
		for _, s := range req.ApproverSessions {
			require.Equal(t, 1, kv.counts[sessionKey(auth.SessionID(s.Sid))])
		}
	}))
	defer func() { require.NoError(t, x.m.db.Database.Callback().Query().Remove(callback)) }()
	d := x.decide(t, req)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
	require.Equal(t, req.Approvers, d.ValidApprovers)
	require.Equal(t, 1, kv.gets)
	for _, s := range req.ApproverSessions {
		require.Equal(t, 1, kv.counts[sessionKey(auth.SessionID(s.Sid))])
	}
}

// An early KV error must still lose to a policy refusal, despite being loaded first.
func TestProposerSessionErrorKeepsPolicyPrecedence(t *testing.T) {
	x := newExecEnv(t)
	fault := errors.New("KV unavailable")
	x.m.refreshKV = &sessionCountingKV{KeyValue: x.kvr, key: sessionKey(x.sid), fault: fault}
	x.m.SetAgentActuation(false)
	d := x.decide(t, x.request("boss@example.invalid"))
	require.Contains(t, d.Reasons, agentpolicy.ReasonInstanceOff)
	x.m.SetAgentActuation(true)
	_, err := x.m.AgentExecutionDecision(context.Background(), personClaims(agentPerson, string(auth.AlarmWrite)), x.request())
	require.ErrorIs(t, err, fault)
}

// Helpers that cannot bind to the transaction must fail, rather than silently escape it.
// Nested snapshots reuse the outer context and callback errors propagate unchanged.
func TestStoreSnapshotFailsLoudlyForUnsupportedMethods(t *testing.T) {
	x := newExecEnv(t)
	ctx := context.Background()
	require.False(t, iam.InSnapshot(ctx))
	abort := errors.New("abort snapshot")
	err := x.store.ReadSnapshot(ctx, func(ctx context.Context) error {
		require.True(t, iam.InSnapshot(ctx))
		require.NoError(t, x.store.ReadSnapshot(ctx, func(inner context.Context) error {
			require.Equal(t, ctx, inner)
			return nil
		}))
		_, err := x.store.AuditEvents(ctx, rdb.AuditEventSearchCriteria{})
		require.ErrorIs(t, err, iam.ErrOutsideSnapshot)
		require.ErrorContains(t, err, "AuditEvents")
		_, err = x.store.PurgeRecords(ctx, iam.PurgeSearchCriteria{})
		require.ErrorIs(t, err, iam.ErrOutsideSnapshot)
		require.ErrorIs(t, x.store.RecordAuth(ctx, "test", "actor", agentTenant, "label"), iam.ErrOutsideSnapshot)
		return abort
	})
	require.ErrorIs(t, err, abort)
	require.False(t, iam.InSnapshot(ctx))
}

// Reusing a record must retain the identity-email check formerly made by loadSession.
func TestAdmitTenantRecordRefusesAnotherIdentity(t *testing.T) {
	x := newExecEnv(t)
	rec, err := x.m.loadSession(x.sid, agentPerson)
	require.NoError(t, err)
	_, err = x.m.admitTenantRecord(context.Background(), rec, &iam.Identity{Email: "other@example.invalid"}, agentTenant, false)
	require.ErrorIs(t, err, errSessionEnded)
}
