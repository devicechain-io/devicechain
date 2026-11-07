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

// webhookSigningPrefix namespaces signing secrets inside a tenant's secret store, apart from
// connector credentials ("connector/<id>") and the Bearer secrets an httpCall action names, so no
// handle a tenant chooses for one can address another.
const webhookSigningPrefix = webhooksig.ReservedPrefix

// WebhookSigningBase is the secret-store base name for a signing handle: the two key slots (see
// core/webhooksig) live beneath it. The httpCall executor resolves the same base from the handle a
// rule carries, so the write path and the delivery path cannot disagree about where a key is.
func WebhookSigningBase(handle string) string { return webhookSigningPrefix + handle }

// ErrNoWebhookSigningSecret is returned by a rotate or end-rotation over a handle with no key.
func ErrNoWebhookSigningSecret(handle string) error {
	return fmt.Errorf("%w: no webhook signing secret exists for handle %q", gorm.ErrRecordNotFound, handle)
}

// WebhookSigningStatus is what a reader may learn about a signing secret: that it exists and
// whether a rotation is open. The key itself is never readable after it is issued.
type WebhookSigningStatus struct {
	Handle   string
	Rotating bool
}

// IssuedWebhookSigningSecret is the one response that carries a key. It is returned exactly once,
// by the mutation that minted it; nothing can read it back.
type IssuedWebhookSigningSecret struct {
	Handle   string
	Secret   string
	Rotating bool
}

func (api *Api) keyring() webhooksig.Keyring { return webhooksig.Keyring{Store: api.Secrets} }

func signingTenant(ctx context.Context, handle string) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", core.ErrNoTenant
	}
	if err := webhooksig.ValidateHandle(handle); err != nil {
		return "", fmt.Errorf("invalid handle: %w", err)
	}
	return tenant, nil
}

// GenerateWebhookSigningSecret mints the first signing key for handle and returns it once. It
// never replaces an existing key: a handle already in use is a conflict, and replacing is
// RotateWebhookSigningSecret, which keeps the old key valid for the overlap.
func (api *Api) GenerateWebhookSigningSecret(ctx context.Context, handle string) (*IssuedWebhookSigningSecret, error) {
	tenant, err := signingTenant(ctx, handle)
	if err != nil {
		return nil, err
	}
	secret, err := api.keyring().Create(ctx, tenant, WebhookSigningBase(handle))
	if errors.Is(err, webhooksig.ErrSigningSecretExists) {
		return nil, conflict.Errorf("a webhook signing secret already exists for handle %q: rotate it to replace it", handle)
	}
	if err != nil {
		return nil, err
	}
	return &IssuedWebhookSigningSecret{Handle: handle, Secret: secret}, nil
}

// RotateWebhookSigningSecret installs a new key and returns it once. The previous key keeps
// signing alongside it until EndWebhookSigningRotation, so a receiver can move at its own pace.
func (api *Api) RotateWebhookSigningSecret(ctx context.Context, handle string) (*IssuedWebhookSigningSecret, error) {
	tenant, err := signingTenant(ctx, handle)
	if err != nil {
		return nil, err
	}
	secret, err := api.keyring().Rotate(ctx, tenant, WebhookSigningBase(handle))
	if errors.Is(err, webhooksig.ErrNoSigningSecret) {
		return nil, ErrNoWebhookSigningSecret(handle)
	}
	if err != nil {
		return nil, err
	}
	return &IssuedWebhookSigningSecret{Handle: handle, Secret: secret, Rotating: true}, nil
}

// EndWebhookSigningRotation drops the previous key, so deliveries carry one signature again.
func (api *Api) EndWebhookSigningRotation(ctx context.Context, handle string) (*WebhookSigningStatus, error) {
	tenant, err := signingTenant(ctx, handle)
	if err != nil {
		return nil, err
	}
	kr := api.keyring()
	exists, _, err := kr.Status(ctx, tenant, WebhookSigningBase(handle))
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNoWebhookSigningSecret(handle)
	}
	if err := kr.EndRotation(ctx, tenant, WebhookSigningBase(handle)); err != nil {
		return nil, err
	}
	return &WebhookSigningStatus{Handle: handle}, nil
}

// DeleteWebhookSigningSecret removes both keys and reports whether there was one. A rule still
// naming the handle then has its deliveries refused (never sent unsigned) until a key is
// generated again.
func (api *Api) DeleteWebhookSigningSecret(ctx context.Context, handle string) (bool, error) {
	tenant, err := signingTenant(ctx, handle)
	if err != nil {
		return false, err
	}
	kr := api.keyring()
	exists, _, err := kr.Status(ctx, tenant, WebhookSigningBase(handle))
	if err != nil {
		return false, err
	}
	if err := kr.Delete(ctx, tenant, WebhookSigningBase(handle)); err != nil {
		return false, err
	}
	return exists, nil
}

// WebhookSigningSecretStatus reports whether handle has a key and whether a rotation is open;
// nil when it has none.
func (api *Api) WebhookSigningSecretStatus(ctx context.Context, handle string) (*WebhookSigningStatus, error) {
	tenant, err := signingTenant(ctx, handle)
	if err != nil {
		return nil, err
	}
	exists, rotating, err := api.keyring().Status(ctx, tenant, WebhookSigningBase(handle))
	if err != nil || !exists {
		return nil, err
	}
	return &WebhookSigningStatus{Handle: handle, Rotating: rotating}, nil
}
