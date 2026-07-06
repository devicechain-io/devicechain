// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-notification-management/model"
)

func TestParseSMTPConfigDefaults(t *testing.T) {
	// STARTTLS default + default submission port.
	cfg, err := parseSMTPConfig(channelWith("s", model.ChannelTypeSMTP,
		`{"host":"smtp.example.com","from":"alarms@example.com"}`, ""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Security != smtpSecurityStartTLS || cfg.Port != 587 {
		t.Fatalf("defaults wrong: %+v", cfg)
	}

	// Implicit TLS defaults to 465.
	cfg, _ = parseSMTPConfig(channelWith("s", model.ChannelTypeSMTP,
		`{"host":"h","from":"f","security":"tls"}`, ""))
	if cfg.Port != 465 {
		t.Fatalf("tls port = %d, want 465", cfg.Port)
	}
}

func TestParseSMTPConfigValidation(t *testing.T) {
	cases := []string{
		`{"from":"f"}`,                           // missing host
		`{"host":"h"}`,                           // missing from
		`{"host":"h","from":"f","security":"x"}`, // unknown security
	}
	for _, c := range cases {
		if _, err := parseSMTPConfig(channelWith("s", model.ChannelTypeSMTP, c, "")); err == nil {
			t.Fatalf("expected error for %s", c)
		}
	}
}

func TestEnsureNoCRLF(t *testing.T) {
	if err := ensureNoCRLF("from", "alarms@example.com"); err != nil {
		t.Fatalf("clean value rejected: %v", err)
	}
	for _, bad := range []string{"a@x.com\r\nBcc: evil@x.com", "a@x.com\nX: y"} {
		if err := ensureNoCRLF("from", bad); err == nil {
			t.Fatalf("expected rejection of %q", bad)
		}
	}
}

func TestBuildEmailHeadersAndCRLF(t *testing.T) {
	msg := &RenderedNotification{
		Subject:  "[CRITICAL] Alarm raised",
		TextBody: "line one\nline two",
	}
	out := string(buildEmail("alarms@example.com", []string{"a@x.com", "b@x.com"}, msg))
	for _, want := range []string{
		"From: alarms@example.com\r\n",
		"To: a@x.com, b@x.com\r\n",
		"Subject: [CRITICAL] Alarm raised\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"line one\r\nline two",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("email missing %q:\n%s", want, out)
		}
	}
	// Headers and body separated by a blank CRLF line.
	if !strings.Contains(out, "\r\n\r\n") {
		t.Fatalf("missing header/body separator")
	}
}
