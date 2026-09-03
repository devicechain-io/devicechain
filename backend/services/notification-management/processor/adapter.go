// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-notification-management/model"
)

// ChannelAdapter delivers one rendered notification through a concrete transport
// (ADR-017): the SMTP sender, the webhook poster. The dispatcher resolves the adapter
// for a channel's type and calls Deliver; keeping delivery behind this seam lets a
// later slice add a transport (SMS, push) without touching the policy-evaluation or
// consumer plumbing.
//
// Deliver MUST bound its own execution to ctx — it runs on a background context
// (drain-on-shutdown), so a hung endpoint would otherwise stall graceful shutdown —
// and return a non-nil error on a failed delivery so the dispatcher can retry it. It
// must NOT retry internally: the dispatcher owns the bounded retry loop so retry
// budget is uniform across transports.
//
// secret is the channel's delivery secret (SMTP password, webhook bearer token),
// resolved from the secret store by the dispatcher server-internal at delivery time
// (ADR-059); it is empty when no secret is configured. It is passed per-call rather
// than carried on the channel so cleartext never lives on the persisted model.
type ChannelAdapter interface {
	Deliver(ctx context.Context, channel *model.NotificationChannel, secret string, recipients []string, msg *RenderedNotification) error
}

// newAdapterRegistry builds the channel-type → adapter map the dispatcher routes
// through. It is the single source of truth for which transports actually deliver; a
// channel whose type is absent here has no adapter and is skipped with a warning
// (its capability flag should still read available=false). Keep this in sync with
// model.SupportedChannelTypes' Available flags.
// guard is the tenant-egress boundary both adapters dial through. It is passed in
// rather than each adapter building its own, so the operator's allowed-destination
// configuration reaches BOTH transports or neither — an escape hatch that applied to
// webhooks and not to SMTP would be worse than none, because it would look like it
// worked.
func newAdapterRegistry(guard *egress.Guard) map[string]ChannelAdapter {
	return map[string]ChannelAdapter{
		model.ChannelTypeSMTP:    &smtpAdapter{guard: guard},
		model.ChannelTypeWebhook: &webhookAdapter{client: egressClient(guard)},
	}
}

// egressClient builds the HTTP client the webhook adapter delivers through: httpsink's
// forced no-redirect policy, over a transport that dials through the guard.
//
// A nil guard falls back to httpsink.DefaultClient, whose own guard carries no
// allowances. That is the fail-closed direction: a wiring mistake here narrows the
// boundary rather than removing it.
func egressClient(guard *egress.Guard) *http.Client {
	if guard == nil {
		return nil
	}
	return &http.Client{Transport: guard.Transport()}
}
