// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/devicechain-io/dc-microservice/httpsink"
)

// This file is the parser and the save-time rules for the NATIVE chat and paging channels
// (Slack, Microsoft Teams, PagerDuty). They differ from the generic webhook in one way that
// shapes everything here: the credential is not a header the tenant chooses, it is part of
// the destination (a Slack or Teams webhook URL IS the credential) or of the body (a
// PagerDuty routing key). So the channel's SECRET always carries it, config holds only
// non-secret settings, and there is no "auth" mode to declare.
//
// Config is decoded STRICTLY (an unknown key is refused): a typo such as "linkBase" would
// otherwise be accepted and silently never take effect, which for a last-mile channel reads
// as a working one.

// LinkConfig is the config shared by the chat channels (Slack, Teams): an optional
// console base URL that the message's "open the alarm" link is built from.
type LinkConfig struct {
	LinkBaseURL string `json:"linkBaseUrl"`
}

// PagerDutyConfig is the PagerDuty channel's non-secret settings.
type PagerDutyConfig struct {
	// Region selects the Events API host: "us" (default) or "eu".
	Region string `json:"region"`
	// Source is the event's source field; it defaults to "devicechain".
	Source      string `json:"source"`
	LinkBaseURL string `json:"linkBaseUrl"`
}

// PagerDuty regions.
const (
	PagerDutyRegionUS = "us"
	PagerDutyRegionEU = "eu"
)

// maxSecretLen bounds a stored native-channel credential. Real webhook URLs and routing
// keys are far shorter; the bound exists so a mistaken paste cannot fill the store.
const maxSecretLen = 2048

// IsNativeChannelType reports whether t is one of the native channels whose credential is
// its secret and whose config this file parses.
func IsNativeChannelType(t string) bool {
	switch t {
	case ChannelTypeSlack, ChannelTypeTeams, ChannelTypePagerDuty:
		return true
	}
	return false
}

func strictDecode(token, kind string, config *string, into any) error {
	if config == nil || strings.TrimSpace(*config) == "" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(*config)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%s channel %q has invalid config: %w", kind, token, err)
	}
	if dec.More() {
		return fmt.Errorf("%s channel %q has invalid config: trailing data after the JSON object", kind, token)
	}
	return nil
}

// normalizeLinkBase validates an optional console base URL and returns it without a
// trailing slash. Empty is allowed (the message then carries no link).
func normalizeLinkBase(token, kind, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if _, err := httpsink.ValidateURL(raw); err != nil {
		return "", fmt.Errorf("%s channel %q linkBaseUrl: %w", kind, token, err)
	}
	return strings.TrimRight(raw, "/"), nil
}

// ParseLinkConfig parses a Slack or Teams channel's config; kind names the type in errors.
func ParseLinkConfig(token, kind string, config *string) (*LinkConfig, error) {
	cfg := &LinkConfig{}
	if err := strictDecode(token, kind, config, cfg); err != nil {
		return nil, err
	}
	base, err := normalizeLinkBase(token, kind, cfg.LinkBaseURL)
	if err != nil {
		return nil, err
	}
	cfg.LinkBaseURL = base
	return cfg, nil
}

// ParsePagerDutyConfig parses a PagerDuty channel's config, applying the region and source
// defaults.
func ParsePagerDutyConfig(token string, config *string) (*PagerDutyConfig, error) {
	cfg := &PagerDutyConfig{}
	if err := strictDecode(token, ChannelTypePagerDuty, config, cfg); err != nil {
		return nil, err
	}
	switch cfg.Region {
	case "":
		cfg.Region = PagerDutyRegionUS
	case PagerDutyRegionUS, PagerDutyRegionEU:
	default:
		return nil, fmt.Errorf("pagerduty channel %q region %q is not one of %s, %s",
			token, cfg.Region, PagerDutyRegionUS, PagerDutyRegionEU)
	}
	if cfg.Source == "" {
		cfg.Source = "devicechain"
	}
	if len(cfg.Source) > 255 {
		return nil, fmt.Errorf("pagerduty channel %q source is longer than 255 characters", token)
	}
	base, err := normalizeLinkBase(token, ChannelTypePagerDuty, cfg.LinkBaseURL)
	if err != nil {
		return nil, err
	}
	cfg.LinkBaseURL = base
	return cfg, nil
}

// ValidateNativeSecret judges the SHAPE of a credential a caller is writing. It never
// echoes the value: the secret is write-only and an error is logged and returned to the
// caller, so quoting it would defeat that.
//
//   - slack and teams: an https URL with a host and no embedded userinfo (the webhook URL);
//   - pagerduty: a routing (integration) key, printable and without whitespace.
func ValidateNativeSecret(token, channelType, secret string) error {
	if len(secret) > maxSecretLen {
		return fmt.Errorf("%s channel %q secret is longer than %d characters", channelType, token, maxSecretLen)
	}
	switch channelType {
	case ChannelTypeSlack, ChannelTypeTeams:
		u, err := url.Parse(secret)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return fmt.Errorf("%s channel %q secret must be the https webhook URL (with a host and no embedded "+
				"credentials)", channelType, token)
		}
	case ChannelTypePagerDuty:
		for _, r := range secret {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return fmt.Errorf("pagerduty channel %q secret must be the routing key, with no whitespace or "+
					"control characters", token)
			}
		}
	}
	return nil
}

// validateNativeChannel is the save-time judgment for a native channel: the config parses,
// a secret will exist, and — when this request carries the value — its shape is right.
// secret is the value being written, or nil when the request preserves the stored one (a
// value that cannot be read back to inspect).
func validateNativeChannel(token, channelType string, config *string, secret *string, hasSecret bool) error {
	switch channelType {
	case ChannelTypeSlack, ChannelTypeTeams:
		if _, err := ParseLinkConfig(token, channelType, config); err != nil {
			return err
		}
	case ChannelTypePagerDuty:
		if _, err := ParsePagerDutyConfig(token, config); err != nil {
			return err
		}
	}
	if !hasSecret {
		return fmt.Errorf("%s channel %q needs a secret (%s): %w", channelType, token,
			nativeSecretHint(channelType), httpsink.ErrMissingCredential)
	}
	if secret != nil && *secret != "" {
		return ValidateNativeSecret(token, channelType, *secret)
	}
	return nil
}

func nativeSecretHint(channelType string) string {
	if channelType == ChannelTypePagerDuty {
		return "the Events API v2 routing key"
	}
	return "the webhook URL"
}
