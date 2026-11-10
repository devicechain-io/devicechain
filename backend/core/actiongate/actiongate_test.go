// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// agentClaims is an agent's access token as the OAuth mint issues it for act:alarms.
func agentClaims() *auth.Claims {
	return &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "ana@example.invalid",
		ClientId: "agent-desktop", Scope: "act:alarms", SessionID: "sid-1",
		Authorities: []string{string(auth.AlarmPropose), string(auth.AlarmRead)}}
}

// personClaims is a person's console token holding alarm:write, signed in with a TOTP code at mfa.
func personClaims(mfa time.Time) *auth.Claims {
	return &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@example.invalid",
		Authorities: []string{string(auth.AlarmWrite)}, Amr: auth.AmrPasswordOTP(),
		MfaTime: jwt.NewNumericDate(mfa)}
}

func with(c *auth.Claims) context.Context { return auth.WithClaims(context.Background(), c) }

// The gate refuses a service token outright, whatever authority it carries: no service token on
// any agent path.
func TestServiceTokenRefusedByGate(t *testing.T) {
	c := agentClaims()
	c.TokenType = auth.TokenTypeService
	_, err := AuthorizeProposer(with(c), auth.AlarmPropose)
	require.True(t, IsCode(err, CodeServiceTokenRefused), "got %v", err)
	require.ErrorIs(t, err, auth.ErrForbidden)

	_, err = AuthorizeApprover(with(&auth.Claims{TokenType: auth.TokenTypeService, Tenant: "acme", Username: "svc",
		Authorities: []string{"*"}}), auth.AlarmWrite)
	require.True(t, IsCode(err, CodeServiceTokenRefused), "got %v", err)

	p, err := AuthorizeProposer(with(agentClaims()), auth.AlarmPropose)
	require.NoError(t, err, "control: an agent's access token proposes")
	require.Equal(t, Proposer{Tenant: "acme", Principal: "ana@example.invalid", Client: "agent-desktop", Sid: "sid-1"}, p)
}

// A person's console token may not propose (AGENT_ONLY), whatever it holds, and "*" never stands in
// for a proposal authority on an agent's token either.
func TestProposalPathIsAgentOnly(t *testing.T) {
	admin := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@example.invalid", Authorities: []string{"*"}}
	_, err := AuthorizeProposer(with(admin), auth.AlarmPropose)
	require.True(t, IsCode(err, CodeAgentOnly), "got %v", err)

	star := agentClaims()
	star.Authorities = []string{"*"}
	_, err = AuthorizeProposer(with(star), auth.AlarmPropose)
	require.ErrorIs(t, err, auth.ErrForbidden)

	other := agentClaims()
	other.Authorities = []string{string(auth.CommandPropose)}
	_, err = AuthorizeProposer(with(other), auth.AlarmPropose)
	require.ErrorIs(t, err, auth.ErrForbidden, "command:propose does not propose an alarm action")

	scoped := agentClaims()
	scoped.ScopeAnchors = []string{"customer:c1"}
	_, err = AuthorizeProposer(with(scoped), auth.AlarmPropose)
	require.True(t, IsCode(err, CodeScopedNotSupported), "got %v", err)

	refresh := agentClaims()
	refresh.TokenType = auth.TokenTypeRefresh
	_, err = AuthorizeProposer(with(refresh), auth.AlarmPropose)
	require.ErrorIs(t, err, auth.ErrForbidden)

	_, err = AuthorizeProposer(context.Background(), auth.AlarmPropose)
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}

// An agent can never approve: any agent-origin token (client id, scope, audience, a proposal
// authority) is refused, even one holding the raw write.
func TestApprovalRefusesAgentOriginToken(t *testing.T) {
	now := time.Now()
	for name, mutate := range map[string]func(*auth.Claims){
		"client id":           func(c *auth.Claims) { c.ClientId = "agent-desktop" },
		"scope":               func(c *auth.Claims) { c.Scope = "read-only" },
		"audience":            func(c *auth.Claims) { c.Audience = jwt.ClaimStrings{"https://mcp.example.invalid"} },
		"proposal authority":  func(c *auth.Claims) { c.Authorities = append(c.Authorities, string(auth.AlarmPropose)) },
		"identity token type": func(c *auth.Claims) { c.TokenType = auth.TokenTypeIdentity },
	} {
		c := personClaims(now)
		mutate(c)
		_, err := AuthorizeApprover(with(c), auth.AlarmWrite)
		require.True(t, IsCode(err, CodeApproverIsAgent), "%s: got %v", name, err)
	}
	_, err := AuthorizeApprover(with(personClaims(now)), auth.AlarmWrite)
	require.NoError(t, err, "control: a person's console token holding the write passes")

	noWrite := personClaims(now)
	noWrite.Authorities = []string{string(auth.AlarmRead)}
	_, err = AuthorizeApprover(with(noWrite), auth.AlarmWrite)
	require.ErrorIs(t, err, auth.ErrForbidden)
}

