// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-notification-management/model"
	"gorm.io/datatypes"
)

// channelWith builds an in-memory channel with the given type/config/secret.
func channelWith(token, ctype, config, secret string) *model.NotificationChannel {
	c := &model.NotificationChannel{ChannelType: ctype}
	c.Token = token
	if config != "" {
		j := datatypes.JSON([]byte(config))
		c.Config = &j
	}
	if secret != "" {
		c.Secret = sql.NullString{String: secret, Valid: true}
	}
	return c
}

func TestWebhookDeliverPostsPayloadWithAuth(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	adapter := &webhookAdapter{client: srv.Client()}
	channel := channelWith("hook-1", model.ChannelTypeWebhook, `{"url":"`+srv.URL+`"}`, "s3cr3t")
	msg := &RenderedNotification{
		Subject: "[CRITICAL] Alarm raised",
		Payload: map[string]any{"text": "[CRITICAL] Alarm raised", "severity": "CRITICAL"},
	}

	if err := adapter.Deliver(context.Background(), channel, []string{"ops@example.com"}, msg); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content-type = %q", gotContentType)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Fatalf("auth = %q, want Bearer s3cr3t", gotAuth)
	}
	if gotBody["text"] != "[CRITICAL] Alarm raised" || gotBody["severity"] != "CRITICAL" {
		t.Fatalf("body = %+v", gotBody)
	}
	if rcpts, ok := gotBody["recipients"].([]any); !ok || len(rcpts) != 1 || rcpts[0] != "ops@example.com" {
		t.Fatalf("recipients not threaded: %+v", gotBody["recipients"])
	}
}

// A custom auth header with an empty scheme sends the raw secret.
func TestWebhookCustomAuthHeader(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	adapter := &webhookAdapter{client: srv.Client()}
	channel := channelWith("hook-2", model.ChannelTypeWebhook,
		`{"url":"`+srv.URL+`","authHeader":"X-API-Key","authScheme":""}`, "rawtoken")
	if err := adapter.Deliver(context.Background(), channel, nil,
		&RenderedNotification{Payload: map[string]any{"text": "hi"}}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if gotKey != "rawtoken" {
		t.Fatalf("X-API-Key = %q, want rawtoken", gotKey)
	}
}

// A non-2xx response is a delivery error (the dispatcher will retry it).
func TestWebhookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	adapter := &webhookAdapter{client: srv.Client()}
	channel := channelWith("hook-3", model.ChannelTypeWebhook, `{"url":"`+srv.URL+`"}`, "")
	err := adapter.Deliver(context.Background(), channel, nil, &RenderedNotification{Payload: map[string]any{}})
	if err == nil {
		t.Fatalf("expected error on 500")
	}
}

func TestWebhookConfigValidation(t *testing.T) {
	if _, err := parseWebhookConfig(channelWith("h", model.ChannelTypeWebhook, `{}`, "")); err == nil {
		t.Fatalf("expected missing-url error")
	}
	if _, err := parseWebhookConfig(channelWith("h", model.ChannelTypeWebhook, `{"url":"ftp://x"}`, "")); err == nil {
		t.Fatalf("expected invalid-scheme error")
	}
	if _, err := parseWebhookConfig(channelWith("h", model.ChannelTypeWebhook, `{"url":"https://x/y","method":"DELETE"}`, "")); err == nil {
		t.Fatalf("expected POST-only rejection")
	}
	cfg, err := parseWebhookConfig(channelWith("h", model.ChannelTypeWebhook, `{"url":"https://x/y"}`, ""))
	if err != nil || cfg.Method != http.MethodPost {
		t.Fatalf("default method: cfg=%+v err=%v", cfg, err)
	}
}

// Reserved headers from tenant config (Authorization, X-DC-*) are dropped; the
// channel secret still populates Authorization.
func TestWebhookDropsReservedHeaders(t *testing.T) {
	var gotAuth, gotTenant, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-DC-Tenant")
		gotCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	adapter := &webhookAdapter{client: srv.Client()}
	channel := channelWith("hook", model.ChannelTypeWebhook,
		`{"url":"`+srv.URL+`","headers":{"Authorization":"Bearer forged","X-DC-Tenant":"victim","X-Custom":"ok"}}`, "realsecret")
	if err := adapter.Deliver(context.Background(), channel, nil, &RenderedNotification{Payload: map[string]any{}}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if gotAuth != "Bearer realsecret" {
		t.Fatalf("Authorization = %q, want the channel secret (config override dropped)", gotAuth)
	}
	if gotTenant != "" {
		t.Fatalf("X-DC-Tenant should be dropped, got %q", gotTenant)
	}
	if gotCustom != "ok" {
		t.Fatalf("X-Custom should pass through, got %q", gotCustom)
	}
}

// The production client does not follow redirects (SSRF hardening).
func TestDefaultWebhookClientBlocksRedirects(t *testing.T) {
	if defaultWebhookClient.CheckRedirect == nil {
		t.Fatal("expected a CheckRedirect that blocks redirects")
	}
	if err := defaultWebhookClient.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatalf("CheckRedirect = %v, want ErrUseLastResponse", err)
	}
}
