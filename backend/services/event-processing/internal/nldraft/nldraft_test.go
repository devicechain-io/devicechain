// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package nldraft

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validRule is a threshold rule that compiles through rules.Compile at the default limits.
const validRule = `{"name":"High temperature","type":"threshold","severity":"major","when":{"metric":"tempC","op":"gt","threshold":80},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"high-temp"}}]}`

// invalidRule is a threshold rule missing its required `when` comparison — the compiler rejects
// it with a field-anchored ValidationError, which is exactly the repair-loop feedback.
const invalidRule = `{"name":"Bad rule","type":"threshold"}`

// fakeInferer returns scripted candidates (one per attempt, repeating the last) or a fixed error,
// and records the prompts/systems it was called with. When errAfter > 0 the error is returned
// starting at that 1-based call number (so an earlier candidate can be produced first); when
// errAfter == 0 and err is set, every call errors.
type fakeInferer struct {
	outputs  []InferOutput
	err      error
	errAfter int
	prompts  []string
	systems  []string
}

func (f *fakeInferer) Infer(_ context.Context, _, prompt, system string) (InferOutput, error) {
	f.prompts = append(f.prompts, prompt)
	f.systems = append(f.systems, system)
	n := len(f.prompts) // 1-based call number
	if f.err != nil && (f.errAfter == 0 || n >= f.errAfter) {
		return InferOutput{}, f.err
	}
	i := n - 1
	if i >= len(f.outputs) {
		i = len(f.outputs) - 1
	}
	return f.outputs[i], nil
}

func draft(t *testing.T, f *fakeInferer, req Request) Result {
	t.Helper()
	d := NewDrafter(f, rules.DefaultLimits(), 3)
	res, err := d.Draft(context.Background(), "acme", req)
	require.NoError(t, err)
	return res
}

func TestDraft_CompilesFirstTry(t *testing.T) {
	f := &fakeInferer{outputs: []InferOutput{{Candidate: validRule, Model: "claude-x", Provider: "anthropic"}}}
	res := draft(t, f, Request{Text: "alarm when temp over 80"})

	assert.True(t, res.OK)
	assert.False(t, res.Unavailable)
	assert.EqualValues(t, 1, res.Attempts)
	assert.Equal(t, "claude-x", res.Model)
	assert.Equal(t, "anthropic", res.Provider)
	assert.Positive(t, res.EstimatedCost)
	assert.Contains(t, res.Definition, `"name":"High temperature"`)
	// The draft carries no id — the token is assigned at save, and the "draft" placeholder used
	// for the compile check must not leak into the definition.
	assert.NotContains(t, res.Definition, `"id"`)
	assert.NotContains(t, res.Definition, "draft")
}

func TestDraft_RepairsThenCompiles(t *testing.T) {
	f := &fakeInferer{outputs: []InferOutput{
		{Candidate: invalidRule, Model: "m", Provider: "p"}, // attempt 1: rejected
		{Candidate: validRule, Model: "m", Provider: "p"},   // attempt 2: compiles
	}}
	res := draft(t, f, Request{Text: "alarm when temp over 80"})

	assert.True(t, res.OK)
	assert.EqualValues(t, 2, res.Attempts)
	require.Len(t, f.prompts, 2)
	// The second prompt is a repair turn: it carries the rejected candidate and the compiler error.
	assert.Contains(t, f.prompts[1], "REJECTED")
	assert.Contains(t, f.prompts[1], invalidRule)
}

func TestDraft_NeverCompiles_ReturnsDiagnosticsAndRaw(t *testing.T) {
	f := &fakeInferer{outputs: []InferOutput{{Candidate: invalidRule, Model: "m", Provider: "p"}}}
	res := draft(t, f, Request{Text: "nonsense"})

	assert.False(t, res.OK)
	assert.False(t, res.Unavailable)
	assert.EqualValues(t, 3, res.Attempts) // exhausted the default cap
	assert.Equal(t, invalidRule, res.RawCandidate)
	require.NotEmpty(t, res.Diagnostics)
	// The threshold-without-when rejection is anchored to the `when` field.
	assert.Equal(t, "when", res.Diagnostics[0].Field)
	assert.NotEmpty(t, res.Diagnostics[0].Message)
}