// An approval needs a sign-in that presented a second factor.
func TestApprovalRefusesWithoutMfaAmr(t *testing.T) {
	c := personClaims(time.Now())
	c.Amr, c.MfaTime = auth.AmrPasswordOnly(), nil
	a, err := AuthorizeApprover(with(c), auth.AlarmWrite)
	require.NoError(t, err)
	err = CheckStepUp(a, auth.StepUp{MaxAge: 15 * time.Minute}, time.Now())
	require.True(t, IsCode(err, CodeMfaRequired), "got %v", err)
}

// Freshness is mfa_time and nothing else: a token issued a second ago from a factor verified an
// hour ago is stale, and a recovery-code sign-in counts only when the policy says so.
func TestApprovalRefusesStaleAuthTime(t *testing.T) {
	now := time.Now()
	su := auth.StepUp{MaxAge: 15 * time.Minute}
	stale := personClaims(now.Add(-time.Hour))
	stale.IssuedAt = jwt.NewNumericDate(now.Add(-time.Second))
	a, err := AuthorizeApprover(with(stale), auth.AlarmWrite)
	require.NoError(t, err)
	require.True(t, IsCode(CheckStepUp(a, su, now), CodeStepUpRequired))

	fresh, err := AuthorizeApprover(with(personClaims(now.Add(-time.Minute))), auth.AlarmWrite)
	require.NoError(t, err)
	require.NoError(t, CheckStepUp(fresh, su, now), "control: a factor verified a minute ago is fresh")

	rc := personClaims(now.Add(-time.Minute))
	rc.Amr = auth.AmrPasswordRecovery()
	recovery, err := AuthorizeApprover(with(rc), auth.AlarmWrite)
	require.NoError(t, err)
	require.True(t, IsCode(CheckStepUp(recovery, su, now), CodeStepUpRequired), "a recovery code is not a step-up by default")
	require.NoError(t, CheckStepUp(recovery, auth.StepUp{MaxAge: su.MaxAge, AcceptRecoveryCodes: true}, now))
}

// The canonical form depends on meaning, not encoding, and the digest binds the kind, the payload
// and the targets with no way for one part's bytes to be read as another's.
func TestDigestBindsKindPayloadAndTargets(t *testing.T) {
	a, err := Canonical(map[string]any{"b": []string{"x"}, "a": 1})
	require.NoError(t, err)
	b, err := Canonical(map[string]any{"a": 1, "b": []string{"x"}})
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
	require.Equal(t, `{"a":1,"b":["x"]}`, string(a))

	base := Digest(KindAlarmAck, []byte(`{"p":1}`), []byte(`["t"]`))
	require.True(t, strings.HasPrefix(base, "sha256:"))
	for name, d := range map[string]string{
		"kind":     Digest(KindAlarmClear, []byte(`{"p":1}`), []byte(`["t"]`)),
		"payload":  Digest(KindAlarmAck, []byte(`{"p":2}`), []byte(`["t"]`)),
		"targets":  Digest(KindAlarmAck, []byte(`{"p":1}`), []byte(`["u"]`)),
		"boundary": Digest(KindAlarmAck, []byte(`{"p":1}["t`), []byte(`"]`)),
	} {
		require.NotEqual(t, base, d, name)
	}
	require.True(t, DigestEqual(base, Digest(KindAlarmAck, []byte(`{"p":1}`), []byte(`["t"]`))))
	require.False(t, DigestEqual("", ""), "an empty digest never matches")
	require.False(t, DigestEqual(base, base[:len(base)-1]))
}

