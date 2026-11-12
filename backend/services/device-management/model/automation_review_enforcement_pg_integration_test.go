// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// These tests share the integration database and must not use t.Parallel.
func newPGEnforcementRig(t *testing.T) *automationReviewRig {
	t.Helper()
	api := NewApi(newPostgresRdbManager(t))
	sys := api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{
		"automation_review_policies", "automation_review_policy_changes", "automation_review_break_glass",
		"automation_reviews", "devices", "device_types", "device_profile_activations", "device_profile_versions",
		"detection_rule_scope_refs", "detection_rules", "command_definitions", "metric_definitions", "device_profiles", "audit_events",
	} {
		require.NoError(t, sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "device-management".%q CASCADE`, table)).Error)
	}
	ctx, _ := reviewIdentity("before")
	profile := seedProfileWithRule(t, api, ctx, "profile", "r1", true)
	var rule DetectionRule
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "r1").First(&rule).Error)
	rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":26}`)
	require.NoError(t, api.RDB.DB(ctx).Save(&rule).Error)
	baseline, err := api.PublishDeviceProfile(ctx, "profile", nil, nil, "before")
	require.NoError(t, err)
	require.EqualValues(t, 1, baseline.Version)
	result := `{"outcome":"PASSED"}`
	comparer := &fakeRuleComparer{response: &CompareResponse{Ok: true, Outcome: ReviewOutcomePassed,
		Result: result, ResultDigest: reviewHash([]byte(result)), InputDigest: "inputs", EngineBuild: "pg-enforcement",
		Fidelity: ReviewFidelity{Status: "SUPPORTED"},
		Windows: []ReviewWindowResult{{Role: "INCIDENT", Stream: "resolved", FirstSeq: "12", LastSeq: "19"},
			{Role: "NORMAL", Stream: "resolved", FirstSeq: "20", LastSeq: "25"}}}}
	api.RuleComparer = comparer
	api.PrincipalStanding = &fakePrincipalStanding{}
	ctx, _ = reviewIdentity("creator")
	return &automationReviewRig{api: api, ctx: ctx, profile: profile, rule: &rule, comparer: comparer}
}

func pgEnforcementRun(t *testing.T, r *automationReviewRig) *AutomationReview {
	t.Helper()
	in := reviewRunInput()
	in.RequestId = fmt.Sprintf("pg-request-%d", r.count(t)+1)
	_, person := reviewIdentity("creator")
	row, err := r.api.RunAutomationReview(r.ctx, person, in)
	require.NoError(t, err)
	return row
}

func pgEnforcementApproved(t *testing.T, r *automationReviewRig) *AutomationReview {
	t.Helper()
	ctx, _ := reviewIdentity("author")
	definition := fmt.Sprintf(`{"metric":"temp_c","threshold":%g}`, 25+float64(r.count(t)+1)/1000)
	require.NoError(t, r.api.RDB.DB(ctx).Model(r.rule).Update("definition", datatypes.JSON(definition)).Error)
	row := approveRigReview(t, r, pgEnforcementRun(t, r), "reviewer")
	require.Equal(t, ReviewStateApproved, row.State)
	return row
}

func pgVersionCount(t *testing.T, r *automationReviewRig) int64 {
	t.Helper()
	var n int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&n).Error)
	return n
}

func pgApprovedUnchanged(t *testing.T, r *automationReviewRig, row *AutomationReview, count int64) {
	t.Helper()
	require.Equal(t, count, pgVersionCount(t, r))
	stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
	require.NoError(t, err)
	require.Equal(t, ReviewStateApproved, stored.State)
	require.Nil(t, stored.ConsumedVersion)
	require.Nil(t, stored.ConsumedTxid)
	require.Equal(t, row.Revision, stored.Revision)
}

func pgGuardCode(t *testing.T, err error, code string) {
	t.Helper()
	var pg *pgconn.PgError
	require.True(t, errors.As(err, &pg), "expected PgError %s, got %v", code, err)
	require.Equal(t, code, pg.Code)
}

func pgReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(15 * time.Second):
		t.Fatal("database goroutine did not finish within 15s")
		var zero T
		return zero
	}
}

// Copy a valid historical snapshot, but use exactly the columns an old pod writes.
func pgOldVersionInsert(profile uint) string {
	return fmt.Sprintf(`INSERT INTO "device-management".device_profile_versions
		(created_at, updated_at, tenant_id, device_profile_id, version, snapshot, published_by, automation_review_token)
		SELECT clock_timestamp(), clock_timestamp(), tenant_id, device_profile_id,
		(SELECT COALESCE(max(version),0)+1 FROM "device-management".device_profile_versions WHERE device_profile_id=%d),
		snapshot, 'old-pod', NULL FROM "device-management".device_profile_versions
		WHERE device_profile_id=%d ORDER BY version LIMIT 1`, profile, profile)
}

func pgRawInsertProbe(t *testing.T, r *automationReviewRig, code string) {
	t.Helper()
	tx := r.api.RDB.DB(r.ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	res := tx.Exec(pgOldVersionInsert(r.profile.ID))
	if code != "" {
		pgGuardCode(t, res.Error, code)
	} else {
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
	}
}

func TestPGProtectedPublishWithoutReviewIsRefused(t *testing.T) {
	r := newPGEnforcementRig(t)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	before := pgVersionCount(t, r)
	_, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "publisher")
	reviewErrorCode(t, err, "REVIEW_REQUIRED")
	require.Equal(t, before, pgVersionCount(t, r))
	r.changeRule(t, "author")
	for _, outcome := range []string{ReviewOutcomeFailed, ReviewOutcomeInconclusive} {
		r.comparer.response.Outcome = outcome
		r.comparer.response.Result = fmt.Sprintf(`{"outcome":%q}`, outcome)
		r.comparer.response.ResultDigest = reviewHash([]byte(r.comparer.response.Result))
		row := pgEnforcementRun(t, r)
		require.Equal(t, outcome, row.Outcome)
		_, err = publishRigReview(r, row)
		reviewErrorCode(t, err, "REVIEW_NOT_APPROVED")
		require.Equal(t, before, pgVersionCount(t, r))
		stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
		require.NoError(t, err)
		require.Equal(t, ReviewStateRan, stored.State)
	}
}