func TestDraft_InferenceError_IsUnavailable(t *testing.T) {
	// A realistic svcclient error carrying the peer URL + a dial detail — it must NOT reach the
	// author (MED-1: no topology leak). The drafter surfaces the fixed safe reason and logs detail.
	f := &fakeInferer{err: errors.New("svcclient: http://ai-inference:8080/graphql: dial tcp 10.1.2.3:8080: connect: connection refused")}
	res := draft(t, f, Request{Text: "anything"})

	assert.True(t, res.Unavailable)
	assert.False(t, res.OK)
	assert.EqualValues(t, 0, res.Attempts)
	assert.Equal(t, unavailableReason, res.UnavailableReason)
	assert.NotContains(t, res.UnavailableReason, "ai-inference")
	assert.NotContains(t, res.UnavailableReason, "10.1.2.3")
}

// A transport error AFTER a candidate was already produced returns the actionable !ok result
// (raw candidate + diagnostics), not a bare unavailable — the author's spent work is preserved.
func TestDraft_MidLoopError_PreservesWork(t *testing.T) {
	f := &fakeInferer{
		outputs:  []InferOutput{{Candidate: invalidRule, Model: "m", Provider: "p"}},
		err:      errors.New("boom"),
		errAfter: 2, // attempt 1 produces a (rejected) candidate; attempt 2 errors.
	}
	res := draft(t, f, Request{Text: "x"})

	assert.False(t, res.Unavailable)
	assert.False(t, res.OK)
	assert.EqualValues(t, 1, res.Attempts)
	assert.Equal(t, invalidRule, res.RawCandidate)
	assert.NotEmpty(t, res.Diagnostics)
}

func TestDraft_StripsMarkdownFence(t *testing.T) {
	fenced := "```json\n" + validRule + "\n```"
	f := &fakeInferer{outputs: []InferOutput{{Candidate: fenced, Model: "m", Provider: "p"}}}
	res := draft(t, f, Request{Text: "alarm when temp over 80"})

	assert.True(t, res.OK, "a fenced valid rule should still compile after fence-stripping")
	assert.EqualValues(t, 1, res.Attempts)
}

func TestExtractJSON(t *testing.T) {
	assert.Equal(t, `{"a":1}`, extractJSON(`{"a":1}`))
	assert.Equal(t, `{"a":1}`, extractJSON("```json\n{\"a\":1}\n```"))
	assert.Equal(t, `{"a":1}`, extractJSON("```\n{\"a\":1}\n```"))
	assert.Equal(t, `{"a":1}`, extractJSON("  \n {\"a\":1} \n "))
}

func TestBuildSystemPrompt_MetricHints(t *testing.T) {
	base := buildSystemPrompt(nil)
	assert.Equal(t, systemPromptBase, base)

	withHints := buildSystemPrompt([]MetricHint{
		{Key: "tempC", DataType: "number", Unit: "°C", Description: "case temperature"},
		{Key: "rpm"},
		{Key: ""}, // skipped
	})
	assert.Contains(t, withHints, "Available metrics")
	assert.Contains(t, withHints, "tempC (number, °C): case temperature")
	assert.Contains(t, withHints, "\n- rpm")
	// The empty-key hint is skipped (no dangling bullet).
	assert.Equal(t, 2, strings.Count(withHints[strings.Index(withHints, "Available metrics"):], "\n- "))
}

func TestNewDrafter_DefaultsMaxAttempts(t *testing.T) {
	f := &fakeInferer{outputs: []InferOutput{{Candidate: invalidRule}}}
	d := NewDrafter(f, rules.DefaultLimits(), 0) // 0 ⇒ default
	res, err := d.Draft(context.Background(), "acme", Request{Text: "x"})
	require.NoError(t, err)
	assert.EqualValues(t, defaultMaxAttempts, res.Attempts)
}

