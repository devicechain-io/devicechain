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
//
//   - reserved-header dropping, so tenant-supplied headers cannot forge the auth
//     header or the internal X-DC-* service identity;
//
//   - http/https-only targets, with URL-embedded credentials redacted from errors;
//
//   - response-body suppression when a credential is presented via Request.Secret,
//     so a hostile endpoint cannot reflect the Authorization header back into our
//     logs. (Present credentials through Secret/Auth — NOT by stuffing a raw token
//     into Headers, which is not covered by suppression.)
//
//   - a stated auth mode: the caller says none, bearer or header, and there is no
//     default. A declared credential that is missing, or a credential with mode none,
//     is refused before a request is built (ErrAuthRefused);
//
//   - a destination-address boundary: DefaultClient dials through egress.Guard, which
//     refuses a private, loopback, link-local or cloud-metadata address at the moment
//     the kernel is about to connect to it. That placement is the point — checking the
//     hostname and then dialing it leaves a window in which DNS can answer differently,
//     and the window cannot be closed by checking harder.
//
// It still does not restrict the HTTP method (method policy is caller-owned). It is
// deliberately transport-agnostic: the caller owns its own config shape, payload
// construction, and error-context wrapping; this package owns the mechanics that must
// be identical everywhere.
//
// 🔴 A caller that supplies its OWN client supplies its own transport, and therefore
// its own dialer. Send forces the redirect policy onto such a client but CANNOT force
// the transport — replacing it would silently discard the caller's timeouts and
// connection pooling, and there is no way to tell a test's httptest client from a
// production one that has quietly lost its guard. So: pass nil in production, and treat
// a non-nil client as an explicit statement that the caller has taken responsibility
// for where the connection goes.
package httpsink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/devicechain-io/dc-microservice/egress"
)

// noRedirect is the redirect policy every httpsink request runs under: a 3xx is
// returned as-is (and treated as a non-2xx failure) rather than followed, so an
// external endpoint cannot 302 the request onto an internal target (an SSRF bypass of
// the configured endpoint). Send forces this onto whatever client it uses.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// DefaultClient is the shared production HTTP client for outbound delivery, used when
// Send is called with a nil client. Callers bound each request with a context deadline
// rather than a client Timeout, so none is set.
//
// It dials through an egress guard with NO allowances, which is the fail-closed default
// on purpose: every future caller that passes nil inherits the boundary without having
// to know it exists. The alternative — leaving DefaultClient unguarded and installing
// the guard at the two call sites that exist today — puts the security property in the
// place a third call site will forget to look.
//
// 🔴 The guard is NOT installed on http.DefaultTransport, and must never be. Service-to-
// service calls, the JWKS fetch and the governance resolvers all dial private ClusterIPs
// legitimately, and some of them run in the same process as tenant egress. A global
// install would break the platform rather than secure it.
var DefaultClient = &http.Client{
	CheckRedirect: noRedirect,
	Transport:     egress.NewGuard(nil).Transport(),
}

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

// AuthMode says whether, and how, a request presents a credential. There is NO default:
// the zero value is AuthUnstated, and Send refuses it. That is the point of the type. The
// old zero Auth meant "Authorization: Bearer <secret>", so an empty secret silently
// produced an unauthenticated request, and no caller could say "this endpoint is meant to
// be anonymous" as distinct from "this endpoint's credential is missing".
type AuthMode int

const (
	// AuthUnstated is the zero value. Send refuses it (ErrAuthModeUnstated).
	AuthUnstated AuthMode = iota
	// AuthNone presents no credential. The endpoint is anonymous, or its URL carries the
	// credential (a Slack incoming webhook). A non-empty Secret is refused.
	AuthNone
	// AuthBearer presents the secret as "Authorization: Bearer <secret>". Header and Scheme
	// must be empty.
	AuthBearer
	// AuthHeader presents the secret in Header, prefixed by Scheme and a space when Scheme
	// is non-empty, raw otherwise. Header is required and may be "Authorization".
	AuthHeader
)

// Auth is how a request presents its credential. Mode is required; Header and Scheme are
// read only under AuthHeader, and setting them under any other mode is refused rather than
// ignored.
type Auth struct {
	Mode   AuthMode
	Header string
	Scheme string
}

// ErrAuthRefused is the parent of every refusal about auth configuration or credential
// presence. Callers classify on it: such a refusal is TERMINAL, because a redelivery sends
// the same configuration and gets the same answer.
var ErrAuthRefused = errors.New("auth refused")