func TestPGEditOrBaselineChangeAfterApprovalIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		prepare    func(*testing.T, *automationReviewRig)
	}{
		{"draft edit", "REVIEW_STALE_DRAFT_CHANGED", func(t *testing.T, r *automationReviewRig) { r.changeRule(t, "later-author") }},
		{"another publish", "REVIEW_STALE_BASELINE_CHANGED", func(t *testing.T, r *automationReviewRig) {
			second := approveRigReview(t, r, pgEnforcementRun(t, r), "reviewer")
			v, err := publishRigReview(r, second)
			require.NoError(t, err)
			require.EqualValues(t, 2, v.Version)
		}},
		{"same number rollback", "REVIEW_STALE_BASELINE_CHANGED", func(t *testing.T, r *automationReviewRig) {
			reason := "reassert known baseline"
			p, err := r.api.RollbackDeviceProfileWithReason(r.ctx, "profile", 1, &reason)
			require.NoError(t, err)
			require.EqualValues(t, 1, p.ActiveVersion.Int32)
		}},
		{"two approvals", "REVIEW_STALE_POLICY_CHANGED", func(t *testing.T, r *automationReviewRig) {
			want := DefaultReviewPolicySettings()
			want.Required, want.RequiredApprovals = true, 2
			view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
			require.NoError(t, err)
			require.True(t, applied)
			require.Equal(t, 2, view.Effective.RequiredApprovals)
		}},
		{"lost device write", "APPROVER_NO_LONGER_QUALIFIES", func(t *testing.T, r *automationReviewRig) {
			r.api.PrincipalStanding = &fakePrincipalStanding{denied: true}
		}},
		{"became scoped", "APPROVER_NO_LONGER_QUALIFIES", func(t *testing.T, r *automationReviewRig) {
			r.api.PrincipalStanding = &fakePrincipalStanding{denied: true}
		}},
		{"standing unavailable", "REVIEW_UNAVAILABLE", func(t *testing.T, r *automationReviewRig) {
			r.api.PrincipalStanding = &fakePrincipalStanding{err: errors.New("standing down")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPGEnforcementRig(t)
			protectReviewRig(t, r, DefaultReviewPolicySettings())
			row := pgEnforcementApproved(t, r)
			tc.prepare(t, r)
			before := pgVersionCount(t, r)
			_, err := publishRigReview(r, row)
			reviewErrorCode(t, err, tc.code)
			pgApprovedUnchanged(t, r, row, before)
		})
	}
}

func TestPGTwoReplicasPublishOneVersion(t *testing.T) {
	r := newPGEnforcementRig(t)
	other := NewApi(newPostgresRdbManager(t))
	other.PrincipalStanding = &fakePrincipalStanding{}
	poolA, err := r.api.RDB.Database.DB()
	require.NoError(t, err)
	poolB, err := other.RDB.Database.DB()
	require.NoError(t, err)
	require.NotSame(t, poolA, poolB)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	type result struct {
		version *DeviceProfileVersion
		err     error
	}
	for round := 0; round < 20; round++ {
		row := pgEnforcementApproved(t, r)
		before := pgVersionCount(t, r)
		start := make(chan struct{})
		done := make(chan result, 2)
		for _, api := range []*Api{r.api, other} {
			go func(api *Api) {
				<-start
				v, err := api.PublishDeviceProfileReviewed(r.ctx, "profile", nil, nil, "publisher", &row.Token)
				done <- result{v, err}
			}(api)
		}
		close(start)
		wins := 0
		var winner *DeviceProfileVersion
		for range 2 {
			got := pgReceive(t, done)
			if got.err == nil {
				wins++
				winner = got.version
			} else {
				var gate *actiongate.Error
				require.ErrorAs(t, got.err, &gate)
				require.Contains(t, []string{"REVIEW_ALREADY_USED", "REVIEW_STALE_BASELINE_CHANGED"}, gate.Code)
			}
		}
		require.Equal(t, 1, wins, "round %d", round)
		require.Equal(t, before+1, pgVersionCount(t, r))
		require.EqualValues(t, before+1, winner.Version)
		stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
		require.NoError(t, err)
		require.Equal(t, ReviewStateConsumed, stored.State)
		require.NotNil(t, stored.ConsumedVersion)
		require.Equal(t, winner.Version, *stored.ConsumedVersion)
		require.NotNil(t, stored.ConsumedTxid)
		require.Positive(t, *stored.ConsumedTxid)
		var version DeviceProfileVersion
		require.NoError(t, r.api.RDB.DB(r.ctx).First(&version, winner.ID).Error)
		require.NotNil(t, version.AutomationReviewToken)
		require.Equal(t, row.Token, *version.AutomationReviewToken)
	}
}

func TestPGDraftEditRacingPublish(t *testing.T) {
	r := newPGEnforcementRig(t)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	seed := time.Now().UnixNano()
	t.Logf("draft/publish seed: %d", seed)
	rng := rand.New(rand.NewSource(seed))
	published, stale := 0, 0
	for round := 0; round < 30; round++ {
		row := pgEnforcementApproved(t, r)
		before := pgVersionCount(t, r)
		delay := time.Duration(rng.Intn(21)) * time.Millisecond
		start := make(chan struct{})
		edited := make(chan error, 1)
		ctx, _ := reviewIdentity("racing-editor")
		definition := datatypes.JSON(fmt.Sprintf(`{"metric":"temp_c","threshold":%d}`, 30+round))
		go func() {
			<-start
			time.Sleep(delay)
			edited <- r.api.RDB.DB(ctx).Model(&DetectionRule{}).Where("id = ?", r.rule.ID).Update("definition", definition).Error
		}()
		close(start)
		version, err := publishRigReview(r, row)
		require.NoError(t, pgReceive(t, edited))
		if err != nil {
			stale++
			reviewErrorCode(t, err, "REVIEW_STALE_DRAFT_CHANGED")
			pgApprovedUnchanged(t, r, row, before)
		} else {
			published++
			require.Equal(t, before+1, pgVersionCount(t, r))
			var frozen DeviceProfileVersion
			require.NoError(t, r.api.RDB.DB(r.ctx).First(&frozen, version.ID).Error)
			digest, err := SnapshotDigest(frozen.Snapshot)
			require.NoError(t, err)
			require.Equal(t, row.DraftDigest, digest)
		}
	}
	t.Logf("draft/publish outcomes: published=%d stale=%d", published, stale)
	require.Equal(t, 30, published+stale)
}

func pgEnforcementConn(t *testing.T, r *automationReviewRig) (*sql.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	pool, err := r.api.RDB.Database.DB()
	require.NoError(t, err)
	conn, err := pool.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
		cancel()
	})
	_, err = conn.ExecContext(ctx, "BEGIN")
	require.NoError(t, err)
	return conn, ctx
}

