// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// webhookAdapter delivers a notification by POSTing its rendered JSON payload to an
// HTTP endpoint (ADR-017). A Slack incoming-webhook URL is one such endpoint — the
// payload carries a "text" field, which is what Slack renders — so Slack rides this
// adapter rather than a vendor SDK. The channel's Config holds the URL/method/headers;
// the channel's Secret (optional) is an auth token added as a header.
type webhookAdapter struct {
	// client is the HTTP client used for delivery; nil uses http.DefaultClient. It is
	// a field so a test can inject a client pointed at an httptest server. The context
	// deadline (not a client Timeout) bounds each request.
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
	recipients []string, msg *RenderedNotification) error {
	cfg, err := parseWebhookConfig(channel)
	if err != nil {
		return err
	}

	body, err := json.Marshal(webhookBody(recipients, msg))
	if err != nil {
		return fmt.Errorf("webhook channel %q marshal payload: %w", channel.Token, err)
	}
	req, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook channel %q build request: %w", channel.Token, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range cfg.Headers {
		// Tenant-authored headers must not forge the auth or the internal tenant
		// identity: drop Authorization (we set it from the channel secret) and any
		// X-DC-* header (the internal service-to-service tenant/service headers), so a
		// webhook cannot be pointed at an internal service with a spoofed identity.
		if isReservedHeader(k) {
			log.Warn().Str("channel", channel.Token).Str("header", k).Msg("Ignoring reserved webhook header from config")
			continue
		}
		req.Header.Set(k, v)
	}
	if channel.Secret.Valid && channel.Secret.String != "" {
		name, value := authHeader(cfg, channel.Secret.String)
		req.Header.Set(name, value)
	}

	client := a.client
	if client == nil {
		client = defaultWebhookClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook channel %q POST %s: %w", channel.Token, cfg.URL, err)
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A hostile endpoint could reflect the Authorization header back in its
		// response body, so never surface the body of a channel that carries a secret
		// (it would leak the write-only secret into logs).
		if channel.Secret.Valid && channel.Secret.String != "" {
			return fmt.Errorf("webhook channel %q returned %d", channel.Token, resp.StatusCode)
		}
		return fmt.Errorf("webhook channel %q returned %d: %s", channel.Token, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// defaultWebhookClient is the production HTTP client for webhook delivery. It does
// NOT follow redirects: an external endpoint must not be able to 302 the request onto
// an internal target (an SSRF bypass of the endpoint the tenant configured). A 3xx is
// returned as-is and treated as a non-2xx failure. The context deadline bounds each
// request, so no client Timeout is set.
var defaultWebhookClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// isReservedHeader reports whether a config-supplied header name is one the adapter
// controls or that carries internal identity, and so must not be settable by tenant
// config.
func isReservedHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	return canonical == "Authorization" || strings.HasPrefix(canonical, "X-Dc-")
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

// authHeader resolves the header name/value carrying the channel secret.
func authHeader(cfg *webhookConfig, secret string) (string, string) {
	name := cfg.AuthHeader
	if name == "" {
		name = "Authorization"
	}
	scheme := cfg.AuthScheme
	if cfg.AuthHeader == "" && cfg.AuthScheme == "" {
		// Default Authorization header defaults to a Bearer scheme.
		scheme = "Bearer"
	}
	if scheme != "" {
		return name, scheme + " " + secret
	}
	return name, secret
}

// parseWebhookConfig unmarshals and defaults/validates the channel's webhook config.
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
	parsed, err := url.Parse(cfg.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("webhook channel %q has an invalid url %q (want http/https)", channel.Token, cfg.URL)
	}
	// Webhook delivery is POST-only: an unbounded method choice widens the SSRF
	// surface (a tenant-authored PUT/DELETE against an internal service) for no
	// delivery benefit. Default and only accept POST.
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	if cfg.Method != http.MethodPost {
		return nil, fmt.Errorf("webhook channel %q method %q is not supported (POST only)", channel.Token, cfg.Method)
	}
	return cfg, nil
}
