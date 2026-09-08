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
		`{"host":"smtp.example.com","from":"alarms@example.com"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Security != smtpSecurityStartTLS || cfg.Port != 587 {
		t.Fatalf("defaults wrong: %+v", cfg)
	}

	// Implicit TLS defaults to 465.
	cfg, _ = parseSMTPConfig(channelWith("s", model.ChannelTypeSMTP,
		`{"host":"h","from":"f","security":"tls"}`))
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
		if _, err := parseSMTPConfig(channelWith("s", model.ChannelTypeSMTP, c)); err == nil {
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

// 🔴 THE SUBJECT IS A HEADER TOO, AND IT WAS THE ONE WITH NO LOCAL GUARD.
//
// buildEmail interpolates Subject straight into a "Subject: %s\r\n" line, so a CR/LF in it
// ends the header and starts another — a hidden Bcc riding out with every alarm. from and
// every recipient were guarded; the subject was not.
//
// 🔑 IT IS CURRENTLY UNREACHABLE, AND THAT IS WHY IT NEEDS THE CHECK. The subject is
// rendered from the alarm's severity and key, and an alarm key is grammar-checked in
// event-processing — a different service, a different binary — at the moment a rule renders
// it. So no crafted subject can arrive here today. Nothing in THIS service would notice if
// that grammar were relaxed, moved, or bypassed by a new producer of alarm events, and a
// guarantee whose only enforcement is two services away with nothing local to catch its
// removal is precisely the shape that keeps burning this codebase. The check costs a string
// scan on a path that is about to open a TCP connection.
func TestASubjectWithALineBreakIsRefused(t *testing.T) {
	clean := &RenderedNotification{Subject: "[CRITICAL] Alarm raised: over-temp on Device 7"}
	if err := ensureHeaderSafe("alarms@example.com", []string{"ops@x.com"}, clean); err != nil {
		t.Fatalf("a well-formed message was rejected: %v", err)
	}

	for _, bad := range []string{
		"[CRITICAL] alarm\r\nBcc: exfil@evil.example",
		"[CRITICAL] alarm\nX-Injected: yes",
	} {
		msg := &RenderedNotification{Subject: bad}
		err := ensureHeaderSafe("alarms@example.com", []string{"ops@x.com"}, msg)
		if err == nil {
			t.Fatalf("a subject containing a line break was accepted: %q", bad)
		}
		if !strings.Contains(err.Error(), "subject") {
			t.Fatalf("the refusal must name the offending field so an operator can find it, got %v", err)
		}
	}
}

// The envelope fields the guard already covered, kept as one enumeration so a future edit
// cannot quietly drop one of the three. All three reach the same header block.
func TestEveryInterpolatedHeaderFieldIsGuarded(t *testing.T) {
	const injected = "x\r\nBcc: exfil@evil.example"
	msg := &RenderedNotification{Subject: "ok"}

	if err := ensureHeaderSafe(injected, []string{"ops@x.com"}, msg); err == nil {
		t.Fatal("a from with a line break was accepted")
	}
	if err := ensureHeaderSafe("alarms@example.com", []string{"ops@x.com", injected}, msg); err == nil {
		t.Fatal("a recipient with a line break was accepted")
	}
	if err := ensureHeaderSafe("alarms@example.com", []string{"ops@x.com"},
		&RenderedNotification{Subject: injected}); err == nil {
		t.Fatal("a subject with a line break was accepted")
	}
}