// Observe the server's lock graph, so a descheduled goroutine cannot satisfy
// a blocking assertion merely by not having issued its statement yet.
func pgWaitBlockedBy(t *testing.T, r *automationReviewRig, blocker int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		require.NoError(t, r.api.RDB.DB(r.ctx).Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
			AND wait_event_type='Lock' AND ? = ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked).Error)
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no statement blocked on the transaction under test")
}

func TestPGOptInRacesOldStylePublish(t *testing.T) {
	t.Run("opt-in first", func(t *testing.T) {
		r := newPGEnforcementRig(t)
		b, ctxB := pgEnforcementConn(t, r)
		a, ctxA := pgEnforcementConn(t, r)
		var blocker int
		require.NoError(t, b.QueryRowContext(ctxB, "SELECT pg_backend_pid()").Scan(&blocker))
		_, err := b.ExecContext(ctxB, `SELECT 1 FROM "device-management".device_profiles WHERE id=$1 FOR NO KEY UPDATE`, r.profile.ID)
		require.NoError(t, err)
		_, err = b.ExecContext(ctxB, `INSERT INTO "device-management".automation_review_policies
			(created_at,updated_at,tenant_id,device_profile_id,required,revision,updated_by)
			VALUES (clock_timestamp(),clock_timestamp(),'acme',$1,true,1,'creator')`, r.profile.ID)
		require.NoError(t, err)
		started, done := make(chan struct{}), make(chan error, 1)
		go func() {
			close(started)
			_, err := a.ExecContext(ctxA, pgOldVersionInsert(r.profile.ID))
			done <- err
		}()
		<-started
		pgWaitBlockedBy(t, r, blocker)
		select {
		case err := <-done:
			t.Fatalf("old insert did not wait for opt-in: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		_, err = b.ExecContext(ctxB, "COMMIT")
		require.NoError(t, err)
		pgGuardCode(t, pgReceive(t, done), "DCR01")
		_, err = a.ExecContext(ctxA, "ROLLBACK")
		require.NoError(t, err)
		require.EqualValues(t, 1, pgVersionCount(t, r))
		view, err := r.api.AutomationReviewPolicyFor(r.ctx, "profile")
		require.NoError(t, err)
		require.True(t, view.Stored.Required)
	})
	t.Run("publish first", func(t *testing.T) {
		r := newPGEnforcementRig(t)
		a, ctx := pgEnforcementConn(t, r)
		var blocker int
		require.NoError(t, a.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blocker))
		res, err := a.ExecContext(ctx, pgOldVersionInsert(r.profile.ID))
		require.NoError(t, err)
		n, err := res.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		_, err = a.ExecContext(ctx, `UPDATE "device-management".device_profiles SET active_version=2, active_since=clock_timestamp() WHERE id=$1`, r.profile.ID)
		require.NoError(t, err)
		done := make(chan error, 1)
		started := make(chan struct{})
		go func() {
			want := DefaultReviewPolicySettings()
			want.Required = true
			close(started)
			_, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
			if err == nil && !applied {
				err = errors.New("opt-in was not applied")
			}
			done <- err
		}()
		<-started
		pgWaitBlockedBy(t, r, blocker)
		select {
		case err := <-done:
			t.Fatalf("opt-in did not wait for old publish: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		_, err = a.ExecContext(ctx, "COMMIT")
		require.NoError(t, err)
		require.NoError(t, pgReceive(t, done))
		require.EqualValues(t, 2, pgVersionCount(t, r))
		var version DeviceProfileVersion
		require.NoError(t, r.api.RDB.DB(r.ctx).Where("device_profile_id = ? AND version = 2", r.profile.ID).First(&version).Error)
		view, err := r.api.AutomationReviewPolicyFor(r.ctx, "profile")
		require.NoError(t, err)
		require.True(t, view.Stored.Required)
		require.True(t, view.Stored.CreatedAt.After(version.CreatedAt))
		p, err := r.api.deviceProfileByToken(r.ctx, "profile")
		require.NoError(t, err)
		require.EqualValues(t, 2, p.ActiveVersion.Int32)
	})
	t.Run("previous release API barrier", func(t *testing.T) {
		committed, refused := 0, 0
		for round := 0; round < 20; round++ {
			t.Run(strconv.Itoa(round), func(t *testing.T) {
				r := newPGEnforcementRig(t)
				old := NewApi(newPostgresRdbManager(t))
				old.skipReviewGuardForOldPodTest = true
				start := make(chan struct{})
				policyDone, publishDone := make(chan error, 1), make(chan error, 1)
				go func() {
					<-start
					_, err := old.PublishDeviceProfile(r.ctx, "profile", nil, nil, "old-pod")
					publishDone <- err
				}()
				go func() {
					<-start
					want := DefaultReviewPolicySettings()
					want.Required = true
					_, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
					if err == nil && !applied {
						err = errors.New("opt-in was not applied")
					}
					policyDone <- err
				}()
				close(start)
				publishErr := pgReceive(t, publishDone)
				require.NoError(t, pgReceive(t, policyDone))
				view, err := r.api.AutomationReviewPolicyFor(r.ctx, "profile")
				require.NoError(t, err)
				require.True(t, view.Stored.Required)
				if publishErr != nil {
					refused++
					reviewErrorCode(t, publishErr, "REVIEW_REQUIRED")
					require.EqualValues(t, 1, pgVersionCount(t, r))
				} else {
					committed++
					require.EqualValues(t, 2, pgVersionCount(t, r))
					var version DeviceProfileVersion
					require.NoError(t, r.api.RDB.DB(r.ctx).Where("device_profile_id = ? AND version = 2", r.profile.ID).First(&version).Error)
					require.True(t, view.Stored.CreatedAt.After(version.CreatedAt), "old publish must precede opt-in")
				}
			})
		}
		t.Logf("opt-in/API outcomes: committed-before-policy=%d refused=%d", committed, refused)
		require.Equal(t, 20, committed+refused)
	})
}

func pgEnforcementBindings(t *testing.T, r *automationReviewRig) (bound, other *DeviceType, device *Device) {
	t.Helper()
	bound, err := r.api.CreateDeviceType(r.ctx, &DeviceTypeCreateRequest{Token: "bound", ProfileToken: &r.profile.Token})
	require.NoError(t, err)
	other, err = r.api.CreateDeviceType(r.ctx, &DeviceTypeCreateRequest{Token: "other"})
	require.NoError(t, err)
	device, err = r.api.CreateDevice(r.ctx, &DeviceCreateRequest{Token: "device", DeviceTypeToken: "bound"})
	require.NoError(t, err)
	return bound, other, device
}

func TestPGOldPodStatementsOnProtectedProfile(t *testing.T) {
	for _, protected := range []bool{true, false} {
		t.Run(fmt.Sprintf("protected=%t", protected), func(t *testing.T) {
			for _, tc := range []struct{ name, code string }{
				{"version insert", "DCR01"}, {"active version", "DCR01"}, {"active since", "DCR01"},
				{"hard delete", "DCR02"}, {"soft delete", "DCR02"}, {"unbind type", "DCR02"},
				{"bind type", "DCR02"}, {"move device out", "DCR02"}, {"snapshot", "DCR02"}, {"version soft delete", "DCR02"},
				{"profile name", ""}, {"device name", ""}, {"move device in", ""}, {"create device", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := newPGEnforcementRig(t)
					v, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "before")
					require.NoError(t, err)
					require.EqualValues(t, 2, v.Version)
					bound, other, device := pgEnforcementBindings(t, r)
					incoming, err := r.api.CreateDevice(r.ctx, &DeviceCreateRequest{Token: "incoming", DeviceTypeToken: "other"})
					require.NoError(t, err)
					if protected {
						protectReviewRig(t, r, DefaultReviewPolicySettings())
					}
					tx := r.api.RDB.DB(r.ctx).Begin()
					require.NoError(t, tx.Error)
					defer tx.Rollback()
					p := r.profile.ID
					statements := map[string]string{
						"version insert":      pgOldVersionInsert(p),
						"active version":      fmt.Sprintf(`UPDATE "device-management".device_profiles SET active_version=1 WHERE id=%d`, p),
						"active since":        fmt.Sprintf(`UPDATE "device-management".device_profiles SET active_since=clock_timestamp() WHERE id=%d`, p),
						"hard delete":         fmt.Sprintf(`DELETE FROM "device-management".device_profiles WHERE id=%d`, p),
						"soft delete":         fmt.Sprintf(`UPDATE "device-management".device_profiles SET deleted_at=now() WHERE id=%d`, p),
						"unbind type":         fmt.Sprintf(`UPDATE "device-management".device_types SET profile_id=NULL WHERE id=%d`, bound.ID),
						"bind type":           fmt.Sprintf(`UPDATE "device-management".device_types SET profile_id=%d WHERE id=%d`, p, other.ID),
						"move device out":     fmt.Sprintf(`UPDATE "device-management".devices SET device_type_id=%d WHERE id=%d`, other.ID, device.ID),
						"snapshot":            fmt.Sprintf(`UPDATE "device-management".device_profile_versions SET snapshot='{}' WHERE id=%d`, v.ID),
						"version soft delete": fmt.Sprintf(`UPDATE "device-management".device_profile_versions SET deleted_at=now() WHERE id=%d`, v.ID),
						"profile name":        fmt.Sprintf(`UPDATE "device-management".device_profiles SET name='x' WHERE id=%d`, p),
						"device name":         fmt.Sprintf(`UPDATE "device-management".devices SET name='y' WHERE id=%d`, device.ID),
						"move device in":      fmt.Sprintf(`UPDATE "device-management".devices SET device_type_id=%d WHERE id=%d`, bound.ID, incoming.ID),
						"create device":       fmt.Sprintf(`INSERT INTO "device-management".devices (tenant_id,token,device_type_id) VALUES ('acme','raw-created',%d)`, bound.ID),
					}
					if tc.name == "hard delete" && !protected {
						// Remove dependent rows in this rolled-back transaction so a FK cannot
						// mask whether the profile's BEFORE DELETE guard allows the statement.
						require.NoError(t, tx.Exec(`UPDATE "device-management".device_types SET profile_id=NULL WHERE profile_id=?`, p).Error)
						for _, table := range []string{"detection_rules", "metric_definitions", "command_definitions", "device_profile_versions", "device_profile_activations"} {
							require.NoError(t, tx.Exec(fmt.Sprintf(`DELETE FROM "device-management".%s WHERE device_profile_id=?`, table), p).Error)
						}
					}
					res := tx.Exec(statements[tc.name])
					if protected && tc.code != "" {
						pgGuardCode(t, res.Error, tc.code)
					} else {
						require.NoError(t, res.Error)
						require.EqualValues(t, 1, res.RowsAffected)
						switch tc.name {
						case "version insert":
							var inserted DeviceProfileVersion
							require.NoError(t, tx.Where("device_profile_id = ? AND version = 3", p).First(&inserted).Error)
							require.Nil(t, inserted.AutomationReviewToken)
							require.Equal(t, "old-pod", inserted.PublishedBy)
						case "active version", "active since", "soft delete", "profile name":
							var stored DeviceProfile
							require.NoError(t, tx.Unscoped().First(&stored, p).Error)
							switch tc.name {
							case "active version":
								require.EqualValues(t, 1, stored.ActiveVersion.Int32)
							case "active since":
								require.True(t, stored.ActiveSince.Valid)
								require.True(t, stored.ActiveSince.Time.After(v.CreatedAt))
							case "soft delete":
								require.True(t, stored.DeletedAt.Valid)
							case "profile name":
								require.Equal(t, "x", stored.Name.String)
							}
						case "hard delete":
							var n int64
							require.NoError(t, tx.Unscoped().Model(&DeviceProfile{}).Where("id = ?", p).Count(&n).Error)
							require.Zero(t, n)
						case "bind type", "unbind type":
							id := bound.ID
							if tc.name == "bind type" {
								id = other.ID
							}
							var stored DeviceType
							require.NoError(t, tx.First(&stored, id).Error)
							if tc.name == "bind type" {
								require.NotNil(t, stored.ProfileId)
								require.Equal(t, p, *stored.ProfileId)
							} else {
								require.Nil(t, stored.ProfileId)
							}
						case "snapshot", "version soft delete":
							var stored DeviceProfileVersion
							require.NoError(t, tx.Unscoped().First(&stored, v.ID).Error)
							if tc.name == "snapshot" {
								require.JSONEq(t, "{}", string(stored.Snapshot))
							} else {
								require.True(t, stored.DeletedAt.Valid)
							}
						case "device name", "move device out", "move device in", "create device":
							var stored Device
							query := tx.Where("id = ?", device.ID)
							if tc.name == "move device in" {
								query = tx.Where("id = ?", incoming.ID)
							} else if tc.name == "create device" {
								query = tx.Where("token = ?", "raw-created")
							}
							require.NoError(t, query.First(&stored).Error)
							if tc.name == "device name" {
								require.Equal(t, "y", stored.Name.String)
							} else if tc.name == "move device out" {
								require.Equal(t, other.ID, stored.DeviceTypeId)
							} else {
								require.Equal(t, bound.ID, stored.DeviceTypeId)
							}
						}
					}
					require.NoError(t, tx.Rollback().Error)
					require.EqualValues(t, 2, pgVersionCount(t, r))
				})
			}
		})
	}
	// A version that was once published with a review stays consumed by it; that must not
	// license an old pod to make it active again later. Only the publishing transaction's
	// own consumption counts, so re-activating it outside that transaction is a rollback,
	// which needs break-glass.
	t.Run("reactivate a reviewed version", func(t *testing.T) {
		r := newPGEnforcementRig(t)
		protectReviewRig(t, r, DefaultReviewPolicySettings())
		row := approveRigReview(t, r, r.run(t), "reviewer")
		published, err := publishRigReview(r, row)
		require.NoError(t, err)
		reason := "revert for the test"
		_, err = r.api.RollbackDeviceProfileWithReason(r.ctx, "profile", published.Version-1, &reason)
		require.NoError(t, err)
		tx := r.api.RDB.DB(r.ctx).Begin()
		require.NoError(t, tx.Error)
		defer tx.Rollback()
		res := tx.Exec(`UPDATE "device-management".device_profiles SET active_version=? WHERE id=?`, published.Version, r.profile.ID)
		pgGuardCode(t, res.Error, "DCR01")
		require.NoError(t, tx.Rollback().Error)
		r.api.skipReviewGuardForOldPodTest = true
		_, err = r.api.RollbackDeviceProfile(r.ctx, "profile", published.Version)
		reviewErrorCode(t, err, "REVIEW_REQUIRED")
		r.api.skipReviewGuardForOldPodTest = false
		var stored DeviceProfile
		require.NoError(t, r.api.RDB.DB(r.ctx).First(&stored, r.profile.ID).Error)
		require.EqualValues(t, published.Version-1, stored.ActiveVersion.Int32)
	})
	t.Run("forged txid", func(t *testing.T) {
		r := newPGEnforcementRig(t)
		protectReviewRig(t, r, DefaultReviewPolicySettings())
		tx := r.api.RDB.DB(r.ctx).Begin()
		require.NoError(t, tx.Error)
		defer tx.Rollback()
		res := tx.Exec(`INSERT INTO "device-management".automation_review_break_glass
			(tenant_id,device_profile_id,action,target,reason,actor,amr,txid)
			VALUES ('acme',?,'ROLLBACK','1','forged','forger','[]',txid_current()-1)`, r.profile.ID)
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
		pgGuardCode(t, tx.Exec(`UPDATE "device-management".device_profiles SET active_since=clock_timestamp() WHERE id=?`, r.profile.ID).Error, "DCR01")
		require.NoError(t, tx.Rollback().Error)
		var count int64
		require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AutomationReviewBreakGlass{}).Count(&count).Error)
		require.Zero(t, count)
	})
}

