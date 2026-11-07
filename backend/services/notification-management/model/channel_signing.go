// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/webhooksig"
	"gorm.io/gorm"
)

// ChannelSigningBase is the secret-store base name of a webhook channel's signing secret:
// "channel/{id}/signing". Like ChannelSecretName it is keyed by the channel's immutable id, so a
// rename keeps the key bound, and the two key slots (see core/webhooksig) live beneath it. The
// delivery adapter resolves the same base, so the write path and the delivery path cannot
// disagree about where a key is.
func ChannelSigningBase(id uint) string { return fmt.Sprintf("channel/%d/signing", id) }

// IssuedChannelSigningSecret is the one response that carries a key, returned once by the
// mutation that minted it.
type IssuedChannelSigningSecret struct {
	Secret   string
	Rotating bool
}

// ChannelSigningStatus is what a reader may learn about a channel's signing secret.
type ChannelSigningStatus struct {
	Exists   bool
	Rotating bool
}

func (api *Api) channelKeyring() webhooksig.Keyring { return webhooksig.Keyring{Store: api.Secrets} }

// webhookChannelForSigning loads the channel a signing operation targets and the tenant it acts
// for. Only a webhook channel signs: an SMTP channel has no request to sign, and minting a key
// for one would be a secret that nothing could ever use.
func (api *Api) webhookChannelForSigning(ctx context.Context, token string) (*NotificationChannel, string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, "", core.ErrNoTenant
	}
	matches, err := api.NotificationChannelsByToken(ctx, []string{token})
	if err != nil {
		return nil, "", err
	}
	if len(matches) == 0 {
		return nil, "", gorm.ErrRecordNotFound
	}
	if matches[0].ChannelType != ChannelTypeWebhook {
		return nil, "", fmt.Errorf("notification channel %q is a %s channel; only a webhook channel can sign its deliveries",
			token, matches[0].ChannelType)
	}
	return matches[0], tenant, nil
}

func errNoChannelSigningSecret(token string) error {
	return fmt.Errorf("%w: notification channel %q has no signing secret", gorm.ErrRecordNotFound, token)
}

// GenerateChannelSigningSecret mints the first signing key for a webhook channel and returns it
// once. It never replaces a key (a conflict); replacing is RotateChannelSigningSecret. Minting a
// key does not turn signing on: the channel config's "signing" flag does, so a key can be issued
// and handed to the receiver before the first signed delivery.
func (api *Api) GenerateChannelSigningSecret(ctx context.Context, token string) (*IssuedChannelSigningSecret, error) {
	ch, tenant, err := api.webhookChannelForSigning(ctx, token)
	if err != nil {
		return nil, err
	}
	secret, err := api.channelKeyring().Create(ctx, tenant, ChannelSigningBase(ch.ID))
	if errors.Is(err, webhooksig.ErrSigningSecretExists) {
		return nil, conflict.Errorf("notification channel %q already has a signing secret: rotate it to replace it", token)
	}
	if err != nil {
		return nil, err
	}
	return &IssuedChannelSigningSecret{Secret: secret}, nil
}

// RotateChannelSigningSecret installs a new key and returns it once. The previous key keeps
// signing alongside it until EndChannelSigningRotation.
func (api *Api) RotateChannelSigningSecret(ctx context.Context, token string) (*IssuedChannelSigningSecret, error) {
	ch, tenant, err := api.webhookChannelForSigning(ctx, token)
	if err != nil {
		return nil, err
	}
	secret, err := api.channelKeyring().Rotate(ctx, tenant, ChannelSigningBase(ch.ID))
	if errors.Is(err, webhooksig.ErrNoSigningSecret) {
		return nil, errNoChannelSigningSecret(token)
	}
	if err != nil {
		return nil, err
	}
	return &IssuedChannelSigningSecret{Secret: secret, Rotating: true}, nil
}

// EndChannelSigningRotation drops the previous key, so deliveries carry one signature again.
func (api *Api) EndChannelSigningRotation(ctx context.Context, token string) (*NotificationChannel, error) {
	ch, tenant, err := api.webhookChannelForSigning(ctx, token)
	if err != nil {
		return nil, err
	}
	kr := api.channelKeyring()
	exists, _, err := kr.Status(ctx, tenant, ChannelSigningBase(ch.ID))
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errNoChannelSigningSecret(token)
	}
	if err := kr.EndRotation(ctx, tenant, ChannelSigningBase(ch.ID)); err != nil {
		return nil, err
	}
	return ch, nil
}

// DeleteChannelSigningSecret removes both keys. A channel whose config still says "signing": true
// then has its deliveries refused rather than sent unsigned, until a key is generated again.
func (api *Api) DeleteChannelSigningSecret(ctx context.Context, token string) (*NotificationChannel, error) {
	ch, tenant, err := api.webhookChannelForSigning(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := api.channelKeyring().Delete(ctx, tenant, ChannelSigningBase(ch.ID)); err != nil {
		return nil, err
	}
	return ch, nil
}

// ChannelSigningState reports whether the channel holds a signing key and whether a rotation is
// open, without exposing either key.
func (api *Api) ChannelSigningState(ctx context.Context, id uint) (ChannelSigningStatus, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return ChannelSigningStatus{}, core.ErrNoTenant
	}
	exists, rotating, err := api.channelKeyring().Status(ctx, tenant, ChannelSigningBase(id))
	return ChannelSigningStatus{Exists: exists, Rotating: rotating}, err
}
