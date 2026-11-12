// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

func TestCompareRuleVersionsRefusalsAndCancellation(t *testing.T) {
	r := &SchemaResolver{Replay: neverCompareOpener{t}}
	ctx := compareContext(compareServiceClaims())
	now := time.Now().UTC().Add(-time.Hour)
	valid := func() compareRuleVersionsInput {
		return compareRuleVersionsInput{
			ProfileToken: "p", DeviceTokens: []string{"d"},
			Windows: []compareWindowInput{{"INCIDENT", now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)}, {"NORMAL", now.Add(-3 * time.Hour).Format(time.RFC3339), now.Add(-2 * time.Hour).Format(time.RFC3339)}},
			Draft:   &compareRuleEnvelopeInput{Token: "hot", Enabled: true, Definition: `{"name":"x","type":"threshold","when":{"metric":"t","op":"gt","threshold":1}}`},
		}
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*compareRuleVersionsInput)
	}{
		{"bad time", refusalInvalidInput, func(in *compareRuleVersionsInput) { in.Windows[0].Start = "yesterday" }},
		{"compile", refusalRuleRejected, func(in *compareRuleVersionsInput) {
			in.Draft.Definition = `{"name":"x","type":"threshold","when":{"metric":"t"}}`
		}},
		{"strict expectation", refusalInvalidInput, func(in *compareRuleVersionsInput) {
			in.Expectations = &[]compareExpectationInput{{Window: "INCIDENT", Expect: `raised: {device: d, surprise: true}`}}
		}},
		{"device limit", refusalLimitExceeded, func(in *compareRuleVersionsInput) { in.DeviceTokens = make([]string, 51) }},
		{"bad bounds", refusalInvalidInput, func(in *compareRuleVersionsInput) {
			in.StreamBounds = &[]compareBoundsInput{{Role: "INCIDENT", Stream: "resolved-events", FirstSeq: "18446744073709551616", LastSeq: "2"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.mutate(&in)
			out, err := r.CompareRuleVersions(ctx, struct{ Input compareRuleVersionsInput }{in})
			require.NoError(t, err)
			require.False(t, out.Ok())
			require.Equal(t, tc.want, out.Refusal().Reason())
		})
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := r.CompareRuleVersions(cancelled, struct{ Input compareRuleVersionsInput }{valid()})
	require.ErrorIs(t, err, context.Canceled)

	// One comparison uses one preview slot, and saturation refuses immediately.
	for i := 0; i < maxConcurrentPreviewsPerTenant; i++ {
		require.True(t, globalPreviewGate.acquire("nightshift"))
	}
	t.Cleanup(func() {
		for i := 0; i < maxConcurrentPreviewsPerTenant; i++ {
			globalPreviewGate.release("nightshift")
		}
	})
	out, err := r.CompareRuleVersions(ctx, struct{ Input compareRuleVersionsInput }{valid()})
	require.NoError(t, err)
	require.Equal(t, refusalBusy, out.Refusal().Reason())
}

func TestCompareRuleVersionsAgentMarkedServiceIsForbidden(t *testing.T) {
	claims := compareServiceClaims()
	claims.ClientId = "agent"
	_, err := (&SchemaResolver{}).CompareRuleVersions(compareContext(claims), struct{ Input compareRuleVersionsInput }{})
	require.ErrorIs(t, err, auth.ErrForbidden)
}
