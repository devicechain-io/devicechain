// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// webhookAdapter delivers a notification by POSTing its rendered JSON payload to an
// HTTP endpoint (ADR-017). A Slack incoming-webhook URL is one such endpoint — the
// payload carries a "text" field, which is what Slack renders — so Slack rides this
// adapter rather than a vendor SDK. The channel's Config holds the URL/method/headers and
// states `auth` (none | bearer | header); the channel's Secret must agree with it, and
// httpsink.Send refuses a channel whose secret does not (terminally — see deliverWithRetry).
// The parser is model.ParseWebhookConfig, shared with channel create/update. The outbound
// hardening (no-redirect client, reserved-header dropping, response-body suppression)
// lives in core/httpsink, shared with the ADR-060 connector sinks.
type webhookAdapter struct {
	// client is the HTTP client used for delivery; nil uses httpsink.DefaultClient. It
	// is a field so a test can inject a client pointed at an httptest server. The
	// context deadline (not a client Timeout) bounds each request.
	client *http.Client
	// store holds the channel signing keys. Read only when a channel's config sets signing; nil
	// means no channel can sign.
	store secrets.SecretStore
}

// Deliver POSTs the rendered payload as JSON to the configured endpoint. recipients
// is ignored for a webhook (the endpoint is the destination), but a rule may still
// carry recipients for a downstream consumer, so they are threaded into the payload.
func (a *webhookAdapter) Deliver(ctx context.Context, channel *model.NotificationChannel,
	secret string, recipients []string, msg *RenderedNotification) error {
	cfg, err := model.ParseWebhookConfig(channel.Token, dcgraphql.MetadataStr(channel.Config))
	if err != nil {
		return err
	}

	body, err := json.Marshal(webhookBody(recipients, msg))
	if err != nil {
		return fmt.Errorf("webhook channel %q marshal payload: %w", channel.Token, err)
	}

	// Pre-filter reserved headers with a warning so a misconfigured channel is visible;
	// httpsink.Send drops them again defensively, so the security invariant holds even
	// if this warning path is ever removed.
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		if httpsink.IsReservedHeader(k) {
			log.Warn().Str("channel", channel.Token).Str("header", k).Msg("Ignoring reserved webhook header from config")
			continue
		}
		headers[k] = v
	}

	var signing *httpsink.Signing
	if cfg.Signing {
		keys, err := a.signingKeys(ctx, channel)
		if err != nil {
			return err
		}
		// The id comes from the dispatcher, stable across this delivery's retries; the timestamp
		// is stamped per attempt inside Send.
		signing = &httpsink.Signing{ID: messageIDFrom(ctx), Secrets: keys}
	}

	if err := httpsink.Send(ctx, a.client, httpsink.Request{
		URL:     cfg.URL,
		Method:  cfg.Method,
		Headers: headers,
		Body:    body,
		Signing: signing,
		Secret:  secret,
		// No adapter-side secret check: Send judges the declared mode against the secret for
		// every caller, before a request exists, and that is where the rule lives.
		Auth: cfg.HTTPAuth(),
	}); err != nil {
		return fmt.Errorf("webhook channel %q: %w", channel.Token, err)
	}
	return nil
}

// signingKeys loads the keys a signing channel signs with: the current key, plus the previous one
// while a rotation is open. A channel with no usable key is httpsink.ErrSigningRefused, which the
// dispatcher treats as terminal; a store failure is returned as is, so it is retried.
func (a *webhookAdapter) signingKeys(ctx context.Context, channel *model.NotificationChannel) ([]string, error) {
	if a.store == nil {
		return nil, fmt.Errorf("webhook channel %q: %w: no secret store is configured", channel.Token, httpsink.ErrSigningRefused)
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, fmt.Errorf("webhook channel %q: %w", channel.Token, core.ErrNoTenant)
	}
	sctx, cancel := context.WithTimeout(ctx, secretResolveTimeout)
	defer cancel()
	keys, err := webhooksig.ResolveKeys(sctx, model.ChannelSigningBase(channel.ID),
		func(ctx context.Context, name string) (string, error) {
			v, err := a.store.Resolve(ctx, secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: name})
			return string(v), err
		})
	switch {
	case err == nil:
		return keys, nil
	case errors.Is(err, webhooksig.ErrNoSigningSecret), errors.Is(err, webhooksig.ErrInvalidSecret):
		return nil, fmt.Errorf("webhook channel %q: %w: %w", channel.Token, httpsink.ErrSigningRefused, err)
	default:
		return nil, fmt.Errorf("webhook channel %q: resolve signing secret: %w", channel.Token, err)
	}
}

// webhookBody adds the rule's recipients to the rendered payload without mutating the
// shared RenderedNotification (the same render is delivered to several channels).
func webhookBody(recipients []string, msg *RenderedNotification) map[string]any {
	body := make(map[string]any, len(msg.Payload)+1)
	for k, v := range msg.Payload {
		body[k] = v
	}
	if len(recipients) > 0 {
		body["recipients"] = recipients
	}
	return body
}