// Nothing ever returns to PROPOSED, and a terminal state never moves.
func TestStateMachineIsOneStepDeep(t *testing.T) {
	for _, to := range States {
		require.Equal(t, to != StateProposed, CanTransition(StateProposed, to), "PROPOSED -> %s", to)
		for _, from := range States {
			if from != StateProposed {
				require.False(t, CanTransition(from, to), "%s -> %s", from, to)
			}
		}
	}
	require.False(t, CanTransition(StateProposed, "BOGUS"))
}

// proposal is a test area's ledger model.
type proposal struct {
	gorm.Model
	rdb.TenantScoped
	Record
}

func ledgerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&proposal{}, &rdb.AuditEvent{}))
	require.NoError(t, rdb.RegisterAuditJournal(db))
	return db
}

func seed(t *testing.T, db *gorm.DB, ctx context.Context, token string, expires time.Time) {
	t.Helper()
	require.NoError(t, db.WithContext(ctx).Create(&proposal{Record: Record{
		Token: token, RequestId: token, EffectToken: "e-" + token, Kind: string(KindAlarmAck), State: string(StateProposed),
		Payload: datatypes.JSON(`{}`), TargetSet: datatypes.JSON(`[]`), Dependencies: datatypes.JSON(`{}`),
		Digest: "sha256:x", Impact: "NORMAL", ProposerSub: "ana", ProposerClient: "c", Approvals: datatypes.JSON(`[]`),
		Reasons: datatypes.JSON(`[]`), ExpiresAt: expires, RequiredApprovals: 1,
	}}).Error)
}

func tenantCtx(tenant string) context.Context {
	return core.WithTenant(auth.WithClaims(context.Background(), &auth.Claims{Tenant: tenant, Username: "boss"}), tenant)
}

// Every transition is conditional on the state it was read in and on the expiry, and leaves
// exactly one journal row.
func TestTransitionIsConditionalAndJournaled(t *testing.T) {
	db := ledgerDB(t)
	ctx := tenantCtx("acme")
	now := time.Now()
	seed(t, db, ctx, "p1", now.Add(time.Hour))
	seed(t, db, ctx, "p2", now.Add(-time.Minute))

	var before int64
	require.NoError(t, db.WithContext(core.WithSystemContext(ctx)).Model(&rdb.AuditEvent{}).Count(&before).Error)
	require.NoError(t, Transition(db.WithContext(ctx), &proposal{}, "p1", StateProposed, StateRejected, now, map[string]any{"decided_by": "boss"}))
	var after int64
	require.NoError(t, db.WithContext(core.WithSystemContext(ctx)).Model(&rdb.AuditEvent{}).Count(&after).Error)
	require.Equal(t, before+1, after, "a transition leaves exactly one journal row")

	err := Transition(db.WithContext(ctx), &proposal{}, "p1", StateProposed, StateRefused, now, nil)
	require.ErrorIs(t, err, ErrNotPending, "a second transition of the same row is refused")
	err = Transition(db.WithContext(ctx), &proposal{}, "p2", StateProposed, StateRejected, now, nil)
	require.ErrorIs(t, err, ErrNotPending, "an expired proposal cannot be decided")
	require.Error(t, Transition(db.WithContext(ctx), &proposal{}, "p1", StateRejected, StateProposed, now, nil))

	// Another tenant cannot move this tenant's row.
	seed(t, db, ctx, "p3", now.Add(time.Hour))
	err = Transition(db.WithContext(tenantCtx("other")), &proposal{}, "p3", StateProposed, StateRejected, now, nil)
	require.ErrorIs(t, err, ErrNotPending)

	var got proposal
	require.NoError(t, db.WithContext(ctx).Where("token = ?", "p1").First(&got).Error)
	require.Equal(t, string(StateRejected), got.State)

	n, err := ExpireDue(core.WithSystemContext(context.Background()), db, &proposal{}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "only the overdue PROPOSED row expires")
	var expired proposal
	require.NoError(t, db.WithContext(ctx).Where("token = ?", "p2").First(&expired).Error)
	require.Equal(t, string(StateExpired), expired.State)
}

