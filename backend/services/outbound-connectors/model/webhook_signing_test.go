// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func signingKeys(t *testing.T, api *Api, tenant, handle string) []string {
	t.Helper()
	keys, err := webhooksig.ResolveKeys(context.Background(), WebhookSigningBase(handle),
		func(c context.Context, name string) (string, error) {
			v, err := api.Secrets.Resolve(c, secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: name})
			return string(v), err
		})
	if err != nil {
		return nil
	}
	return keys
}

func TestWebhookSigningSecretLifecycle(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	st, err := api.WebhookSigningSecretStatus(ctx, "hooks/prod")
	require.NoError(t, err)
	assert.Nil(t, st, "no key, no status")

	first, err := api.GenerateWebhookSigningSecret(ctx, "hooks/prod")
	require.NoError(t, err)
	require.NoError(t, webhooksig.ValidateSecret(first.Secret))
	assert.Equal(t, []string{first.Secret}, signingKeys(t, api, "acme", "hooks/prod"))

	_, err = api.GenerateWebhookSigningSecret(ctx, "hooks/prod")
	assert.True(t, conflict.Is(err), "generate never replaces: %v", err)
	assert.Equal(t, []string{first.Secret}, signingKeys(t, api, "acme", "hooks/prod"))

	second, err := api.RotateWebhookSigningSecret(ctx, "hooks/prod")
	require.NoError(t, err)
	assert.True(t, second.Rotating)
	assert.Equal(t, []string{second.Secret, first.Secret}, signingKeys(t, api, "acme", "hooks/prod"))
	st, err = api.WebhookSigningSecretStatus(ctx, "hooks/prod")
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.True(t, st.Rotating)

	st, err = api.EndWebhookSigningRotation(ctx, "hooks/prod")
	require.NoError(t, err)
	assert.False(t, st.Rotating)
	assert.Equal(t, []string{second.Secret}, signingKeys(t, api, "acme", "hooks/prod"))

	existed, err := api.DeleteWebhookSigningSecret(ctx, "hooks/prod")
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Nil(t, signingKeys(t, api, "acme", "hooks/prod"))
	existed, err = api.DeleteWebhookSigningSecret(ctx, "hooks/prod")
	require.NoError(t, err)
	assert.False(t, existed, "delete is idempotent")
}

func TestWebhookSigningSecretRefusals(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.RotateWebhookSigningSecret(ctx, "h")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.EndWebhookSigningRotation(ctx, "h")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	for _, bad := range []string{"", "/abs", "a//b", "a/../b", "has space", "x\ny"} {
		_, err := api.GenerateWebhookSigningSecret(ctx, bad)
		assert.Error(t, err, "%q", bad)
	}
	_, err = api.GenerateWebhookSigningSecret(context.Background(), "h")
	assert.ErrorIs(t, err, core.ErrNoTenant)

	// Tenant isolation: the same handle in another tenant is a different secret.
	_, err = api.GenerateWebhookSigningSecret(ctx, "h")
	require.NoError(t, err)
	assert.Nil(t, signingKeys(t, api, "globex", "h"))
	st, err := api.WebhookSigningSecretStatus(core.WithTenant(context.Background(), "globex"), "h")
	require.NoError(t, err)
	assert.Nil(t, st)
}

// The namespace keeps a signing handle off every other secret: the Bearer secret an httpCall
// names, and a connector credential, live outside webhook-signing/.
func TestWebhookSigningBaseIsNamespaced(t *testing.T) {
	assert.Equal(t, "webhook-signing/hooks/prod", WebhookSigningBase("hooks/prod"))
	assert.NotEqual(t, ConnectorSecretName(7), WebhookSigningBase("connector/7"))
}