// A tenant over its inference rate ceiling gets the TRANSIENT reason, not the generic
// one. The distinction matters to the author: the generic reason points at provider
// configuration and would send them to an operator, when the fix is to wait a moment.
func TestDraft_RateLimited_ReportsTransientReason(t *testing.T) {
	f := &fakeInferer{err: fmt.Errorf("ai-inference: %w: upstream said no", ErrRateLimited)}
	res := draft(t, f, Request{Text: "alarm when temp over 80"})

	assert.True(t, res.Unavailable)
	assert.False(t, res.OK)
	assert.Equal(t, rateLimitedReason, res.UnavailableReason)
	assert.NotEqual(t, unavailableReason, res.UnavailableReason)
	// The reason describes the caller's own behaviour against their own tenant's
	// ceiling; it must never carry the wrapped upstream detail (topology).
	assert.NotContains(t, res.UnavailableReason, "upstream said no")
}

// Every other inference failure keeps the generic fixed reason, so a marker that ever
// drifts degrades to today's message rather than to a wrong one.
func TestDraft_OtherFailure_ReportsGenericReason(t *testing.T) {
	f := &fakeInferer{err: errors.New("ai-inference: dial tcp 10.0.0.5:8080: connection refused")}
	res := draft(t, f, Request{Text: "alarm when temp over 80"})

	assert.True(t, res.Unavailable)
	assert.Equal(t, unavailableReason, res.UnavailableReason)
	assert.NotContains(t, res.UnavailableReason, "10.0.0.5", "the reason must never leak topology")
}

// A rate limit hit AFTER a candidate was produced keeps the spent work (the candidate +
// its compiler diagnostics) AND says the repair loop was truncated — otherwise the
// author reads "the AI wrote a non-compiling rule" and rewrites a description that was
// never the problem, when the fix is to wait.
func TestDraft_RateLimitedMidLoop_KeepsWorkAndSaysTruncated(t *testing.T) {
	f := &fakeInferer{
		outputs:  []InferOutput{{Candidate: invalidRule, Model: "m", Provider: "p"}},
		err:      fmt.Errorf("ai-inference: %w", ErrRateLimited),
		errAfter: 2, // call 1 produces a candidate; the repair call (2) is shed
	}
	res := draft(t, f, Request{Text: "alarm when temp over 80"})

	// Not "unavailable": a candidate exists, so the author gets the actionable result.
	assert.False(t, res.Unavailable)
	assert.False(t, res.OK)
	assert.Equal(t, invalidRule, res.RawCandidate)
	assert.EqualValues(t, 1, res.Attempts)

	require.NotEmpty(t, res.Diagnostics)
	last := res.Diagnostics[len(res.Diagnostics)-1]
	assert.Equal(t, repairTruncatedMessage, last.Message)
	assert.Empty(t, last.Field, "the truncation notice is unanchored — it is not a compiler field rejection")
	// The compiler's own diagnostics are still there alongside it.
	assert.Equal(t, "when", res.Diagnostics[0].Field)
}

// A non-rate-limit mid-loop failure keeps today's behaviour: spent work preserved, no
// truncation notice (nothing about waiting would help).
func TestDraft_OtherFailureMidLoop_NoTruncationNotice(t *testing.T) {
	f := &fakeInferer{
		outputs:  []InferOutput{{Candidate: invalidRule, Model: "m", Provider: "p"}},
		err:      errors.New("connection refused"),
		errAfter: 2,
	}
	res := draft(t, f, Request{Text: "alarm when temp over 80"})

	assert.False(t, res.OK)
	require.NotEmpty(t, res.Diagnostics)
	for _, d := range res.Diagnostics {
		assert.NotEqual(t, repairTruncatedMessage, d.Message)
	}
}
