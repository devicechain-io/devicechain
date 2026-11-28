// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/intent"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
)

// The seam's preview is the planner create runs: the same digests and counts as planBulkChange
// for the same change and devices, and nothing written.
func TestThePreviewIsThePlannersAnswer(t *testing.T) {
	api, ctx := fleetFixture(t, 5)
	set := &FleetConfigSetIntent{Key: "site", ValueType: "STRING", Value: sptr("north")}
	for name, tokens := range map[string][]string{
		"all admitted":        {"d1", "d2", "d3"},
		"with refusals":       {"d4", "nope", "other1", "d4", " d5 "},
		"an unset, one token": {"d2"},
	} {
		cs := *set
		if strings.HasPrefix(name, "an unset") {
			cs = FleetConfigSetIntent{Key: "site", Unset: true}
		}
		want, err := api.PlanFleetOperation(ctx, &FleetOperationCreateRequest{Token: "x", Name: "x", Kind: FleetKindConfigSet,
			ConfigSet: &cs, DeviceTokens: tokens})
		require.NoError(t, err, name)
		got, err := api.PreviewIntentBulkChange(ctx, FleetKindConfigSet, &cs, tokens)
		require.NoError(t, err, name)
		require.Equal(t, want.RequestDigest, got.RequestDigest, name)
		require.Equal(t, want.PlanDigest, got.PlanDigest, name)
		require.Equal(t, want.ContractDigest, got.ContractDigest, name)
		require.Equal(t, want.Admitted, got.Admitted, name)
		require.Equal(t, want.Refused, got.Refused, name)
		require.Equal(t, want.RefusalCounts, got.RefusalCounts, name)
		require.Contains(t, got.ContractBody, FleetApprovalHumanDirect, name)
	}
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Count(&n).Error)
	require.Zero(t, n, "the preview wrote a bulk change")
}

// Every refusal of the planner stays reachable through the preview, so a later loosening of the
// planner is noticed rather than inherited silently. The intent compiler's chunk is built never
// to earn one: the test also proves a maximal chunk is admitted.
func TestThePreviewKeepsThePlannersRefusals(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	set := &FleetConfigSetIntent{Key: "site", ValueType: "STRING", Value: sptr("north")}
	_, err := api.PreviewIntentBulkChange(ctx, FleetKindConfigSet, set, nil)
	requireFleetCode(t, err, FleetCodeInvalidRequest)
	_, err = api.PreviewIntentBulkChange(ctx, FleetKindConfigSet, set, []string{})
	requireFleetCode(t, err, FleetCodeInvalidRequest)

	long := make([]string, ledger.MaxLinesPerOperation)
	drift := intent.Drift{}
	for i := range long {
		long[i] = fmt.Sprintf("d%d-%s", i, strings.Repeat("x", intent.MaxTokenLen-len(fmt.Sprintf("d%d-", i))))
		drift.Lines = append(drift.Lines, intent.Line{DeviceID: int64(i + 1), Token: long[i]})
	}
	_, err = api.PreviewIntentBulkChange(ctx, FleetKindConfigSet, set, long)
	requireFleetCode(t, err, FleetCodeInputTooLarge)

	short := make([]string, ledger.MaxLinesPerOperation+1)
	for i := range short {
		short[i] = fmt.Sprintf("s%d", i)
	}
	_, err = api.PreviewIntentBulkChange(ctx, FleetKindConfigSet, set, short)
	requireFleetCode(t, err, FleetCodeOperationTooLarge)

	_, err = api.PreviewIntentBulkChange(ctx, "config.reboot", set, []string{"d1"})
	requireFleetCode(t, err, FleetCodeKindNotExecutable)

	// The compiler's chunk of the same 10,000 long tokens fits: 8,128 lines, admitted.
	chunk, err := intent.Chunk(drift, intent.MaxLinesPerReconcile, ledger.MaxRequestBytes)
	require.NoError(t, err)
	require.Len(t, chunk, 8128)
	tokens := make([]string, len(chunk))
	for i, l := range chunk {
		tokens[i] = l.Token
	}
	plan, err := api.PreviewIntentBulkChange(ctx, FleetKindConfigSet, set, tokens)
	require.NoError(t, err, "a maximal byte-bounded chunk is refused by the planner")
	require.Equal(t, 8128, plan.Refused, "the long tokens name no device, and every one is still planned")
}

func TestThePreviewNeedsTheSeam(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	api.IntentSeam = nil
	_, err := api.PreviewIntentBulkChange(ctx, FleetKindConfigSet, &FleetConfigSetIntent{Key: "site", Unset: true}, []string{"d1"})
	require.Error(t, err, "an Api without a seam answered")
}

type standingAnswer struct {
	answer map[string]bool
	err    error
	block  bool
	asked  [][]string
}

func (s *standingAnswer) PrincipalStanding(ctx context.Context, principals, authorities []string) (map[string]bool, error) {
	s.asked = append(s.asked, append(append([]string{}, principals...), authorities...))
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.answer, s.err
}