// This is the entire pre-enforcement version row: no automation_review_token.
type previousReleaseProfileVersion struct {
	gorm.Model
	rdb.TenantScoped
	DeviceProfileId uint
	Version         int32
	Label           sql.NullString
	Description     sql.NullString
	Snapshot        datatypes.JSON
	PublishedBy     string
}

func (previousReleaseProfileVersion) TableName() string {
	return "device-management.device_profile_versions"
}

func TestPGPreviousReleaseCodeOnProtectedProfile(t *testing.T) {
	for _, protected := range []bool{true, false} {
		for _, action := range []string{"publish", "rollback", "delete", "re-point type", "re-type device"} {
			t.Run(fmt.Sprintf("protected=%t/%s", protected, action), func(t *testing.T) {
				r := newPGEnforcementRig(t)
				_, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "before")
				require.NoError(t, err)
				var bound, other *DeviceType
				var device *Device
				if action == "re-point type" || action == "re-type device" {
					bound, other, device = pgEnforcementBindings(t, r)
				}
				if protected {
					protectReviewRig(t, r, DefaultReviewPolicySettings())
				}
				r.api.skipReviewGuardForOldPodTest = true
				before, err := r.api.deviceProfileByToken(r.ctx, "profile")
				require.NoError(t, err)
				var auditBefore int64
				require.NoError(t, r.api.RDB.DB(r.ctx).Model(&rdb.AuditEvent{}).Count(&auditBefore).Error)
				code := "PROFILE_PROTECTED"
				switch action {
				case "publish":
					code = "REVIEW_REQUIRED"
					_, err = r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "old-pod")
				case "rollback":
					code = "REVIEW_REQUIRED"
					_, err = r.api.RollbackDeviceProfile(r.ctx, "profile", 1)
				case "delete":
					var deleted bool
					deleted, err = r.api.DeleteDeviceProfile(r.ctx, "profile")
					require.Equal(t, !protected, deleted)
				case "re-point type":
					_, err = r.api.UpdateDeviceType(r.ctx, "bound", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalString{Set: true}})
				case "re-type device":
					_, err = r.api.UpdateDevice(r.ctx, "device", &DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalStringOf("other")})
				}
				if protected {
					reviewErrorCode(t, err, code)
					var after DeviceProfile
					require.NoError(t, r.api.RDB.DB(r.ctx).Unscoped().First(&after, r.profile.ID).Error)
					require.Equal(t, before.ActiveVersion, after.ActiveVersion)
					require.Equal(t, before.ActiveSince, after.ActiveSince)
					require.Equal(t, before.DeletedAt, after.DeletedAt)
					require.EqualValues(t, 2, pgVersionCount(t, r))
					var auditAfter int64
					require.NoError(t, r.api.RDB.DB(r.ctx).Model(&rdb.AuditEvent{}).Count(&auditAfter).Error)
					require.Equal(t, auditBefore, auditAfter, "refused transaction must roll back its journal too")
				} else {
					require.NoError(t, err)
					wantCount := int64(2)
					if action == "publish" {
						wantCount = 3
					} else if action == "delete" {
						wantCount = 0
					}
					require.Equal(t, wantCount, pgVersionCount(t, r))
					if action == "delete" {
						var n int64
						require.NoError(t, r.api.RDB.DB(r.ctx).Unscoped().Model(&DeviceProfile{}).Where("id = ?", r.profile.ID).Count(&n).Error)
						require.Zero(t, n)
					} else {
						p, err := r.api.deviceProfileByToken(r.ctx, "profile")
						require.NoError(t, err)
						wantActive := int32(2)
						if action == "publish" {
							wantActive = 3
						} else if action == "rollback" {
							wantActive = 1
						}
						require.Equal(t, wantActive, p.ActiveVersion.Int32)
					}
				}
				if bound != nil {
					var storedType DeviceType
					var storedDevice Device
					require.NoError(t, r.api.RDB.DB(r.ctx).First(&storedType, bound.ID).Error)
					require.NoError(t, r.api.RDB.DB(r.ctx).First(&storedDevice, device.ID).Error)
					if action == "re-point type" && !protected {
						require.Nil(t, storedType.ProfileId)
					} else {
						require.Equal(t, bound.ProfileId, storedType.ProfileId)
					}
					wantType := bound.ID
					if action == "re-type device" && !protected {
						wantType = other.ID
					}
					require.Equal(t, wantType, storedDevice.DeviceTypeId)
				}
			})
		}
	}
	t.Run("previous release row shape", func(t *testing.T) {
		r := newPGEnforcementRig(t)
		protectReviewRig(t, r, DefaultReviewPolicySettings())
		row := pgEnforcementApproved(t, r)
		_, err := publishRigReview(r, row)
		require.NoError(t, err)
		var oldRows []previousReleaseProfileVersion
		var newRows []DeviceProfileVersion
		require.NoError(t, r.api.RDB.DB(r.ctx).Order("id").Find(&oldRows).Error)
		require.NoError(t, r.api.RDB.DB(r.ctx).Order("id").Find(&newRows).Error)
		require.Len(t, oldRows, 2)
		require.Len(t, newRows, len(oldRows))
		for i, old := range oldRows {
			current := newRows[i]
			require.Equal(t, current.Model, old.Model)
			require.Equal(t, current.TenantScoped, old.TenantScoped)
			require.Equal(t, current.DeviceProfileId, old.DeviceProfileId)
			require.Equal(t, current.Version, old.Version)
			require.Equal(t, current.Label, old.Label)
			require.Equal(t, current.Description, old.Description)
			require.JSONEq(t, string(current.Snapshot), string(old.Snapshot))
			require.Equal(t, current.PublishedBy, old.PublishedBy)
		}
		p := seedProfileWithRule(t, r.api, r.ctx, "unprotected", "r2", false)
		old := &previousReleaseProfileVersion{DeviceProfileId: p.ID, Version: 1, Snapshot: oldRows[0].Snapshot, PublishedBy: "old-pod"}
		require.NoError(t, r.api.RDB.DB(r.ctx).Create(old).Error)
		var inserted DeviceProfileVersion
		require.NoError(t, r.api.RDB.DB(r.ctx).First(&inserted, old.ID).Error)
		require.Equal(t, p.ID, inserted.DeviceProfileId)
		require.EqualValues(t, 1, inserted.Version)
		require.Equal(t, "old-pod", inserted.PublishedBy)
		require.Nil(t, inserted.AutomationReviewToken)
		require.JSONEq(t, string(old.Snapshot), string(inserted.Snapshot))
	})
}

