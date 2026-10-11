// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	typed, ok := err.(interface{ Extensions() map[string]any })
	require.True(t, ok, "the refusal must carry an extensions code, got %T: %v", err, err)
	code, _ := typed.Extensions()["code"].(string)
	return code
}

// No provider client reads Params, so setting one must fail loudly (UNSUPPORTED) rather
// than be stored, echoed and ignored.
func TestProviderParamsAreRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithSystemContext(context.Background())

	req := claudeReq("p-params", nil)
	req.Params = strp(`{"temperature":0.2}`)
	_, err := api.CreateAIProvider(ctx, req)
	require.Error(t, err, "create with params must be refused")
	assert.Equal(t, "UNSUPPORTED", codeOf(t, err))

	// A blank document is "no params", not a setting.
	blank := claudeReq("p-blank", nil)
	blank.Params = strp("  ")
	_, err = api.CreateAIProvider(ctx, blank)
	require.NoError(t, err)

	// Update: setting is refused and leaves the row alone...
	up := &AIProviderUpdateRequest{}
	up.Params = dcgraphql.OptionalString{Set: true, Value: strp(`{"temperature":0.2}`)}
	_, err = api.UpdateAIProvider(ctx, "p-blank", up, nil)
	require.Error(t, err, "update with params must be refused")
	assert.Equal(t, "UNSUPPORTED", codeOf(t, err))
	rows, err := api.AIProvidersByToken(ctx, []string{"p-blank"})
	require.NoError(t, err)
	assert.Empty(t, rows[0].Params)

	// ...and an explicit null is still accepted.
	clear := &AIProviderUpdateRequest{}
	clear.Params = dcgraphql.OptionalString{Set: true}
	_, err = api.UpdateAIProvider(ctx, "p-blank", clear, nil)
	require.NoError(t, err)
}

// A provider written before the refusal keeps its stored params: an unrelated edit must
// not be blocked by them, and an explicit null clears them.
func TestLegacyStoredParamsStayEditable(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithSystemContext(context.Background())
	created, err := api.CreateAIProvider(ctx, claudeReq("p-legacy", nil))
	require.NoError(t, err)
	require.NoError(t, api.sys(ctx).Model(created).Update("params", `{"maxTokens":256}`).Error)

	rename := &AIProviderUpdateRequest{}
	rename.Name = dcgraphql.OptionalString{Set: true, Value: strp("Renamed")}
	_, err = api.UpdateAIProvider(ctx, "p-legacy", rename, nil)
	require.NoError(t, err)
	rows, err := api.AIProvidersByToken(ctx, []string{"p-legacy"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"maxTokens":256}`, string(rows[0].Params), "an omitted params field is preserved")

	clear := &AIProviderUpdateRequest{}
	clear.Params = dcgraphql.OptionalString{Set: true}
	_, err = api.UpdateAIProvider(ctx, "p-legacy", clear, nil)
	require.NoError(t, err)
	rows, err = api.AIProvidersByToken(ctx, []string{"p-legacy"})
	require.NoError(t, err)
	assert.Empty(t, rows[0].Params)
}

// Resending the stored value is not a change: it is accepted, while a different value is
// still refused. The console used to resend params on every save.
func TestResendingStoredParamsIsAccepted(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithSystemContext(context.Background())
	created, err := api.CreateAIProvider(ctx, claudeReq("p-resend", nil))
	require.NoError(t, err)
	require.NoError(t, api.sys(ctx).Model(created).Update("params", `{"maxTokens":256}`).Error)

	same := &AIProviderUpdateRequest{}
	same.Params = dcgraphql.OptionalString{Set: true, Value: strp(`{ "maxTokens": 256 }`)}
	same.Enabled = dcgraphql.OptionalBool{Set: true, Value: boolp(false)}
	_, err = api.UpdateAIProvider(ctx, "p-resend", same, nil)
	require.NoError(t, err, "the stored value, resent, must not lock the provider out of edits")

	other := &AIProviderUpdateRequest{}
	other.Params = dcgraphql.OptionalString{Set: true, Value: strp(`{"maxTokens":512}`)}
	_, err = api.UpdateAIProvider(ctx, "p-resend", other, nil)
	require.Error(t, err)
	assert.Equal(t, "UNSUPPORTED", codeOf(t, err))
}

func boolp(b bool) *bool { return &b }
