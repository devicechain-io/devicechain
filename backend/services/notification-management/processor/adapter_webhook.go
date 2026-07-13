// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// webhookAdapter delivers a notification by POSTing its rendered JSON payload to an
// HTTP endpoint (ADR-017). A Slack incoming-webhook URL is one such endpoint — the
// payload carries a "text" field, which is what Slack renders — so Slack rides this
// adapter rather than a vendor SDK. The channel's Config holds the URL/method/headers;
// the channel's Secret (optional) is an auth token added as a header. The outbound
// hardening (no-redirect client, reserved-header dropping, response-body suppression)
// lives in core/httpsink, shared with the ADR-060 connector sinks.
type webhookAdapter struct {
	// client is the HTTP client used for delivery; nil uses httpsink.DefaultClient. It
	// is a field so a test can inject a client pointed at an httptest server. The
	// context deadline (not a client Timeout) bounds each request.
	client *http.Client
}

// webhookConfig is the webhook channel's non-secret settings. AuthHeader/AuthScheme
// control how the channel's Secret is presented: by default the secret goes out as
// "Authorization: Bearer <secret>"; set AuthScheme to "" and AuthHeader to a custom
// header (e.g. "X-API-Key") to send the raw token instead.
type webhookConfig struct {
	URL        string            `json:"url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	AuthHeader string            `json:"authHeader"`
	AuthScheme string            `json:"authScheme"`
}

// Deliver POSTs the rendered payload as JSON to the configured endpoint. recipients
// is ignored for a webhook (the endpoint is the destination), but a rule may still
// carry recipients for a downstream consumer, so they are threaded into the payload.
func (a *webhookAdapter) Deliver(ctx context.Context, channel *model.NotificationChannel,
	secret string, recipients []string, msg *RenderedNotification) error {
	cfg, err := parseWebhookConfig(channel)
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
		Auth:    httpsink.Auth{Header: cfg.AuthHeader, Scheme: cfg.AuthScheme},
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

// parseWebhookConfig unmarshals and defaults/validates the channel's webhook config.
// The http/https scheme guard is shared with the connector sinks (httpsink.ValidateURL);
// webhook delivery is POST-only, since an unbounded method choice widens the SSRF
// surface (a tenant-authored PUT/DELETE against an internal service) for no benefit.
func parseWebhookConfig(channel *model.NotificationChannel) (*webhookConfig, error) {
	cfg := &webhookConfig{}
	if channel.Config != nil {
		if err := json.Unmarshal([]byte(*channel.Config), cfg); err != nil {
			return nil, fmt.Errorf("webhook channel %q has invalid config: %w", channel.Token, err)
		}
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("webhook channel %q config is missing url", channel.Token)
	}
	if _, err := httpsink.ValidateURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("webhook channel %q has an %w", channel.Token, err)
	}
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	if cfg.Method != http.MethodPost {
		return nil, fmt.Errorf("webhook channel %q method %q is not supported (POST only)", channel.Token, cfg.Method)
	}
	return cfg, nil
}