func TestPGBreakGlassRollback(t *testing.T) {
	r := newPGEnforcementRig(t)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	row := pgEnforcementApproved(t, r)
	before := pgVersionCount(t, r)
	_, err := r.api.RollbackDeviceProfile(r.ctx, "profile", 1)
	reviewErrorCode(t, err, "BREAK_GLASS_REASON_REQUIRED")
	reason := "restore safe operation"
	_, err = r.api.RollbackDeviceProfileWithReason(withoutReviewMFA("operator"), "profile", 1, &reason)
	reviewErrorCode(t, err, actiongate.CodeMfaRequired)
	var count int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AutomationReviewBreakGlass{}).Count(&count).Error)
	require.Zero(t, count)
	ctx, _ := reviewIdentity("operator")
	p, err := r.api.RollbackDeviceProfileWithReason(ctx, "profile", 1, &reason)
	require.NoError(t, err)
	require.EqualValues(t, 1, p.ActiveVersion.Int32)
	var rows []AutomationReviewBreakGlass
	require.NoError(t, r.api.RDB.DB(ctx).Find(&rows).Error)
	require.Len(t, rows, 1)
	bg := rows[0]
	require.Equal(t, "ROLLBACK", bg.Action)
	require.Equal(t, "1", bg.Target)
	require.Equal(t, reason, bg.Reason)
	require.Equal(t, "operator", bg.Actor)
	require.Positive(t, bg.Txid)
	var txidPresent bool
	require.NoError(t, r.api.RDB.DB(ctx).Raw(`SELECT txid IS NOT NULL FROM "device-management".automation_review_break_glass WHERE id=?`, bg.ID).Scan(&txidPresent).Error)
	require.True(t, txidPresent)
	table, err := journalTable(r.api.RDB.DB(ctx), &AutomationReviewBreakGlass{})
	require.NoError(t, err)
	var events []rdb.AuditEvent
	require.NoError(t, r.api.RDB.DB(ctx).Where("table_name = ? AND entity_pk = ? AND operation = ?", table, strconv.FormatUint(uint64(bg.ID), 10), "create").Find(&events).Error)
	require.Len(t, events, 1)
	require.Equal(t, "operator", events[0].Actor)
	require.Equal(t, "acme", events[0].TenantId)
	require.EqualValues(t, 1, events[0].RowsAffected)
	_, err = publishRigReview(r, row)
	reviewErrorCode(t, err, "REVIEW_STALE_BASELINE_CHANGED")
	pgApprovedUnchanged(t, r, row, before)
}

