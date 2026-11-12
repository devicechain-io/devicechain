// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTallyRequiresApproverSession(t *testing.T) {
	now := time.Now()
	a := Approval{Sub: "boss", Amr: auth.AmrPasswordOTP(), MfaTime: &now}
	tally := func() int {
		return Tally([]Approval{a}, []string{"boss"}, "proposer", 1, false, auth.StepUp{MaxAge: time.Minute}, now)
	}
	require.Zero(t, tally())
	a.Sid = "live"
	require.Equal(t, 1, tally())
}

func TestApproverSessions(t *testing.T) {
	require.Equal(t, []ApproverSession{}, ApproverSessions(nil))
	require.Equal(t, []ApproverSession{{Principal: "b", Sid: "b1"}, {Principal: "a", Sid: "a1"}}, ApproverSessions([]Approval{
		{Sub: "legacy"}, {Sub: "b", Sid: "b1"}, {Sub: "a", Sid: "a1"}, {Sub: "b", Sid: "b2"}, {Sid: "orphan"},
	}))
}

func TestAuthorizeApproverCarriesSid(t *testing.T) {
	claims := personClaims(time.Now())
	claims.SessionID = "sign-in"
	got, err := AuthorizeApprover(with(claims), auth.AlarmWrite)
	require.NoError(t, err)
	require.Equal(t, "sign-in", got.Sid)
}

func TestCallerDeciderSendsApproverSessions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sessions []ApproverSession
	}{
		{name: "empty"},
		{name: "bound", sessions: []ApproverSession{{Principal: "boss", Sid: "live"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := tc.sessions
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Variables struct {
						Input map[string]json.RawMessage `json:"input"`
					} `json:"variables"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				raw, ok := body.Variables.Input["approverSessions"]
				require.True(t, ok)
				want := sessions
				if want == nil {
					want = []ApproverSession{}
				}
				encoded, err := json.Marshal(want)
				require.NoError(t, err)
				require.JSONEq(t, string(encoded), string(raw))
				_, _ = io.WriteString(w, `{"data":{"agentExecutionDecision":{"verdict":"APPROVE","requiredApprovals":1,"actuationGeneration":0}}}`)
			}))
			defer srv.Close()
			_, err := NewCallerDecider(srv.URL).Execution(auth.WithBearer(context.Background(), "person"), ExecutionRequest{Kind: KindAlarmAck, ApproverSessions: sessions})
			require.NoError(t, err)
		})
	}
}

func TestCommitPointDiscardsAnAnswerAtTheDeadlineBeforeTheTimerFires(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	original := commitPointNow
	t.Cleanup(func() { commitPointNow = original })
	got, err := CommitPoint(context.Background(), db, func(ctx context.Context) (ExecutionDecision, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.NoError(t, ctx.Err(), "the context timer has not fired")
		commitPointNow = func() time.Time { return deadline }
		return ExecutionDecision{Verdict: VerdictApprove}, nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, ExecutionDecision{}, got)
}
