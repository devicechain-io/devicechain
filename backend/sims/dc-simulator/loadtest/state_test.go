// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedStates answers each poll from a script: poll n (1-based) returns script(n, token)
// for every token asked, and every call is recorded.
type scriptedStates struct {
	polls  int
	calls  int
	script func(poll int, token string) deviceStateObs
	err    error
	// chunk sizes seen, per call
	asked []int
}

func (s *scriptedStates) states(_ context.Context, tokens []string) (map[string]deviceStateObs, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.calls++
	s.asked = append(s.asked, len(tokens))
	out := make(map[string]deviceStateObs, len(tokens))
	for _, t := range tokens {
		out[t] = s.script(s.polls+1, t)
	}
	return out, nil
}

// pollCounting advances the scripted poll number once per full pass over the fleet.
type pollCounting struct {
	*scriptedStates
	perPoll int
}

func (p *pollCounting) states(ctx context.Context, tokens []string) (map[string]deviceStateObs, error) {
	out, err := p.scriptedStates.states(ctx, tokens)
	if err == nil && p.calls%p.perPoll == 0 {
		p.polls++
	}
	return out, err
}

var stT0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func ledger(n int) map[string]time.Time {
	want := make(map[string]time.Time, n)
	for i := 0; i < n; i++ {
		want[fmt.Sprintf("dev-%03d", i)] = stT0.Add(time.Duration(i) * time.Second)
	}
	return want
}

func at(t time.Time) deviceStateObs { return deviceStateObs{Present: true, LastActivity: t} }

// The live state catches up on the third poll: CaughtUp, three polls, nothing behind.
func TestAwaitStatePassesWhenEveryDeviceReachesItsLastEvent(t *testing.T) {
	want := ledger(10)
	r := &pollCounting{scriptedStates: &scriptedStates{script: func(poll int, tok string) deviceStateObs {
		if poll < 3 {
			return at(want[tok].Add(-time.Minute))
		}
		return at(want[tok])
	}}, perPoll: 1}
	res, err := AwaitState(context.Background(), r, want, 0, time.Minute)
	require.NoError(t, err)
	assert.True(t, res.CaughtUp)
	assert.Equal(t, 3, res.Polls)
	assert.Equal(t, 0, res.Behind)
	assert.Equal(t, 10, res.Devices)
	assert.True(t, StateInvariant(res, 12*time.Second).Passed)
}

// 🔴 The negative control: one device of ten never catches up. Not caught up, one behind,
// the example names it, and the invariant FAILS.
func TestAwaitStateFailsWhenOneDeviceNeverCatchesUp(t *testing.T) {
	want := ledger(10)
	r := &scriptedStates{script: func(_ int, tok string) deviceStateObs {
		if tok == "dev-007" {
			return at(want[tok].Add(-time.Minute))
		}
		return at(want[tok])
	}}
	res, err := AwaitState(context.Background(), r, want, 0, 50*time.Millisecond)
	require.NoError(t, err)
	assert.False(t, res.CaughtUp)
	assert.Equal(t, 1, res.Behind)
	assert.Contains(t, res.Example, "dev-007")
	inv := StateInvariant(res, 5*time.Minute)
	assert.False(t, inv.Passed)
	assert.Equal(t, InvStateCaughtUp, inv.Name)
	assert.Contains(t, inv.Detail, "1 of 10 devices")
}

// A device with no row has not caught up to anything.
func TestAwaitStateCountsAMissingRowAsBehind(t *testing.T) {
	want := ledger(3)
	r := &scriptedStates{script: func(_ int, tok string) deviceStateObs {
		if tok == "dev-001" {
			return deviceStateObs{}
		}
		return at(want[tok])
	}}
	res, err := AwaitState(context.Background(), r, want, 0, 20*time.Millisecond)
	require.NoError(t, err)
	assert.False(t, res.CaughtUp)
	assert.Equal(t, 1, res.Behind)
	assert.Contains(t, res.Example, "no device-state row")
}

// A row with no activity time has not caught up either.
func TestAwaitStateCountsARowWithNoActivityAsBehind(t *testing.T) {
	want := ledger(1)
	r := &scriptedStates{script: func(int, string) deviceStateObs { return deviceStateObs{Present: true} }}
	res, err := AwaitState(context.Background(), r, want, 0, 20*time.Millisecond)
	require.NoError(t, err)
	assert.False(t, res.CaughtUp)
	assert.Contains(t, res.Example, "no lastActivityTime")
}

// A read error is a run error, never "caught up".
func TestAwaitStateFailsClosedOnAReadError(t *testing.T) {
	r := &scriptedStates{err: errors.New("device-state unavailable")}
	res, err := AwaitState(context.Background(), r, ledger(2), 0, time.Minute)
	require.Error(t, err)
	assert.False(t, res.CaughtUp)
}