func TestPGLooseningIsPending(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow self approval=%t", delayed), func(t *testing.T) {
			r := newPGEnforcementRig(t)
			settings := DefaultReviewPolicySettings()
			settings.AllowSelfApproval = delayed
			protectReviewRig(t, r, settings)
			want := settings
			want.Required = false
			before := reviewNow()
			view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
			require.NoError(t, err)
			require.False(t, applied)
			require.True(t, view.Effective.Required)
			require.NotNil(t, view.Pending)
			pending := view.Pending
			require.Equal(t, "PENDING", pending.State)
			pgRawInsertProbe(t, r, "DCR01")
			if delayed {
				require.True(t, pending.EffectiveAt.Valid)
				require.WithinDuration(t, before.Add(24*time.Hour), pending.EffectiveAt.Time, time.Second)
				oldNow := reviewNow
				reviewNow = func() time.Time { return pending.EffectiveAt.Time.Add(time.Second) }
				t.Cleanup(func() { reviewNow = oldNow })
				v, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "publisher")
				require.NoError(t, err)
				require.EqualValues(t, 2, v.Version)
				require.Nil(t, v.AutomationReviewToken)
			} else {
				require.False(t, pending.EffectiveAt.Valid)
				_, err := r.api.ApproveAutomationReviewPolicyChange(withoutReviewMFA("second"), pending.Token)
				reviewErrorCode(t, err, actiongate.CodeMfaRequired)
				ctx, _ := reviewIdentity("second")
				view, err = r.api.ApproveAutomationReviewPolicyChange(ctx, pending.Token)
				require.NoError(t, err)
				require.False(t, view.Effective.Required)
				require.Nil(t, view.Pending)
			}
			var change AutomationReviewPolicyChange
			require.NoError(t, r.api.RDB.DB(r.ctx).First(&change, pending.ID).Error)
			require.Equal(t, "APPLIED", change.State)
			require.True(t, change.DecidedBy.Valid)
			actor := "second"
			if delayed {
				actor = "delay"
			}
			require.Equal(t, actor, change.DecidedBy.String)
			pgRawInsertProbe(t, r, "")
			view, err = r.api.AutomationReviewPolicyFor(r.ctx, "profile")
			require.NoError(t, err)
			require.False(t, view.Stored.Required)
			require.Nil(t, view.Pending)
		})
	}
}