// An approval is a compare-and-set on the revision: a writer that read an older revision lands
// nothing.
func TestRecordApprovalIsCompareAndSet(t *testing.T) {
	db := ledgerDB(t)
	ctx := tenantCtx("acme")
	now := time.Now()
	seed(t, db, ctx, "p1", now.Add(time.Hour))
	first := []Approval{{Sub: "boss", At: now}}
	require.NoError(t, RecordApproval(db.WithContext(ctx), &proposal{}, "p1", 0, first, 2, now))
	require.ErrorIs(t, RecordApproval(db.WithContext(ctx), &proposal{}, "p1", 0, []Approval{{Sub: "other", At: now}}, 2, now), ErrNotPending)
	var got proposal
	require.NoError(t, db.WithContext(ctx).Where("token = ?", "p1").First(&got).Error)
	approvals, err := ParseApprovals(got.Approvals)
	require.NoError(t, err)
	require.Equal(t, []string{"boss"}, Principals(approvals))
	require.Equal(t, 1, got.Revision)
	require.Equal(t, 2, got.RequiredApprovals)

	_, err = ParseApprovals(nil)
	require.Error(t, err, "missing approvals are an error, never an empty list")
}

// Every counted approval is re-validated; the proposer never counts toward two approvals.
func TestTallyRevalidatesEveryApprover(t *testing.T) {
	now := time.Now()
	approvals := []Approval{{Sub: "a", At: now, Amr: auth.AmrPasswordOTP(), MfaTime: &now}, {Sub: "b", At: now, Amr: auth.AmrPasswordOTP(), MfaTime: &now}, {Sub: "a", At: now, Amr: auth.AmrPasswordOTP(), MfaTime: &now}, {Sub: "ana", At: now, Amr: auth.AmrPasswordOTP(), MfaTime: &now}}
	require.Equal(t, 2, Tally(approvals, []string{"a", "b"}, "ana", 2, true, auth.StepUp{MaxAge: time.Minute}, now))
	require.Equal(t, 1, Tally(approvals, []string{"b"}, "ana", 2, true, auth.StepUp{MaxAge: time.Minute}, now), "a revoked first approver no longer counts")
	require.Equal(t, 2, Tally(approvals, []string{"b", "ana", "a"}, "ana", 2, true, auth.StepUp{MaxAge: time.Minute}, now), "the proposer never counts toward two")
	require.Equal(t, 1, Tally([]Approval{{Sub: "ana", Amr: auth.AmrPasswordOTP(), MfaTime: &now}}, []string{"ana"}, "ana", 1, true, auth.StepUp{MaxAge: time.Minute}, now), "self-confirm of a single approval")
	require.Equal(t, 0, Tally([]Approval{{Sub: "ana", Amr: auth.AmrPasswordOTP(), MfaTime: &now}}, []string{"ana"}, "ana", 1, false, auth.StepUp{MaxAge: time.Minute}, now), "self-confirm forbidden by policy")
}

// The policy can narrow the platform rate and never widen it; each limit is checked.
func TestLimitsNeverWidenThePlatformCeiling(t *testing.T) {
	require.Equal(t, PlatformActionsPerHour, LimitsFor(0).ActionsPerHour)
	require.Equal(t, PlatformActionsPerHour, LimitsFor(PlatformActionsPerHour*10).ActionsPerHour)
	require.Equal(t, 5, LimitsFor(5).ActionsPerHour)
	l := LimitsFor(5)
	require.Equal(t, "", l.Refusal(Counts{ClientHour: 4, PrincipalHour: 4, ClientOpen: DefaultOpenPerClient - 1}))
	require.Equal(t, ReasonClientRateExceeded, l.Refusal(Counts{ClientHour: 5}))
	require.Equal(t, ReasonPrincipalRateExceeded, l.Refusal(Counts{PrincipalHour: 5}))
	require.Equal(t, ReasonTooManyOpen, l.Refusal(Counts{ClientOpen: DefaultOpenPerClient}))
}

