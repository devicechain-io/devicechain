// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package httpsink is the shared, hardened outbound HTTP-delivery primitive for
// webhook-style sinks. It concentrates the SSRF/credential hardening that any
// tenant-configured outbound call must apply — so notification-management's webhook
// channel (ADR-017) and the outbound-connectors httpCall action (ADR-060) enforce
// exactly the same rules from one place, and the rules cannot drift apart:
//
//   - a no-redirect policy, forced onto whatever client Send uses (even a caller-
//     supplied one), so an external endpoint cannot 3xx the request onto an internal
//     target;
//   - reserved-header dropping, so tenant-supplied headers cannot forge the auth
//     header or the internal X-DC-* service identity;
//   - http/https-only targets, with URL-embedded credentials redacted from errors;
//   - response-body suppression when a credential is presented via Request.Secret,
//     so a hostile endpoint cannot reflect the Authorization header back into our
//     logs. (Present credentials through Secret/Auth — NOT by stuffing a raw token
//     into Headers, which is not covered by suppression.)
//
// It is NOT a complete SSRF firewall: it does not resolve or filter destination IPs
// (private-range / link-local / DNS-rebinding defense is the caller's job), and it
// does not restrict the HTTP method (method policy is caller-owned). It is
// deliberately transport-agnostic: the caller owns its own config shape, payload
// construction, and error-context wrapping; this package owns the mechanics that must
// be identical everywhere.
package httpsink

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// noRedirect is the redirect policy every httpsink request runs under: a 3xx is
// returned as-is (and treated as a non-2xx failure) rather than followed, so an
// external endpoint cannot 302 the request onto an internal target (an SSRF bypass of
// the configured endpoint). Send forces this onto whatever client it uses.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// DefaultClient is the shared production HTTP client for outbound delivery, used when
// Send is called with a nil client. Callers bound each request with a context
// deadline rather than a client Timeout, so none is set.
var DefaultClient = &http.Client{CheckRedirect: noRedirect}

// IsReservedHeader reports whether a caller/tenant-supplied header name is one the
// sink controls or that carries internal service identity, and so must never be
// settable from config: Authorization (set from the secret) and any X-DC-* internal
// header (the service-to-service tenant/service headers). Dropping these stops a
// configured outbound call from being pointed at an internal service with a spoofed
// identity.
func IsReservedHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	return canonical == "Authorization" || strings.HasPrefix(canonical, "X-Dc-")
}

// idempotencyHeader carries a caller-supplied idempotency key so an endpoint that
// honors it can dedup a redelivered send. It is an X-DC-* header we set AFTER
// reserved-header dropping (a caller-supplied one would have been dropped), so it
// always reflects the sink's own key, never a forged one.
const idempotencyHeader = "X-DC-Idempotency-Key"

// Auth describes how a secret is presented as a request header. The zero value
// (Header and Scheme both empty) means "Authorization: Bearer <secret>". Set Header
// to a custom header (e.g. "X-API-Key") and Scheme to "" to send the raw token.
type Auth struct {
	Header string
	Scheme string
}

// HeaderValue resolves the header name and value that carry secret. With the zero
// Auth it is ("Authorization", "Bearer <secret>"); a custom Header with an empty
// Scheme yields (Header, secret) — the raw token.
func (a Auth) HeaderValue(secret string) (name, value string) {
	name = a.Header
	if name == "" {
		name = "Authorization"
	}
	scheme := a.Scheme
	if a.Header == "" && a.Scheme == "" {
		// A defaulted Authorization header defaults to a Bearer scheme.
		scheme = "Bearer"
	}
	if scheme != "" {
		return name, scheme + " " + secret
	}
	return name, secret
}

// ValidateURL parses raw and requires an absolute http/https URL with a host and NO embedded
// userinfo. A missing scheme, ftp://, file://, or an unparseable URL is rejected — a non-http(s)
// target widens the SSRF surface for no delivery benefit. A host-less URL (https://) can never
// dispatch, so it is rejected early rather than at send time. Userinfo (https://user:pass@host)
// is rejected because it embeds a cleartext credential in the caller's stored config — exactly
// what an ADR-059 secret handle exists to avoid; credentials belong in Secret/Auth, never the URL.
func ValidateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		// Do not echo the raw string — an unparseable URL may still contain a credential.
		return nil, fmt.Errorf("invalid url (unparseable)")
	}
	// From here the URL parsed, so Redacted() masks any userinfo password in every error text.
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid url %q (want http/https)", parsed.Redacted())
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid url %q (missing host)", parsed.Redacted())
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("invalid url %q (must not embed userinfo credentials; use a secret handle)", parsed.Redacted())
	}
	return parsed, nil
}