var (
	// ErrAuthModeUnstated: the caller did not say how (or whether) to authenticate.
	ErrAuthModeUnstated = fmt.Errorf("%w: no auth mode was stated", ErrAuthRefused)
	// ErrMissingCredential: a credential is required and there is none. Worded without
	// reference to HTTP on purpose — the SMTP adapter wraps it too, so that one terminal
	// classification covers every credential refusal a notification can hit.
	ErrMissingCredential = fmt.Errorf("%w: a credential is required but none was supplied", ErrAuthRefused)
	// ErrUnexpectedCredential: a credential with mode none, which would never be presented.
	ErrUnexpectedCredential = fmt.Errorf("%w: a credential was supplied but the auth mode is none", ErrAuthRefused)
)

// Validate reports whether the auth configuration is one this sink will write. It does not
// look at the credential (CheckCredential does), so it can run at save time on config alone.
//
// 🔴 The header check is NOT IsReservedHeader, and the difference is the whole reason it
// exists. IsReservedHeader rejects Authorization — which is a legitimate header for
// AuthHeader (a "Token" scheme, say), and the one AuthBearer writes. Reusing it would refuse
// the common case. What must be refused is the internal service identity: an X-DC-* header
// carrying a tenant-supplied secret value.
//
// The hole this closes: Send writes the auth header AFTER the reserved-header drop loop,
// so it never passed through that filter. A notification channel configured with
// authHeader "X-DC-Service-Secret" and a secret holding the real service secret would send
// it as the service-identity header — which, combined with a URL aimed at
// user-management's mint endpoint, is a path to a service token. The address half of that
// is refused by the egress guard; this is the other half, and it should not depend on the
// first one holding.
//
// It ERRORS rather than dropping. Dropping would ship the payload to a tenant's endpoint
// with no credential at all — a different defect, and a silent one: the endpoint would
// reject it, the operator would see delivery failures, and nothing would say why.
func (a Auth) Validate() error {
	switch a.Mode {
	case AuthNone, AuthBearer:
		if a.Header != "" || a.Scheme != "" {
			return fmt.Errorf("%w: a header or scheme is set, but only auth mode header uses them", ErrAuthRefused)
		}
		return nil
	case AuthHeader:
		if a.Header == "" {
			return fmt.Errorf("%w: auth mode header needs a header name", ErrAuthRefused)
		}
		if err := ValidateHeader(a.Header, a.Scheme); err != nil {
			return fmt.Errorf("%w: %w", ErrAuthRefused, err)
		}
		if strings.HasPrefix(http.CanonicalHeaderKey(a.Header), "X-Dc-") {
			return fmt.Errorf("%w: auth header %q is reserved: X-DC-* headers carry internal service identity "+
				"and must not be settable from configuration", ErrAuthRefused, a.Header)
		}
		return nil
	default:
		return ErrAuthModeUnstated
	}
}

// CheckCredential is THE rule relating a mode to whether a credential is present. Send calls
// it with (Secret != ""); notification-management calls it when a channel is saved, with
// "will a secret be stored". One definition, so that the two cannot disagree about the rule.
// (They can still disagree about the FACT — a secret write that fails after its row is saved
// — which is why Send, the point of use, is the authority.)
func (a Auth) CheckCredential(present bool) error {
	switch a.Mode {
	case AuthNone:
		if present {
			return ErrUnexpectedCredential
		}
		return nil
	case AuthBearer, AuthHeader:
		if !present {
			return ErrMissingCredential
		}
		return nil
	default:
		return ErrAuthModeUnstated
	}
}

// headerValue resolves the header that carries secret. Only meaningful for AuthBearer and
// AuthHeader, which is all Send calls it with.
func (a Auth) headerValue(secret string) (name, value string) {
	if a.Mode == AuthBearer {
		return "Authorization", "Bearer " + secret
	}
	if a.Scheme != "" {
		return a.Header, a.Scheme + " " + secret
	}
	return a.Header, secret
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
// reserved names dropped. Auth is REQUIRED: Send refuses the zero value, a mode that
// presents a credential with an empty Secret, and a Secret with AuthNone. The response
// body is suppressed from any error whenever Secret is set (see Send).
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
// the target is http/https and the auth mode against the credential, drops reserved
// headers, sets the content type, applies the auth header, and stamps the idempotency
// key. It returns nil on a 2xx. An auth refusal wraps ErrAuthRefused and is returned
// before any request is built, so nothing reaches the wire.
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
	// Checked BEFORE a request exists. This is the point of use and the only place that sees
	// every caller: a stored config written before any save-time check existed, or a caller
	// that forgot one, reaches the wire through here and nowhere else.
	if err := req.Auth.Validate(); err != nil {
		return err
	}
	if err := req.Auth.CheckCredential(req.Secret != ""); err != nil {
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
	if req.Auth.Mode != AuthNone {
		// CheckCredential above guarantees a non-empty Secret here.
		name, value := req.Auth.headerValue(req.Secret)
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
