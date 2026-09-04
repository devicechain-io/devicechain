// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/smtp"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// smtpAdapter delivers a notification as a plaintext email over SMTP (ADR-017). The
// channel's Config carries the non-secret connection settings; the channel's Secret
// is the SMTP password (used only when a username is configured). It speaks SMTP
// directly (net/smtp) rather than through smtp.SendMail so it can honor the context
// deadline on every step and support implicit TLS as well as STARTTLS.
type smtpAdapter struct {
	// guard is the tenant-egress boundary this adapter dials through, carrying the
	// operator's allowed destinations. nil means the fail-closed default with no
	// allowances — a wiring mistake narrows the boundary, never removes it.
	guard *egress.Guard
}

// egressGuard returns the configured guard, or a no-allowance one.
//
// 🔴 Judge this on where SMTP CREDENTIALS go, not only on what SSRF reaches. The default
// security mode is STARTTLS, negotiated after the greeting, and "none" is cleartext — so
// a channel pointed at an attacker-chosen host hands over the configured username and
// password. The failure here is credential disclosure, not request forgery, which is why
// the fallback is a real guard rather than a bare dialer.
func (a *smtpAdapter) egressGuard() *egress.Guard {
	if a.guard == nil {
		return egress.NewGuard(nil)
	}
	return a.guard
}

// smtpConfig is the SMTP channel's connection settings (the channel's non-secret
// Config JSON). Security selects the transport: "starttls" (default) upgrades a
// plaintext connection, "tls" dials an implicitly-encrypted port (465), "none" sends
// in cleartext (test/loopback only).
type smtpConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	From     string `json:"from"`
	Username string `json:"username"`
	Security string `json:"security"`
}

const (
	smtpSecurityStartTLS = "starttls"
	smtpSecurityTLS      = "tls"
	smtpSecurityNone     = "none"
)

// Deliver sends the rendered notification to each recipient as one email. secret is
// the SMTP password (empty when unconfigured); it is required only when the channel
// config names a username.
func (a *smtpAdapter) Deliver(ctx context.Context, channel *model.NotificationChannel,
	secret string, recipients []string, msg *RenderedNotification) error {
	cfg, err := parseSMTPConfig(channel)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return fmt.Errorf("smtp channel %q has no recipients", channel.Token)
	}
	if err := ensureHeaderSafe(cfg.From, recipients, msg); err != nil {
		return err
	}

	client, err := a.dial(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if cfg.Security == smtpSecurityStartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	// Authenticate only when a username is configured; an open relay / loopback test
	// server takes no auth.
	if cfg.Username != "" {
		if secret == "" {
			return fmt.Errorf("smtp channel %q has a username but no secret configured", channel.Token)
		}
		auth := smtp.PlainAuth("", cfg.Username, secret, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp RCPT TO %q: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(buildEmail(cfg.From, recipients, msg)); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close body: %w", err)
	}
	// The server accepted the message once Close (the final ".") returned nil, so the
	// email is delivered. A QUIT error after that must NOT be surfaced: it would make
	// the dispatcher retry and send a duplicate. Log-and-swallow instead.
	if err := client.Quit(); err != nil {
		log.Warn().Err(err).Str("channel", channel.Token).Msg("SMTP QUIT failed after message was accepted")
	}
	return nil
}

// dial opens the SMTP client, applying implicit TLS when configured. It sets the
// connection deadline from the context so every step of the SMTP conversation —
// including the greeting read inside NewClient, which nothing else bounds — cannot
// hang a dispatch worker (and thus shutdown) against a black-hole endpoint.
func (a *smtpAdapter) dial(ctx context.Context, cfg *smtpConfig) (*smtp.Client, error) {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	// The host comes from the channel's Config JSON, so it is tenant-supplied and until
	// now was dialed with no address check of any kind — an SMTP channel pointed at
	// 169.254.169.254 was a connection to the instance metadata service. The guard runs
	// inside the dialer's Control hook, on the address the kernel is about to connect to,
	// which is the only placement a DNS answer cannot get between.
	conn, err := a.egressGuard().Dialer().DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if cfg.Security == smtpSecurityTLS {
		tconn := tls.Client(conn, &tls.Config{ServerName: cfg.Host})
		if err := tconn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("smtp tls handshake: %w", err)
		}
		conn = tconn
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("smtp client: %w", err)
	}
	return client, nil
}

// ensureHeaderSafe rejects a CR/LF in ANY value this adapter interpolates into the
// message's headers — the envelope from, every recipient, and the subject — because a
// line break in any of them opens the same hole: the remainder of the value is parsed as
// a new header, so a hidden Bcc rides out with the alarm.
//
// 🔴 THE SUBJECT IS IN THIS LIST BECAUSE IT IS THE ONE THAT WAS MISSING, and it was
// missing for the most dangerous reason: it looked safe from somewhere else. The subject
// is rendered from the alarm's key and severity, and an alarm key is grammar-checked in
// event-processing, two services away, at the moment a rule renders it. That check is
// real and it is why no crafted subject can reach here today — but it is enforced in a
// different binary by a different team's code, and nothing in THIS service would notice
// if it were relaxed, moved, or routed around by a future producer of alarm events. The
// local check costs a string scan on a path that already dials a network socket; the
// distant invariant it duplicates costs nothing to lose silently.
func ensureHeaderSafe(from string, recipients []string, msg *RenderedNotification) error {
	if err := ensureNoCRLF("from", from); err != nil {
		return err
	}
	for _, rcpt := range recipients {
		if err := ensureNoCRLF("recipient", rcpt); err != nil {
			return err
		}
	}
	return ensureNoCRLF("subject", msg.Subject)
}

// ensureNoCRLF rejects a value containing a carriage return or line feed, which in an
// SMTP header would let the remainder of the value be parsed as an extra header.
func ensureNoCRLF(field, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("smtp %s %q contains a line break", field, value)
	}
	return nil
}

// parseSMTPConfig unmarshals and defaults/validates the channel's SMTP config.
func parseSMTPConfig(channel *model.NotificationChannel) (*smtpConfig, error) {
	cfg := &smtpConfig{}
	if channel.Config != nil {
		if err := json.Unmarshal([]byte(*channel.Config), cfg); err != nil {
			return nil, fmt.Errorf("smtp channel %q has invalid config: %w", channel.Token, err)
		}
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("smtp channel %q config is missing host", channel.Token)
	}
	if cfg.From == "" {
		return nil, fmt.Errorf("smtp channel %q config is missing from", channel.Token)
	}
	if cfg.Security == "" {
		cfg.Security = smtpSecurityStartTLS
	}
	switch cfg.Security {
	case smtpSecurityStartTLS, smtpSecurityTLS, smtpSecurityNone:
	default:
		return nil, fmt.Errorf("smtp channel %q has unknown security %q (want starttls|tls|none)", channel.Token, cfg.Security)
	}
	if cfg.Port == 0 {
		if cfg.Security == smtpSecurityTLS {
			cfg.Port = 465
		} else {
			cfg.Port = 587
		}
	}
	return cfg, nil
}

// buildEmail renders the RFC 5322 message: headers plus the plaintext body.
func buildEmail(from string, recipients []string, msg *RenderedNotification) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(recipients, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	// Normalize body line endings to CRLF for SMTP.
	b.WriteString(strings.ReplaceAll(msg.TextBody, "\n", "\r\n"))
	return []byte(b.String())
}
