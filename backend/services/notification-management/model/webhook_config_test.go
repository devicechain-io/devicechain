// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"net/http"
	"testing"

	"github.com/devicechain-io/dc-microservice/httpsink"
)

func parseHook(config string) (*WebhookConfig, error) {
	return ParseWebhookConfig("h", &config)
}

func TestWebhookConfigValidation(t *testing.T) {
	for name, config := range map[string]string{
		"missing url":    `{"auth":"none"}`,
		"invalid scheme": `{"url":"ftp://x","auth":"none"}`,
		"not POST":       `{"url":"https://x/y","method":"DELETE","auth":"none"}`,
		"not JSON":       `{nope`,
	} {
		if _, err := parseHook(config); err == nil {
			t.Errorf("%s: %s was accepted", name, config)
		}
	}
	if _, err := ParseWebhookConfig("h", nil); err == nil {
		t.Error("a nil config was accepted")
	}
	cfg, err := parseHook(`{"url":"https://x/y","auth":"none"}`)
	if err != nil || cfg.Method != http.MethodPost {
		t.Fatalf("default method: cfg=%+v err=%v", cfg, err)
	}
}

// Each mode maps onto the httpsink mode that presents the credential the way the docs say.
func TestWebhookAuthModes(t *testing.T) {
	for config, want := range map[string]httpsink.Auth{
		`{"url":"https://x/y","auth":"none"}`:   {Mode: httpsink.AuthNone},
		`{"url":"https://x/y","auth":"bearer"}`: {Mode: httpsink.AuthBearer},
		`{"url":"https://x/y","auth":"header","authHeader":"X-API-Key"}`: {
			Mode: httpsink.AuthHeader, Header: "X-API-Key"},
		`{"url":"https://x/y","auth":"header","authHeader":"Authorization","authScheme":"Token"}`: {
			Mode: httpsink.AuthHeader, Header: "Authorization", Scheme: "Token"},
	} {
		cfg, err := parseHook(config)
		if err != nil {
			t.Errorf("%s: %v", config, err)
			continue
		}
		if got := cfg.HTTPAuth(); got != want {
			t.Errorf("%s: HTTPAuth = %+v, want %+v", config, got, want)
		}
	}
}

// A missing auth is refused, never defaulted; an unknown one and a stray header/scheme under
// a mode that does not read them are refused too — each as an auth refusal.
func TestWebhookAuthRefusals(t *testing.T) {
	for config, want := range map[string]error{
		`{"url":"https://x/y"}`:                                                    httpsink.ErrAuthModeUnstated,
		`{"url":"https://x/y","auth":"basic"}`:                                     httpsink.ErrAuthRefused,
		`{"url":"https://x/y","auth":"bearer","authHeader":"X-API-Key"}`:           httpsink.ErrAuthRefused,
		`{"url":"https://x/y","auth":"bearer","authScheme":"Bearer"}`:              httpsink.ErrAuthRefused,
		`{"url":"https://x/y","auth":"none","authHeader":"X-API-Key"}`:             httpsink.ErrAuthRefused,
		`{"url":"https://x/y","auth":"header"}`:                                    httpsink.ErrAuthRefused,
		`{"url":"https://x/y","auth":"header","authHeader":"X-DC-Service-Secret"}`: httpsink.ErrAuthRefused,
	} {
		_, err := parseHook(config)
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", config, err, want)
		}
	}
}
