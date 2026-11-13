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

func TestWebhookLinkBaseURL(t *testing.T) {
	parse := func(extra string) (*WebhookConfig, error) {
		c := `{"url":"https://hooks.example/x","auth":"none"` + extra + `}`
		return ParseWebhookConfig("hook", &c)
	}
	cfg, err := parse(`,"linkBaseUrl":"https://console.example/"`)
	if err != nil || cfg.LinkBaseURL != "https://console.example" {
		t.Fatalf("got %+v, %v; want the trailing slash trimmed", cfg, err)
	}
	if cfg, err = parse(``); err != nil || cfg.LinkBaseURL != "" {
		t.Fatalf("absent: %+v, %v", cfg, err)
	}
	if cfg, err = parse(`,"linkBaseUrl":"http://console.example"`); err != nil || cfg.LinkBaseURL != "http://console.example" {
		t.Fatalf("http base: %+v, %v", cfg, err)
	}
	for _, bad := range []string{`"ftp://x"`, `"not a url"`, `"javascript:alert(1)"`,
		`"https://console.example?x=1"`, `"https://console.example#alarm"`,
		`"https://console.example?"`, `"https://console.example#"`} {
		if _, err := parse(`,"linkBaseUrl":` + bad); !errors.Is(err, ErrInvalidLinkBase) {
			t.Errorf("linkBaseUrl %s: got %v, want ErrInvalidLinkBase", bad, err)
		}
	}
}

func TestWebhookConfigForDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, config         string
		wantLinkErr, wantErr bool
	}{
		{"valid", `{"url":"http://hooks.example/x","auth":"none","linkBaseUrl":"http://console.example/"}`, false, false},
		{"invalid link", `{"url":"https://hooks.example/x","auth":"none","linkBaseUrl":"ftp://x"}`, true, false},
		{"query", `{"url":"https://hooks.example/x","auth":"none","linkBaseUrl":"https://console.example?x=1"}`, true, false},
		{"case insensitive key", `{"url":"https://hooks.example/x","auth":"none","LINKBASEURL":"ftp://x"}`, true, false},
		{"bad destination", `{"url":"ftp://hooks.example/x","auth":"none","linkBaseUrl":"ftp://x"}`, false, true},
		{"bad method after invalid link", `{"url":"https://hooks.example/x","auth":"none","method":"DELETE","linkBaseUrl":"ftp://x"}`, true, true},
		{"missing auth after invalid link", `{"url":"https://hooks.example/x","linkBaseUrl":"ftp://x"}`, true, true},
		{"malformed JSON", `{nope`, false, true},
		{"wrong link type", `{"url":"https://hooks.example/x","auth":"none","linkBaseUrl":42}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.config
			cfg, linkErr, err := ParseWebhookConfigForDelivery("hook", &tc.config)
			if errors.Is(linkErr, ErrInvalidLinkBase) != tc.wantLinkErr || (err != nil) != tc.wantErr {
				t.Fatalf("cfg=%+v linkErr=%v err=%v", cfg, linkErr, err)
			}
			if !tc.wantErr {
				wantBase := "http://console.example"
				if tc.wantLinkErr {
					wantBase = ""
				}
				if cfg.LinkBaseURL != wantBase || cfg.Method != http.MethodPost {
					t.Fatalf("cfg=%+v", cfg)
				}
			}
			if tc.config != original {
				t.Fatal("stored config was mutated")
			}
		})
	}
	if _, linkErr, err := ParseWebhookConfigForDelivery("hook", nil); linkErr != nil || err == nil {
		t.Fatalf("nil config: linkErr=%v err=%v", linkErr, err)
	}
}

func TestWebhookDeliveryPreservesCaseVariantPrecedence(t *testing.T) {
	withoutLink := `{"signing":false,"SIGNING":true,"url":"https://hooks.example/x","auth":"none"}`
	withLink := `{"signing":false,"SIGNING":true,"url":"https://hooks.example/x","auth":"none","linkBaseUrl":"ftp://x"}`
	strict, err := ParseWebhookConfig("hook", &withoutLink)
	if err != nil {
		t.Fatal(err)
	}
	cfg, linkErr, err := ParseWebhookConfigForDelivery("hook", &withLink)
	if err != nil || !errors.Is(linkErr, ErrInvalidLinkBase) {
		t.Fatalf("cfg=%+v linkErr=%v err=%v", cfg, linkErr, err)
	}
	if cfg.Signing != strict.Signing {
		t.Fatalf("Signing=%v, want strict parser's %v", cfg.Signing, strict.Signing)
	}
}

func TestConfigForDeliveryOmitsInvalidLinks(t *testing.T) {
	for _, base := range []string{"ftp://x", "https://c.example/?x=1", "https://c.example/#alarm"} {
		t.Run(base, func(t *testing.T) {
			config := `{"linkBaseUrl":"` + base + `"}`
			for _, kind := range []string{ChannelTypeSlack, ChannelTypeTeams} {
				cfg, linkErr, err := ParseLinkConfigForDelivery("c", kind, &config)
				if err != nil || !errors.Is(linkErr, ErrInvalidLinkBase) || cfg == nil || cfg.LinkBaseURL != "" {
					t.Fatalf("%s: cfg=%+v linkErr=%v err=%v", kind, cfg, linkErr, err)
				}
				if _, err := ParseLinkConfig("c", kind, &config); !errors.Is(err, ErrInvalidLinkBase) {
					t.Fatalf("strict %s: %v", kind, err)
				}
			}
			pd, linkErr, err := ParsePagerDutyConfigForDelivery("c", &config)
			if err != nil || !errors.Is(linkErr, ErrInvalidLinkBase) || pd == nil || pd.LinkBaseURL != "" || pd.Region != PagerDutyRegionUS || pd.Source != "devicechain" {
				t.Fatalf("pagerduty: cfg=%+v linkErr=%v err=%v", pd, linkErr, err)
			}
			if _, err := ParsePagerDutyConfig("c", &config); !errors.Is(err, ErrInvalidLinkBase) {
				t.Fatalf("strict pagerduty: %v", err)
			}
			hookConfig := `{"url":"https://hooks.example/x","auth":"none","linkBaseUrl":"` + base + `"}`
			hook, linkErr, err := ParseWebhookConfigForDelivery("c", &hookConfig)
			if err != nil || !errors.Is(linkErr, ErrInvalidLinkBase) || hook == nil || hook.LinkBaseURL != "" {
				t.Fatalf("webhook: cfg=%+v linkErr=%v err=%v", hook, linkErr, err)
			}
			if _, err := ParseWebhookConfig("c", &hookConfig); !errors.Is(err, ErrInvalidLinkBase) {
				t.Fatalf("strict webhook: %v", err)
			}
		})
	}
}