// ValidateHeader reports whether a header name/value is well-formed for the wire: a non-empty
// RFC 7230 token name and a value free of control characters (CR/LF/NUL/other C0, DEL). net/http
// rejects a malformed header at send time, so validating it at authoring/config time turns a
// dispatch-time failure into an early, actionable rejection — and forbidding CR/LF closes header
// injection at the gate rather than relying on the transport.
func ValidateHeader(name, value string) error {
	if name == "" {
		return fmt.Errorf("header name must not be empty")
	}
	for i := 0; i < len(name); i++ {
		if !validHeaderNameByte(name[i]) {
			return fmt.Errorf("header name %q contains an invalid character", name)
		}
	}
	for i := 0; i < len(value); i++ {
		if b := value[i]; (b < 0x20 && b != '\t') || b == 0x7f {
			return fmt.Errorf("header %q value contains a control character", name)
		}
	}
	return nil
}

// validHeaderNameByte reports whether c is an RFC 7230 token character (the header-name grammar).
func validHeaderNameByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

// Request is one outbound delivery. Body is sent as-is with ContentType (defaulting
// to application/json). Method defaults to POST. Caller-supplied Headers have
// reserved names dropped. When Secret is non-empty it is presented per Auth, and the
// response body is suppressed from any error (see Send).
type Request struct {
	URL            string
	Method         string
	Headers        map[string]string
	Body           []byte
	ContentType    string
	Secret         string
	Auth           Auth
	IdempotencyKey string
}

// Send delivers req using client (nil ⇒ DefaultClient), bounded by ctx. It validates
// the target is http/https, drops reserved headers, sets the content type, applies
// the secret auth header, and stamps the idempotency key. It returns nil on a 2xx.
//
// On a non-2xx or a transport error it returns an error — whose text NEVER includes
// the response body when req.Secret is set, because a hostile endpoint could reflect
// the Authorization header in its body and leak the write-only secret into logs. Any
// URL-embedded credential is redacted from the transport-error text. The error carries
// no tenant/resource context; the caller wraps it with its own (e.g. the channel or
// connector token).
func Send(ctx context.Context, client *http.Client, req Request) error {
	parsed, err := ValidateURL(req.URL)
	if err != nil {
		return err
	}
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	contentType := req.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	httpReq.Header.Set("Content-Type", contentType)
	for k, v := range req.Headers {
		if IsReservedHeader(k) {
			// Silently drop here — the caller decides whether to warn; the security
			// invariant (a reserved header from config never reaches the wire) holds
			// regardless.
			continue
		}
		httpReq.Header.Set(k, v)
	}
	if req.Secret != "" {
		name, value := req.Auth.HeaderValue(req.Secret)
		httpReq.Header.Set(name, value)
	}
	if req.IdempotencyKey != "" {
		httpReq.Header.Set(idempotencyHeader, req.IdempotencyKey)
	}

	c := DefaultClient
	if client != nil {
		// Force the no-redirect SSRF policy onto a caller-supplied client too: the
		// package's guarantee must not depend on the caller remembering to set
		// CheckRedirect. Clone (a shallow copy) so we override only the redirect policy;
		// Transport/Jar are shared, which is safe.
		clone := *client
		clone.CheckRedirect = noRedirect
		c = &clone
	}
	resp, err := c.Do(httpReq)
	if err != nil {
		// Redact any URL-embedded credential (https://user:pass@host) so a
		// transport-error log can never leak it. The wrapped *url.Error is already
		// password-stripped by net/http, but the explicit URL here would not be.
		return fmt.Errorf("%s %s: %w", method, parsed.Redacted(), err)
	}
	defer resp.Body.Close()
	// Read a bounded snippet for diagnostics (used only on a non-2xx, non-secret path).
	// This is not a full drain, so it does not by itself enable keep-alive reuse.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if req.Secret != "" {
			// Never surface the body of a secret-bearing call (it could reflect the
			// Authorization header).
			return fmt.Errorf("returned %d", resp.StatusCode)
		}
		return fmt.Errorf("returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