// A standing answer is yes, no, or none; none is never read as either.
func TestGranteeStandingAnswersOrSaysItCannot(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	got, err := api.BulkChangeGranteeStanding(ctx, "alice@example.com")
	require.NoError(t, err)
	require.Nil(t, got, "no checker wired must be no answer")

	for name, tc := range map[string]struct {
		checker *standingAnswer
		want    *bool
	}{
		"qualifies":     {&standingAnswer{answer: map[string]bool{"alice@example.com": true}}, boolPtr(true)},
		"does not":      {&standingAnswer{answer: map[string]bool{"alice@example.com": false}}, boolPtr(false)},
		"error":         {&standingAnswer{err: errors.New("down")}, nil},
		"not mentioned": {&standingAnswer{answer: map[string]bool{"bob@example.com": true}}, nil},
		"nil answer":    {&standingAnswer{}, nil},
	} {
		api.PrincipalStanding = tc.checker
		got, err := api.BulkChangeGranteeStanding(ctx, "alice@example.com")
		require.NoError(t, err, name)
		require.Equal(t, tc.want, got, name)
		require.Equal(t, [][]string{{"alice@example.com", "device:write", "fleet:operate"}}, tc.checker.asked, name)
	}

	_, err = api.BulkChangeGranteeStanding(ctx, "  ")
	require.ErrorIs(t, err, ErrStandingPrincipalRequired)
}

func TestGranteeStandingKeepsTheBulkChangeDeadline(t *testing.T) {
	require.Equal(t, 4*time.Second, FleetStandingTimeout)
	api, ctx := fleetFixture(t, 0)
	api.PrincipalStanding = &standingAnswer{block: true}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	got, err := api.BulkChangeGranteeStanding(short, "alice@example.com")
	require.NoError(t, err)
	require.Nil(t, got, "a timeout must be no answer")
}

func TestSeamQueriesRefuseWithoutPostgres(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	_, err := api.ConfigurationKeyStateDigest(ctx, IntentSeamTarget{GroupToken: "g", Version: 1}, "dim")
	require.Error(t, err)
	_, err = api.ConfigurationKeyStateDigest(context.Background(), IntentSeamTarget{GroupToken: "g", Version: 1}, "dim")
	require.ErrorIs(t, err, core.ErrNoTenant)
}

func boolPtr(b bool) *bool { return &b }

// The evaluation's three preview branches end in no planner refusal: an empty target and a
// satisfied fleet send nothing, and 10,000 drifted devices with 128-byte tokens send the
// byte-bounded chunk of 8,128 lines. The controls show each branch is load-bearing: previewing
// the empty chunk anyway is INVALID_REQUEST, and the whole unbounded drift is INPUT_TOO_LARGE.
func TestEveryPreviewBranchIsInsideThePlannersBounds(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	spec, err := intent.Parse([]byte(`{"target":{"groupToken":"g","groupVersion":1},"point":{"configKey":"site"},` +
		`"value":{"valueType":"STRING","value":"north"},"class":"SCHEDULE","ceiling":{"maxDevices":100000},` +
		`"reconcile":{"minIntervalSeconds":60,"maxLinesPerReconcile":10000,"maxActuationsPerDevicePerDay":4}}`))
	require.NoError(t, err)
	set := &FleetConfigSetIntent{Key: "site", ValueType: "STRING", Value: sptr("north")}
	preview := func(lines []intent.Line) error {
		tokens := make([]string, len(lines))
		for i, l := range lines {
			tokens[i] = l.Token
		}
		_, err := api.PreviewIntentBulkChange(ctx, FleetKindConfigSet, set, tokens)
		return err
	}
	long := intent.Drift{}
	for i := 1; i <= intent.MaxLinesPerReconcile; i++ {
		token := fmt.Sprintf("d%d-", i)
		long.Lines = append(long.Lines, intent.Line{DeviceID: int64(i), Token: token + strings.Repeat("x", intent.MaxTokenLen-len(token))})
	}
	for name, tc := range map[string]struct {
		members   int64
		drift     intent.Drift
		wantLines int
	}{
		"an empty target":    {0, intent.Drift{Lines: []intent.Line{}}, 0},
		"a satisfied fleet":  {1, intent.Drift{Lines: []intent.Line{}, Counts: intent.Counts{InSync: 1}}, 0},
		"10,000 long tokens": {int64(len(long.Lines)), long, 8128},
	} {
		lines, send, err := intent.PreviewLines(spec, tc.members, tc.drift, ledger.MaxRequestBytes)
		require.NoError(t, err, name)
		require.Len(t, lines, tc.wantLines, name)
		require.Equal(t, tc.wantLines > 0, send, name)
		if send {
			require.NoError(t, preview(lines), "%s: the planner refused the chunk", name)
		} else {
			requireFleetCode(t, preview(lines), FleetCodeInvalidRequest) // control: the branch is needed
		}
	}
	requireFleetCode(t, preview(long.Lines), FleetCodeInputTooLarge) // control: the byte bound is needed
}