// Nothing accepted is nothing asserted, which is not a pass.
func TestAwaitStateRefusesAnEmptyLedger(t *testing.T) {
	r := &scriptedStates{script: func(int, string) deviceStateObs { return deviceStateObs{} }}
	_, err := AwaitState(context.Background(), r, map[string]time.Time{}, 0, time.Minute)
	require.Error(t, err)
	assert.Equal(t, 0, r.calls, "an empty ledger must be refused before anything is read")
}

// The skew pad is the boundary, on both sides: 4 s short of the last accepted event passes,
// 6 s short does not.
func TestAwaitStateAllowsTheSkewPad(t *testing.T) {
	for _, tc := range []struct {
		short time.Duration
		want  bool
	}{{4 * time.Second, true}, {stateSkewPad, true}, {6 * time.Second, false}} {
		t.Run(tc.short.String(), func(t *testing.T) {
			want := ledger(1)
			r := &scriptedStates{script: func(_ int, tok string) deviceStateObs { return at(want[tok].Add(-tc.short)) }}
			res, err := AwaitState(context.Background(), r, want, 0, 20*time.Millisecond)
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.CaughtUp)
		})
	}
}

// A large fleet is read in chunks of at most stateReadChunk.
func TestAwaitStateReadsInChunks(t *testing.T) {
	want := ledger(450)
	r := &scriptedStates{script: func(_ int, tok string) deviceStateObs { return at(want[tok]) }}
	res, err := AwaitState(context.Background(), r, want, 0, time.Minute)
	require.NoError(t, err)
	assert.True(t, res.CaughtUp)
	assert.Equal(t, []int{200, 200, 50}, r.asked)
}

// The production reader is the presence harness's own query, which now carries
// lastActivityTime: a row's activity time reaches the observation, and a malformed one fails
// closed.
func TestTheStateReaderParsesLastActivityTime(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return statesJSON(map[string]any{
			"deviceToken": "a", "active": true, "presenceSource": "INFERRED", "sessionId": "0", "source": "mqtt1",
			"lastActivityTime": "2026-09-20T12:00:01.5Z",
		}, map[string]any{
			"deviceToken": "b", "active": false, "presenceSource": "INFERRED", "sessionId": "0", "source": "mqtt1",
			"lastActivityTime": nil,
		}), nil
	}}}
	obs, err := o.states(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	assert.True(t, obs["a"].LastActivity.Equal(stT0.Add(1500*time.Millisecond)))
	assert.True(t, obs["b"].Present)
	assert.True(t, obs["b"].LastActivity.IsZero())

	bad := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return statesJSON(map[string]any{
			"deviceToken": "a", "active": true, "presenceSource": "INFERRED", "sessionId": "0", "source": "mqtt1",
			"lastActivityTime": "yesterday",
		}), nil
	}}}
	_, err = bad.states(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a time")
}

// The report states the check's outcome, or that it was not run.
func TestTheReportSaysWhetherTheLiveStateCaughtUp(t *testing.T) {
	yes, no := true, false
	r := &Report{Invariants: []Invariant{{Name: "x", Passed: true}}}
	assert.Contains(t, r.Human(), "state: not checked")
	r.StateCaughtUp, r.StateLagSecs = &yes, 12
	assert.Contains(t, r.Human(), "state: caught up 12s after the drive")
	r.StateCaughtUp = &no
	assert.Contains(t, r.Human(), "state: NOT caught up")
	body, err := r.JSON()
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(body), `"stateCaughtUp": false`))
}

// A negative state timeout is refused; zero is legal and turns the check off.
func TestAStateTimeoutMustNotBeNegative(t *testing.T) {
	p := Profile{Manifest: "devicepulse", StateTimeout: -time.Second}
	require.Error(t, p.Validate())
	p.StateTimeout = 0
	require.NoError(t, p.Validate())
	assert.Equal(t, time.Duration(0), p.withDefaults().StateTimeout, "0 must stay 0: it means do not check")
}

// A run asked to check the live state with a handshake that cannot reach device-state is
// refused before anything is provisioned.
func TestRunRefusesAStateCheckWithNoDeviceStateEndpoint(t *testing.T) {
	hs := &sim.Handshake{Tenant: "acme", Endpoints: sim.Endpoints{EventMgmtWS: "ws://example.invalid/graphql"}}
	_, err := Run(context.Background(), hs, Profile{Manifest: "devicepulse", StateTimeout: time.Minute})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deviceStateGraphQL")
}
