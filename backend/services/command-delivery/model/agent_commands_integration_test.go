// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type fixedCommandDecider struct {
	commit func(context.Context) (actiongate.ExecutionDecision, error)
	calls  atomic.Int32
}

func (d *fixedCommandDecider) Actuation(context.Context, actiongate.ActuationRequest) (actiongate.ActuationDecision, error) {
	return actiongate.ActuationDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: 1, Expiry: time.Hour}, nil
}
func (d *fixedCommandDecider) Execution(ctx context.Context, _ actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	if d.calls.Add(1) == 2 && d.commit != nil {
		return d.commit(ctx)
	}
	return execApprove(1, "boss"), nil
}

type fixedCommandTargets struct{}

func (fixedCommandTargets) ReadProposalTarget(context.Context, string, string, []byte, time.Time) (*ProposalTarget, error) {
	return &ProposalTarget{Exists: true, Incarnation: 42, Constrained: true, Found: true, Allowed: true, DefinitionFingerprint: "pg-definition", Impact: "NORMAL", Anchors: []string{}}, nil
}
func pgAgentSetup(t *testing.T) (*Api, context.Context, *AgentProposal) {
	t.Helper()
	api := newPostgresApi(t)
	pool, poolErr := api.RDB.Database.DB()
	require.NoError(t, poolErr)
	pool.SetMaxOpenConns(8)
	api.CommandProposalsEnabled = true
	api.AgentDecider = &fixedCommandDecider{}
	api.ProposalTargets = fixedCommandTargets{}
	api.KillFeed = NewKillFeed(0, nil)
	api.KillFeed.Beat(time.Now())
	ctx := core.WithTenant(context.Background(), "acme")
	out, err := api.ProposeCommand(ctx, rigAgent, commandReq("one"))
	require.NoError(t, err)
	require.NotNil(t, out.Proposal)
	return api, ctx, out.Proposal
}
func secondCommandConnection(t *testing.T, api *Api) *sql.DB {
	t.Helper()
	dialect, ok := api.RDB.Database.Dialector.(*postgres.Dialector)
	require.True(t, ok)
	db, err := sql.Open("pgx", dialect.Config.DSN)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	t.Cleanup(func() { db.Close() })
	return db
}
func pgVisibleCommandCount(db *sql.DB, token string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM "command-delivery".commands WHERE tenant_id = $1 AND token = $2 AND deleted_at IS NULL`, "acme", token).Scan(&n)
	return n, err
}

// Lease a separate connection for each racing caller before releasing its
// start barrier; a fast first transaction cannot turn this into a serial test.
func pgCommandConnection(api *Api, ready chan<- struct{}, start <-chan struct{}, run func(*Api) error) error {
	return api.RDB.Database.Connection(func(connection *gorm.DB) error {
		manager := rdb.RdbManager{Database: connection}
		caller := *api
		caller.RDB = &manager
		ready <- struct{}{}
		<-start
		return run(&caller)
	})
}
func TestPostgresAgentConcurrentFinalApprovals(t *testing.T) {
	api, ctx, p := pgAgentSetup(t)
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- pgCommandConnection(api, ready, start, func(caller *Api) error {
				got, err := caller.ApproveAgentProposal(ctx, approver("boss", time.Now()), p.Token, p.Digest)
				if err == nil && got.State == "EXECUTED" {
					won.Add(1)
				}
				return err
			})
		}()
	}
	<-ready
	<-ready
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			require.True(t, actiongate.IsCode(err, actiongate.CodeNotPending) || actiongate.IsCode(err, actiongate.CodeConcurrentApproval), "unexpected approval error: %v", err)
		}
	}
	require.EqualValues(t, 1, won.Load())
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Command{}).Where("proposal_token = ?", p.Token).Count(&n).Error)
	require.EqualValues(t, 1, n)
	stored, err := api.AgentProposalByToken(ctx, p.Token, nil)
	require.NoError(t, err)
	require.Equal(t, "EXECUTED", stored.State)
	require.Equal(t, 1, stored.Revision)
	require.NoError(t, api.RDB.DB(ctx).Model(&rdb.AuditEvent{}).Where("table_name LIKE ? AND operation = ? AND proposal = ?", "%agent_proposals", "update", p.Token).Count(&n).Error)
	require.EqualValues(t, 1, n, "one executed ledger transition")
}

type pgVisibleNudger struct {
	db      *sql.DB
	token   string
	calls   int
	visible int
	err     error
}

func (n *pgVisibleNudger) NudgeDevice(string, string) {
	n.calls++
	n.visible, n.err = pgVisibleCommandCount(n.db, n.token)
}
func TestPostgresAgentNudgeSeesOuterCommitAndRollbackDoesNotNudge(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[refuse], func(t *testing.T) {
			api, ctx, p := pgAgentSetup(t)
			db := secondCommandConnection(t, api)
			n := &pgVisibleNudger{db: db, token: p.EffectToken}
			api.Nudger = n
			if refuse {
				api.AgentDecider = &fixedCommandDecider{commit: func(context.Context) (actiongate.ExecutionDecision, error) {
					visible, err := pgVisibleCommandCount(db, p.EffectToken)
					require.NoError(t, err)
					require.Zero(t, visible, "another connection sees no staged command")
					return actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse, Reasons: []string{"TENANT_ACTUATION_OFF"}}, nil
				}}
			}
			got, err := api.ApproveAgentProposal(ctx, approver("boss", time.Now()), p.Token, p.Digest)
			require.NoError(t, err)
			visible, err := pgVisibleCommandCount(db, p.EffectToken)
			require.NoError(t, err)
			if refuse {
				require.Zero(t, n.calls)
				require.Zero(t, visible)
				require.Equal(t, "REFUSED", got.State)
			} else {
				require.Equal(t, 1, n.calls)
				require.NoError(t, n.err)
				require.Equal(t, 1, n.visible)
				require.Equal(t, 1, visible)
			}
		})
	}
}
func TestPostgresAgentKillRacingApprovalCancelsCommittedCommand(t *testing.T) {
	api, ctx, p := pgAgentSetup(t)
	atFence := make(chan struct{})
	release := make(chan struct{})
	api.afterFenceForTest = func(*gorm.DB) error { close(atFence); <-release; return nil }
	approvalDone := make(chan error, 1)
	go func() {
		_, err := api.ApproveAgentProposal(ctx, approver("boss", time.Now()), p.Token, p.Digest)
		approvalDone <- err
	}()
	<-atFence
	killDone := make(chan error, 1)
	killStarted := make(chan struct{})
	go func() { close(killStarted); killDone <- api.ApplyKillEvent(ctx, killForCommands()) }()
	<-killStarted
	close(release)
	approvalErr := <-approvalDone
	require.NoError(t, <-killDone)
	var rows []Command
	require.NoError(t, api.RDB.DB(ctx).Where("proposal_token = ?", p.Token).Find(&rows).Error)
	if approvalErr == nil {
		require.Len(t, rows, 1)
	}
	for _, c := range rows {
		require.Equal(t, "CANCELLED", c.Status, "no live agent command may survive the committed kill")
	}
}
func TestPostgresAgentConcurrentProposalsAtClientCap(t *testing.T) {
	api, ctx, _ := pgAgentSetup(t)
	for i := 1; i < 19; i++ {
		req := commandReq(time.Unix(int64(i), 0).Format(time.RFC3339))
		out, err := api.ProposeCommand(ctx, rigAgent, req)
		require.NoError(t, err)
		require.Equal(t, ProposalStatusPending, out.Status)
	}
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	var wg sync.WaitGroup
	var admitted atomic.Int32
	errs := make(chan error, 2)
	for _, id := range []string{"race-a", "race-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- pgCommandConnection(api, ready, start, func(caller *Api) error {
				out, err := caller.ProposeCommand(ctx, rigAgent, commandReq(id))
				if err == nil && out.Status == ProposalStatusPending {
					admitted.Add(1)
				}
				return err
			})
		}(id)
	}
	<-ready
	<-ready
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, admitted.Load())
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&AgentProposal{}).Where("state = ?", "PROPOSED").Count(&n).Error)
	require.EqualValues(t, 20, n)
}

// jsonb rewrites some number spellings and reorders keys; the digest is judged on the
// re-canonicalized value, so every payload a proposal accepts must survive a real
// Postgres round trip and still execute with the exact approved bytes. The spellings jsonb
// would rewrite are refused at proposal, before anything is stored.
func TestPostgresAgentPayloadSurvivesJsonbAndExecutes(t *testing.T) {
	api, ctx, _ := pgAgentSetup(t)
	raw := `{"zeta":1.10,"big":12345678901234567890123,"neg":-0.5,"zero":0.0,"text":"<&> é \u2028","nested":{"b":[1,2.50,{"k":null}],"a":true}}`
	req := commandReq("jsonb")
	req.Payload = &raw
	out, err := api.ProposeCommand(ctx, rigAgent, req)
	require.NoError(t, err)
	require.Equal(t, ProposalStatusPending, out.Status)
	got, err := api.ApproveAgentProposal(ctx, approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err, "a stored payload must still verify after jsonb")
	require.Equal(t, "EXECUTED", got.State)
	var cmd Command
	require.NoError(t, api.RDB.DB(ctx).Where("proposal_token = ?", out.Proposal.Token).First(&cmd).Error)
	require.NotNil(t, cmd.Payload)
	stored, err := canonicalJSON(*cmd.Payload, true)
	require.NoError(t, err)
	require.Equal(t, string(out.Preview.Payload), string(stored))

	for i, bad := range []string{`{"a":1e2}`, `{"a":-0}`} {
		b := bad
		req := commandReq(fmt.Sprintf("bad-%d", i))
		req.Payload = &b
		_, err := api.ProposeCommand(ctx, rigAgent, req)
		require.Error(t, err, bad)
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(&AgentProposal{}).Where("request_id = ?", req.RequestId).Count(&n).Error)
		require.Zero(t, n)
	}
}

// On Postgres the unique (tenant_id, proposal_token) index is real: a soft-deleted command
// of this proposal must still answer ALREADY_ADMITTED, not a unique violation.
func TestPostgresAgentSoftDeletedAdmissionIsAlreadyAdmitted(t *testing.T) {
	api, ctx, p := pgAgentSetup(t)
	prior := &Command{TokenReference: rdb.TokenReference{Token: "earlier-effect"}, DeviceToken: "dev-1", Name: "reboot", Status: CommandCancelled.String(), Outcome: sql.NullString{String: OutcomeNotDelivered.String(), Valid: true}, ProposalToken: sql.NullString{String: p.Token, Valid: true}, CauseKind: sql.NullString{String: CauseKindProposal, Valid: true}}
	require.NoError(t, api.RDB.DB(ctx).Create(prior).Error)
	require.NoError(t, api.RDB.DB(ctx).Delete(prior).Error)
	_, err := api.ApproveAgentProposal(ctx, approver("boss", time.Now()), p.Token, p.Digest)
	require.True(t, actiongate.IsCode(err, "ALREADY_ADMITTED"), "got %v", err)
	stored, err := api.AgentProposalByToken(ctx, p.Token, nil)
	require.NoError(t, err)
	require.Equal(t, "PROPOSED", stored.State)
}

// The retention purge's DELETE ... WHERE id IN (subquery) runs under a system context on
// Postgres, across tenants, and leaves PROPOSED rows alone.
func TestPostgresAgentPurge(t *testing.T) {
	api, ctx, p := pgAgentSetup(t)
	old := time.Now().UTC().Add(-ProposalRetention - time.Hour)
	for _, tenant := range []string{"acme", "other"} {
		tctx := core.WithTenant(context.Background(), tenant)
		row := *p
		row.ID = 0
		row.Token, row.EffectToken, row.RequestId = tenant+"-old", tenant+"-old-effect", tenant+"-old"
		row.TenantScoped = rdb.TenantScoped{}
		row.State = "EXPIRED"
		row.CreatedAt, row.UpdatedAt, row.DecidedAt = old, old, &old
		require.NoError(t, api.RDB.DB(tctx).Create(&row).Error)
	}
	require.NoError(t, api.RDB.DB(ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("created_at", old).Error)
	n, err := api.PurgeAgentProposals(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	var left int64
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).Unscoped().Model(&AgentProposal{}).Count(&left).Error)
	require.EqualValues(t, 1, left, "only the PROPOSED row stays")
}

// A replayed kill judged before a concurrent re-enable must not close the re-enabled fence or
// cancel what was admitted after it. The re-enable commits on ANOTHER connection inside the
// kill's window (after the kill locked the proposals, before it judges the fence); the kill
// must then see the re-enabled fence under its lock, judge itself stale and change nothing.
func TestPostgresAgentKillReplayRacingReenableIsStale(t *testing.T) {
	api, ctx, p := pgAgentSetup(t)
	kill := actiongate.KillEvent{EventID: "kill-3", Tenant: "acme", Cause: actiongate.KillCauseTenantDisabled, ActuationGeneration: 3, At: time.Now().UTC()}
	_, err := api.ApplyKillEventWithCounts(ctx, kill)
	require.NoError(t, err)
	// The first kill ended the pending proposal; make a fresh one for after the re-enable.
	out, err := api.ProposeCommand(ctx, rigAgent, commandReq("after-reenable"))
	require.NoError(t, err)
	require.NotEqual(t, p.Token, out.Proposal.Token)
	reenabled := false
	api.afterKillProposalsForTest = func() {
		if reenabled {
			return
		}
		reenabled = true
		other := secondCommandConnection(t, api)
		_, err := other.Exec(`UPDATE "command-delivery".agent_actuation_fences SET enabled = true, epoch = epoch + 1 WHERE tenant_id = $1 AND enabled = false AND killed_at_generation < $2`, "acme", 4)
		require.NoError(t, err)
	}
	counts, err := api.ApplyKillEventWithCounts(ctx, kill)
	require.NoError(t, err)
	require.True(t, reenabled)
	require.True(t, counts.Stale, "the replay must be judged on the fence as it is under the lock")
	stored, err := api.AgentProposalByToken(ctx, out.Proposal.Token, nil)
	require.NoError(t, err)
	require.Equal(t, "PROPOSED", stored.State, "a stale replay must roll back the proposal update")
	var enabled bool
	require.NoError(t, api.RDB.DB(ctx).Raw(`SELECT enabled FROM "command-delivery".agent_actuation_fences WHERE tenant_id = ?`, "acme").Scan(&enabled).Error)
	require.True(t, enabled, "a stale replay must not close the re-enabled fence")
}
