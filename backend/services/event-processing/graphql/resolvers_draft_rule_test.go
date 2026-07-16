// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// draftValidRule is a threshold rule that compiles at the default limits.
const draftValidRule = `{"name":"High temperature","type":"threshold","severity":"major","when":{"metric":"tempC","op":"gt","threshold":80},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"high-temp"}}]}`

// stubInferer returns a fixed candidate/error for the drafter under test.
type stubInferer struct {
	out nldraft.InferOutput
	err error
}

func (s stubInferer) Infer(context.Context, string, string, string) (nldraft.InferOutput, error) {
	return s.out, s.err
}

// draftInput wraps the resolver arg for brevity.
func draftInput(in draftRuleFromTextInput) struct{ Input draftRuleFromTextInput } {
	return struct{ Input draftRuleFromTextInput }{Input: in}
}

// wiredResolver builds a resolver whose drafter always returns the given candidate.
func wiredResolver(candidate string) *SchemaResolver {
	d := nldraft.NewDrafter(stubInferer{out: nldraft.InferOutput{Candidate: candidate, Model: "m", Provider: "p"}}, rules.DefaultLimits(), 1)
	return &SchemaResolver{Drafter: d}
}

// draftCtx is a device:write-authorized context carrying a tenant.
func draftCtx() context.Context {
	return core.WithTenant(authedContext(auth.DeviceWrite), "acme")
}

func TestDraftRule_Unwired_ReturnsUnavailable(t *testing.T) {
	r := &SchemaResolver{} // Drafter nil ⇒ inference path not wired
	res, err := r.DraftDetectionRuleFromText(draftCtx(), draftInput(draftRuleFromTextInput{Text: "x", ProfileToken: "p"}))
	require.NoError(t, err)
	assert.True(t, res.Unavailable())
	assert.False(t, res.Ok())
	require.NotNil(t, res.UnavailableReason())
	assert.Contains(t, *res.UnavailableReason(), "not enabled")
}

func TestDraftRule_RequiresDeviceWrite(t *testing.T) {
	r := wiredResolver(draftValidRule)
	// device:read is enough for the pure compile doors but NOT for drafting (it spends budget).
	ctx := core.WithTenant(authedContext(auth.DeviceRead), "acme")
	_, err := r.DraftDetectionRuleFromText(ctx, draftInput(draftRuleFromTextInput{Text: "x", ProfileToken: "p"}))
	require.Error(t, err)
}

func TestDraftRule_RequiresTenant(t *testing.T) {
	r := wiredResolver(draftValidRule)
	// Authorized but no tenant in context ⇒ fail closed.
	_, err := r.DraftDetectionRuleFromText(authedContext(auth.DeviceWrite), draftInput(draftRuleFromTextInput{Text: "x", ProfileToken: "p"}))
	require.ErrorIs(t, err, core.ErrNoTenant)
}

func TestDraftRule_Wired_Ok(t *testing.T) {
	r := wiredResolver(draftValidRule)
	metrics := []metricHintInput{{Key: "tempC"}}
	res, err := r.DraftDetectionRuleFromText(draftCtx(), draftInput(draftRuleFromTextInput{
		Text: "alarm when temp over 80", ProfileToken: "p", Metrics: &metrics,
	}))
	require.NoError(t, err)
	assert.True(t, res.Ok())
	assert.False(t, res.Unavailable())
	require.NotNil(t, res.Definition())
	assert.Contains(t, *res.Definition(), `"name":"High temperature"`)
	require.NotNil(t, res.EstimatedCost())
	assert.Empty(t, res.Diagnostics())
}