func TestPGPublishedVersionTracesToReview(t *testing.T) {
	r := newPGEnforcementRig(t)
	policy := protectReviewRig(t, r, DefaultReviewPolicySettings())
	row := pgEnforcementApproved(t, r)
	v, err := publishRigReview(r, row)
	require.NoError(t, err)
	var version DeviceProfileVersion
	require.NoError(t, r.api.RDB.DB(r.ctx).First(&version, v.ID).Error)
	require.NotNil(t, version.AutomationReviewToken)
	require.Equal(t, row.Token, *version.AutomationReviewToken)
	trace, err := r.api.AutomationReviewByToken(r.ctx, *version.AutomationReviewToken)
	require.NoError(t, err)
	require.Equal(t, ReviewStateConsumed, trace.State)
	require.NotNil(t, trace.ConsumedVersion)
	require.Equal(t, version.Version, *trace.ConsumedVersion)
	require.NotNil(t, trace.ConsumedTxid)
	var approvals []ReviewApproval
	require.NoError(t, json.Unmarshal(trace.Approvals, &approvals))
	require.Len(t, approvals, 1)
	require.Equal(t, "reviewer", approvals[0].Sub)
	require.EqualValues(t, policy.Stored.Revision, approvals[0].PolicyRevision)
	require.JSONEq(t, string(row.Approvals), string(trace.Approvals))
	for _, pair := range [][2]datatypes.JSON{{row.Expectations, trace.Expectations}, {row.StreamBounds, trace.StreamBounds}, {row.Scope, trace.Scope}} {
		require.NotEmpty(t, pair[1])
		require.JSONEq(t, string(pair[0]), string(pair[1]))
	}
	var expectations []ReviewExpectation
	require.NoError(t, json.Unmarshal(trace.Expectations, &expectations))
	require.Equal(t, reviewRunInput().Expectations, expectations)
	require.NotEmpty(t, trace.InputDigest)
	require.Equal(t, row.InputDigest, trace.InputDigest)
	require.NotEmpty(t, trace.Result)
	require.Equal(t, row.Result, trace.Result)
	require.Equal(t, row.ResultDigest, trace.ResultDigest)
	require.Equal(t, row.ReviewDigest, trace.ReviewDigest)
	digest, err := SnapshotDigest(version.Snapshot)
	require.NoError(t, err)
	require.Equal(t, trace.DraftDigest, digest)
}

func TestPGJournalBoundaryWaitsForInFlightEdit(t *testing.T) {
	r := newPGEnforcementRig(t)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	row := pgEnforcementApproved(t, r)
	before := pgVersionCount(t, r)
	ctx, _ := reviewIdentity("in-flight-author")
	tx := r.api.RDB.DB(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	var blocker int
	require.NoError(t, tx.Raw("SELECT pg_backend_pid()").Scan(&blocker).Error)
	res := tx.Model(&DetectionRule{}).Where("id = ?", r.rule.ID).
		Update("definition", datatypes.JSON(`{"metric":"temp_c","threshold":24}`))
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	table, err := journalTable(tx, &DetectionRule{})
	require.NoError(t, err)
	var journalCount int64
	require.NoError(t, tx.Model(&rdb.AuditEvent{}).Where("table_name = ? AND actor = ? AND operation = ?", table, "in-flight-author", "update").Count(&journalCount).Error)
	require.EqualValues(t, 1, journalCount)
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := publishRigReview(r, row)
		done <- err
	}()
	<-started
	pgWaitBlockedBy(t, r, blocker)
	select {
	case err := <-done:
		t.Fatalf("publish did not wait for the journal transaction: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, tx.Commit().Error)
	reviewErrorCode(t, pgReceive(t, done), "REVIEW_STALE_DRAFT_CHANGED")
	pgApprovedUnchanged(t, r, row, before)
	next := pgEnforcementRun(t, r)
	var authors []string
	require.NoError(t, json.Unmarshal(next.DraftAuthors, &authors))
	require.Contains(t, authors, "in-flight-author")
	require.NotEqual(t, row.DraftDigest, next.DraftDigest)
}

func TestPGDeleteKeepsConsumedReviews(t *testing.T) {
	r := newPGEnforcementRig(t)
	pgEnforcementBindings(t, r)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	deleted, err := r.api.DeleteDeviceProfile(r.ctx, "profile")
	reviewErrorCode(t, err, "PROFILE_PROTECTED")
	require.False(t, deleted)
	consumed := pgEnforcementApproved(t, r)
	_, err = publishRigReview(r, consumed)
	require.NoError(t, err)
	ran := pgEnforcementRun(t, r)
	require.Equal(t, ReviewStateRan, ran.State)
	reason := "restore baseline before retirement"
	_, err = r.api.RollbackDeviceProfileWithReason(r.ctx, "profile", 1, &reason)
	require.NoError(t, err)
	var history []AutomationReviewBreakGlass
	require.NoError(t, r.api.RDB.DB(r.ctx).Order("id").Find(&history).Error)
	require.Len(t, history, 1)
	view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", DefaultReviewPolicySettings())
	require.NoError(t, err)
	require.False(t, applied)
	require.NotNil(t, view.Pending)
	ctx, _ := reviewIdentity("second")
	view, err = r.api.ApproveAutomationReviewPolicyChange(ctx, view.Pending.Token)
	require.NoError(t, err)
	require.False(t, view.Effective.Required)
	bound, err := r.api.UpdateDeviceType(r.ctx, "bound", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalString{Set: true}})
	require.NoError(t, err)
	require.Nil(t, bound.ProfileId)
	deleted, err = r.api.DeleteDeviceProfile(r.ctx, "profile")
	require.NoError(t, err)
	require.True(t, deleted)
	retained, err := r.api.AutomationReviewByToken(r.ctx, consumed.Token)
	require.NoError(t, err)
	require.Equal(t, ReviewStateConsumed, retained.State)
	require.NotNil(t, retained.ConsumedTxid)
	require.NotNil(t, retained.ConsumedVersion)
	var after []AutomationReviewBreakGlass
	require.NoError(t, r.api.RDB.DB(r.ctx).Order("id").Find(&after).Error)
	require.Equal(t, history, after)
	for _, model := range []any{&AutomationReviewPolicy{}, &AutomationReviewPolicyChange{}, &DeviceProfileVersion{}, &DeviceProfile{}} {
		var n int64
		db := r.api.RDB.DB(r.ctx).Unscoped().Model(model)
		if _, ok := model.(*DeviceProfile); ok {
			db = db.Where("id = ?", r.profile.ID)
		} else {
			db = db.Where("device_profile_id = ?", r.profile.ID)
		}
		require.NoError(t, db.Count(&n).Error)
		require.Zero(t, n)
	}
	var reviews []AutomationReview
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("device_profile_id = ?", r.profile.ID).Find(&reviews).Error)
	require.Len(t, reviews, 1)
	require.Equal(t, consumed.Token, reviews[0].Token)
	require.NotEqual(t, ran.Token, reviews[0].Token)
}

