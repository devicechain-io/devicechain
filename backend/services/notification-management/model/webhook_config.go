// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/devicechain-io/dc-microservice/httpsink"
)

// WebhookAuth is a webhook channel's declared auth mode. It is REQUIRED in config: a missing
// value is refused, never defaulted, because a default would reintroduce the ambiguity this
// field ends — a channel with no secret could not say whether it was meant to be anonymous
// or had lost its credential.
type WebhookAuth string

const (
	// WebhookAuthNone presents no credential: the URL carries it (a Slack incoming
	// webhook), or the endpoint is open.
	WebhookAuthNone WebhookAuth = "none"
	// WebhookAuthBearer presents the channel's secret as "Authorization: Bearer <secret>".
	WebhookAuthBearer WebhookAuth = "bearer"
	// WebhookAuthHeader presents the channel's secret as "<authHeader>: [<authScheme> ]<secret>".
	WebhookAuthHeader WebhookAuth = "header"
)

// WebhookConfig is the webhook channel's non-secret settings.
type WebhookConfig struct {
	URL        string            `json:"url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	Auth       WebhookAuth       `json:"auth"`
	AuthHeader string            `json:"authHeader"`
	AuthScheme string            `json:"authScheme"`
}

// ParseWebhookConfig unmarshals and validates a webhook channel's config; token names the
// channel in errors, and config may be nil. It is THE webhook parser: channel create and
// update call it (through validateChannelCredential) and so does delivery, so what is
// accepted at save time and what is sent cannot drift apart.
//
// The http/https scheme guard is shared with the connector sinks (httpsink.ValidateURL);
// webhook delivery is POST-only, since an unbounded method choice widens the SSRF surface
// (a tenant-authored PUT/DELETE against an internal service) for no benefit.
func ParseWebhookConfig(token string, config *string) (*WebhookConfig, error) {
	cfg := &WebhookConfig{}
	if config != nil {
		if err := json.Unmarshal([]byte(*config), cfg); err != nil {
			return nil, fmt.Errorf("webhook channel %q has invalid config: %w", token, err)
		}
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("webhook channel %q config is missing url", token)
	}
	if _, err := httpsink.ValidateURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("webhook channel %q has an %w", token, err)
	}
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	if cfg.Method != http.MethodPost {
		return nil, fmt.Errorf("webhook channel %q method %q is not supported (POST only)", token, cfg.Method)
	}
	switch cfg.Auth {
	case "":
		return nil, fmt.Errorf("webhook channel %q config is missing auth: set \"none\" if the URL itself "+
			"carries the credential (a Slack incoming webhook), or \"bearer\" or \"header\" to present the "+
			"channel's secret: %w", token, httpsink.ErrAuthModeUnstated)
	case WebhookAuthNone, WebhookAuthBearer, WebhookAuthHeader:
	default:
		return nil, fmt.Errorf("webhook channel %q auth %q is not one of none, bearer, header: %w",
			token, cfg.Auth, httpsink.ErrAuthRefused)
	}
	// authHeader is written to the wire AFTER httpsink's reserved-header drop loop, so an
	// X-DC-* name would reach the request having passed through no filter at all. Validate
	// refuses it — and, because this parser runs when the channel is saved as well as when it
	// delivers, a bad authHeader is now refused at save time, with a message that names the
	// field. httpsink.Send checks it again at the point of use, for a row saved before either
	// check existed.
	if err := cfg.HTTPAuth().Validate(); err != nil {
		return nil, fmt.Errorf("webhook channel %q has an invalid auth configuration: %w", token, err)
	}
	return cfg, nil
}

// HTTPAuth maps the declared mode onto httpsink's. Call it only on a parsed config.
//
// Header and Scheme are carried through under EVERY mode, not only "header": under none or
// bearer httpsink.Auth.Validate then refuses them, where dropping them here would silently
// ignore a field the tenant set and believes is in effect.
func (c *WebhookConfig) HTTPAuth() httpsink.Auth {
	auth := httpsink.Auth{Header: c.AuthHeader, Scheme: c.AuthScheme}
	switch c.Auth {
	case WebhookAuthNone:
		auth.Mode = httpsink.AuthNone
	case WebhookAuthBearer:
		auth.Mode = httpsink.AuthBearer
	case WebhookAuthHeader:
		auth.Mode = httpsink.AuthHeader
	}
	return auth
}

// validateChannelCredential refuses a channel whose declared auth and secret presence
// disagree. It parses the FULL webhook config, so a bad url or method is refused at save
// time too. Non-webhook types pass: SMTP's credential rule (a username needs a secret) is
// judged by its adapter, before it dials.
func validateChannelCredential(token, channelType string, config *string, hasSecret bool) error {
	if channelType != ChannelTypeWebhook {
		return nil
	}
	cfg, err := ParseWebhookConfig(token, config)
	if err != nil {
		return err
	}
	switch err := cfg.HTTPAuth().CheckCredential(hasSecret); {
	case errors.Is(err, httpsink.ErrMissingCredential):
		return fmt.Errorf("webhook channel %q declares auth %q but has no secret: set secret, or set auth "+
			"to \"none\" if the URL itself carries the credential: %w", token, cfg.Auth, err)
	case errors.Is(err, httpsink.ErrUnexpectedCredential):
		return fmt.Errorf("webhook channel %q declares auth \"none\" but has a secret it would never "+
			"present: clear secret (null), or declare \"bearer\" or \"header\": %w", token, err)
	default:
		return err
	}
}
