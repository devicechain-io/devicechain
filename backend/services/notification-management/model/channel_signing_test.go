// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func webhookChannel(t *testing.T, api *Api, ctx context.Context, token string) *NotificationChannel {
	t.Helper()
	ch, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: token, ChannelType: ChannelTypeWebhook, Enabled: true,
		Config: strPtr(`{"url":"https://hooks.example.com/in","auth":"none","signing":true}`),
	})
	require.NoError(t, err)
	return ch
}

func keysOf(t *testing.T, api *Api, ctx context.Context, id uint) []string {
	t.Helper()
	tenant := "A"
	keys, err := webhooksig.ResolveKeys(ctx, ChannelSigningBase(id), func(c context.Context, name string) (string, error) {
		v, err := api.Secrets.Resolve(c, secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: name})
		return string(v), err
	})
	if err != nil {
		return nil
	}
	return keys
}

func TestChannelSigningLifecycle(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	ch := webhookChannel(t, api, ctx, "hook")

	st, err := api.ChannelSigningState(ctx, ch.ID)
	require.NoError(t, err)
	assert.False(t, st.Exists)

	first, err := api.GenerateChannelSigningSecret(ctx, "hook")
	require.NoError(t, err)
	require.NoError(t, webhooksig.ValidateSecret(first.Secret))
	assert.False(t, first.Rotating)
	assert.Equal(t, []string{first.Secret}, keysOf(t, api, ctx, ch.ID), "stored sealed and resolvable server-side")

	_, err = api.GenerateChannelSigningSecret(ctx, "hook")
	assert.True(t, conflict.Is(err), "a second generate never replaces the key: %v", err)
	assert.Equal(t, []string{first.Secret}, keysOf(t, api, ctx, ch.ID))

	second, err := api.RotateChannelSigningSecret(ctx, "hook")
	require.NoError(t, err)
	assert.True(t, second.Rotating)
	assert.NotEqual(t, first.Secret, second.Secret)
	assert.Equal(t, []string{second.Secret, first.Secret}, keysOf(t, api, ctx, ch.ID), "both keys sign during the overlap")
	st, _ = api.ChannelSigningState(ctx, ch.ID)
	assert.True(t, st.Exists)
	assert.True(t, st.Rotating)

	_, err = api.EndChannelSigningRotation(ctx, "hook")
	require.NoError(t, err)
	assert.Equal(t, []string{second.Secret}, keysOf(t, api, ctx, ch.ID))
	st, _ = api.ChannelSigningState(ctx, ch.ID)
	assert.False(t, st.Rotating)

	_, err = api.DeleteChannelSigningSecret(ctx, "hook")
	require.NoError(t, err)
	assert.Nil(t, keysOf(t, api, ctx, ch.ID))
	st, _ = api.ChannelSigningState(ctx, ch.ID)
	assert.False(t, st.Exists)
}

func TestChannelSigningRefusalsAndScoping(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	webhookChannel(t, api, ctx, "hook")

	_, err := api.RotateChannelSigningSecret(ctx, "hook")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "nothing to rotate")
	_, err = api.EndChannelSigningRotation(ctx, "hook")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.GenerateChannelSigningSecret(ctx, "nope")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	_, err = api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "mail", ChannelType: ChannelTypeSMTP, Enabled: true,
		Config: strPtr(`{"host":"smtp.example.com","port":587}`),
	})
	require.NoError(t, err)
	_, err = api.GenerateChannelSigningSecret(ctx, "mail")
	require.Error(t, err, "an SMTP channel has nothing to sign")
	assert.Contains(t, err.Error(), "webhook")

	// Another tenant cannot see or mint a key for this tenant's channel.
	other := tenantCtx("B")
	_, err = api.GenerateChannelSigningSecret(other, "hook")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	_, err = api.GenerateChannelSigningSecret(context.Background(), "hook")
	assert.Error(t, err, "fails closed with no tenant")
}

// Deleting a channel removes its signing keys, and renaming it keeps them bound (the key is
// keyed by the immutable id).
func TestChannelSigningSurvivesRenameAndDiesWithTheChannel(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	ch := webhookChannel(t, api, ctx, "hook")
	issued, err := api.GenerateChannelSigningSecret(ctx, "hook")
	require.NoError(t, err)

	_, err = api.RenameNotificationChannel(ctx, "hook", "hook-renamed")
	require.NoError(t, err)
	assert.Equal(t, []string{issued.Secret}, keysOf(t, api, ctx, ch.ID))

	deleted, err := api.DeleteNotificationChannel(ctx, "hook-renamed")
	require.NoError(t, err)
	require.True(t, deleted)
	assert.Nil(t, keysOf(t, api, ctx, ch.ID))
}

// The config flag is accepted and round-trips; a channel without it parses as unsigned.
func TestWebhookConfigSigningFlag(t *testing.T) {
	signed, err := ParseWebhookConfig("c", strPtr(`{"url":"https://h.example/x","auth":"none","signing":true}`))
	require.NoError(t, err)
	assert.True(t, signed.Signing)
	unsigned, err := ParseWebhookConfig("c", strPtr(`{"url":"https://h.example/x","auth":"none"}`))
	require.NoError(t, err)
	assert.False(t, unsigned.Signing)
}

// A signed channel whose credential header is a signing header is refused when it is saved; the
// same auth on an unsigned channel, and a different header on a signed one, are accepted.
func TestWebhookConfigRefusesSigningOverItsOwnAuthHeader(t *testing.T) {
	for _, h := range []string{"webhook-signature", "Webhook-Id", "webhook-timestamp"} {
		_, err := ParseWebhookConfig("c", strPtr(`{"url":"https://h.example/x","auth":"header","authHeader":"`+h+`","signing":true}`))
		assert.ErrorIs(t, err, httpsink.ErrSigningRefused, h)
		_, err = ParseWebhookConfig("c", strPtr(`{"url":"https://h.example/x","auth":"header","authHeader":"`+h+`"}`))
		assert.NoError(t, err, h+" unsigned")
	}
	_, err := ParseWebhookConfig("c", strPtr(`{"url":"https://h.example/x","auth":"header","authHeader":"X-Api-Key","signing":true}`))
	assert.NoError(t, err)
}