// The decider presents the CALLER'S OWN token, asks every time (no cache), refuses to ask at all
// with no token of the caller's, and reads an unknown verdict as an error.
func TestCallerDeciderAsksUnderTheCallersOwnTokenEveryTime(t *testing.T) {
	var calls atomic.Int32
	var verdict atomic.Value
	verdict.Store("APPROVE")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer the-callers-own-token", r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		require.Contains(t, string(body), "agentActuationDecision")
		_, _ = io.WriteString(w, `{"data":{"agentActuationDecision":{"verdict":"`+verdict.Load().(string)+
			`","requiredApprovals":1,"reasons":[],"policyVersion":3,"expiresInSeconds":90000,"actionsPerHour":7,"clientAttested":true}}}`)
	}))
	defer srv.Close()
	d := NewCallerDecider(srv.URL)
	req := ActuationRequest{Kind: KindAlarmAck, TargetCount: 1}

	_, err := d.Actuation(with(agentClaims()), req)
	require.Error(t, err, "no bearer of the caller's own: nothing is asked")
	require.Zero(t, calls.Load())

	ctx := auth.WithBearer(with(agentClaims()), "the-callers-own-token")
	got, err := d.Actuation(ctx, req)
	require.NoError(t, err)
	require.Equal(t, ActuationDecision{Verdict: VerdictApprove, RequiredApprovals: 1, Reasons: []string{}, PolicyVersion: 3,
		Expiry: MaxExpiry, ActionsPerHour: 7, ClientAttested: true}, got, "the expiry is bounded by the code floor")
	verdict.Store("REFUSE")
	got, err = d.Actuation(ctx, req)
	require.NoError(t, err)
	require.Equal(t, VerdictRefuse, got.Verdict, "a decision is never answered from a cache")
	require.EqualValues(t, 2, calls.Load())

	verdict.Store("MAYBE")
	_, err = d.Actuation(ctx, req)
	require.Error(t, err, "an unknown verdict fails closed")
}

// A refusal from user-management reaches the caller as FORBIDDEN, and an error is never a verdict.
func TestCallerDeciderErrorsAreNeverVerdicts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"no","extensions":{"code":"FORBIDDEN"}}]}`)
	}))
	defer srv.Close()
	ctx := auth.WithBearer(with(personClaims(time.Now())), "t")
	_, err := NewCallerDecider(srv.URL).Execution(ctx, ExecutionRequest{Kind: KindAlarmAck})
	require.ErrorIs(t, err, auth.ErrForbidden)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()
	_, err = NewCallerDecider(down.URL).Execution(ctx, ExecutionRequest{Kind: KindAlarmAck})
	require.Error(t, err)
	require.False(t, errors.Is(err, auth.ErrForbidden))

	require.Equal(t, DefaultExpiry, BoundExpiry(0))
	require.Equal(t, MaxExpiry, BoundExpiry(48*time.Hour))
	require.Equal(t, time.Minute, BoundExpiry(time.Minute))
}

// An older approval is judged by the current policy, including recovery-code admission.
func TestTallyChecksCurrentFreshness(t *testing.T) {
	now := time.Now()
	old := now.Add(-20 * time.Minute)
	su := auth.StepUp{MaxAge: 15 * time.Minute}
	approvals := []Approval{
		{Sub: "a", Amr: auth.AmrPasswordOTP(), MfaTime: &old},
		{Sub: "b", Amr: auth.AmrPasswordRecovery(), MfaTime: &now},
	}
	require.Zero(t, Tally(approvals, []string{"a", "b"}, "proposer", 2, false, su, now))
	approvals = append(approvals, Approval{Sub: "a", Amr: auth.AmrPasswordOTP(), MfaTime: &now})
	approvals = append(approvals, approvals[2])
	require.Equal(t, 1, Tally(approvals, []string{"a", "b"}, "proposer", 2, false, su, now))
	su.AcceptRecoveryCodes = true
	require.Equal(t, 2, Tally(approvals, []string{"a", "b"}, "proposer", 2, false, su, now))
}

// A decision endpoint redirect must never receive the caller's bearer at its destination.
func TestCallerDeciderRefusesRedirect(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	d := NewCallerDecider(source.URL)
	d.HTTP.Transport = http.DefaultTransport
	ctx := auth.WithBearer(context.Background(), "caller-secret")
	_, err := d.Actuation(ctx, ActuationRequest{Kind: KindAlarmClear, TargetCount: 1})
	require.ErrorContains(t, err, "HTTP 307")
	_, err = d.Execution(ctx, ExecutionRequest{Kind: KindAlarmClear})
	require.ErrorContains(t, err, "HTTP 307")
	require.Zero(t, hits.Load())
}
