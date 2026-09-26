// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/httpsink"
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

	if err := httpsink.Send(ctx, a.client, httpsink.Request{
		URL:     cfg.URL,
		Method:  cfg.Method,
		Headers: headers,
		Body:    body,
		Secret:  secret,
		// No adapter-side secret check: Send judges the declared mode against the secret for
		// every caller, before a request exists, and that is where the rule lives.
		Auth: cfg.HTTPAuth(),
	}); err != nil {
		return fmt.Errorf("webhook channel %q: %w", channel.Token, err)
	}
	return nil
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