func TestPGPreviousReleaseDeleteWithPolicyHistory(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(fmt.Sprintf("protected=%t", protected), func(t *testing.T) {
			r := newPGEnforcementRig(t)
			policy := &AutomationReviewPolicy{DeviceProfileId: r.profile.ID, Required: protected, UpdatedBy: "operator"}
			require.NoError(t, r.api.RDB.DB(r.ctx).Create(policy).Error)
			change := &AutomationReviewPolicyChange{DeviceProfileId: r.profile.ID, Token: "pending-delete-test",
				BaseRevision: policy.Revision, Proposed: datatypes.JSON(`{"required":false}`), ProposedBy: "operator", State: "PENDING"}
			require.NoError(t, r.api.RDB.DB(r.ctx).Create(change).Error)
			// Read back PostgreSQL's timestamp/JSON representation before comparing.
			require.NoError(t, r.api.RDB.DB(r.ctx).First(policy, policy.ID).Error)
			require.NoError(t, r.api.RDB.DB(r.ctx).First(change, change.ID).Error)
			versions := pgVersionCount(t, r)
			r.api.skipReviewGuardForOldPodTest = true
			deleted, err := r.api.DeleteDeviceProfile(r.ctx, "profile")
			if protected {
				reviewErrorCode(t, err, "PROFILE_PROTECTED")
				require.False(t, deleted)
				var storedPolicy AutomationReviewPolicy
				var storedChange AutomationReviewPolicyChange
				require.NoError(t, r.api.RDB.DB(r.ctx).First(&storedPolicy, policy.ID).Error)
				require.NoError(t, r.api.RDB.DB(r.ctx).First(&storedChange, change.ID).Error)
				require.Equal(t, *policy, storedPolicy)
				require.Equal(t, *change, storedChange)
				require.Equal(t, versions, pgVersionCount(t, r))
			} else {
				require.NoError(t, err)
				require.True(t, deleted)
			}
			for _, model := range []any{&DeviceProfile{}, &AutomationReviewPolicy{}, &AutomationReviewPolicyChange{}} {
				query := r.api.RDB.DB(r.ctx).Unscoped().Model(model)
				if _, ok := model.(*DeviceProfile); ok {
					query = query.Where("id = ?", r.profile.ID)
				} else {
					query = query.Where("device_profile_id = ?", r.profile.ID)
				}
				var count int64
				require.NoError(t, query.Count(&count).Error)
				if protected {
					require.EqualValues(t, 1, count)
				} else {
					require.Zero(t, count)
				}
			}
		})
	}
}

func TestPGProtectedPublishWaitsForRejectBeforeJournalLock(t *testing.T) {
	r := newPGEnforcementRig(t)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	row := pgEnforcementApproved(t, r)
	before := pgVersionCount(t, r)
	ctx, _ := reviewIdentity("rejector")
	tx := r.api.RDB.DB(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	var blocker int
	require.NoError(t, tx.Raw("SELECT pg_backend_pid()").Scan(&blocker).Error)
	// Hold just the review row first; the journaled part of rejection follows later.
	require.NoError(t, tx.Exec(`UPDATE "device-management".automation_reviews
		SET state=?, revision=revision+1 WHERE id=?`, ReviewStateRejected, row.ID).Error)
	done := make(chan error, 1)
	go func() {
		_, err := publishRigReview(r, row)
		done <- err
	}()
	pgWaitBlockedBy(t, r, blocker)
	time.Sleep(300 * time.Millisecond)
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	require.NoError(t, tx.WithContext(writeCtx).Model(row).Update("decided_note", "rejected concurrently").Error,
		"reject must journal without waiting for the publish's journal lock")
	table, err := journalTable(tx, &AutomationReview{})
	require.NoError(t, err)
	var journalCount int64
	require.NoError(t, tx.Model(&rdb.AuditEvent{}).Where("table_name = ? AND actor = ? AND operation = ?", table, "rejector", "update").Count(&journalCount).Error)
	require.EqualValues(t, 1, journalCount)
	require.NoError(t, tx.Commit().Error)
	reviewErrorCode(t, pgReceive(t, done), "REVIEW_NOT_APPROVED")
	require.Equal(t, before, pgVersionCount(t, r))
	stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
	require.NoError(t, err)
	require.Equal(t, ReviewStateRejected, stored.State)
	require.Equal(t, row.Revision+1, stored.Revision)
}
