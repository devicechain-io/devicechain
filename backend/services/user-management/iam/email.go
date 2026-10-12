// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// There is ONE canonical spelling of an email address and ONE canonical spelling of a domain,
// both defined here, and every place that creates, looks up or compares an identity's email
// -- and every place that matches an email's domain against a verified-domain assertion --
// goes through them. Two spellings of one mailbox must never be two identities, and two
// spellings of one domain must never be two assertions:
//
//	bücher.example, BÜCHER.example, xn--bcher-kva.example and xn--bcher-kva.example.
//
// are the same domain. The local part is only trimmed and lower-cased (as it always has
// been); the domain is IDNA-mapped to lower-case ASCII (punycode) without a trailing dot.

var (
	// ErrEmailInvalid refuses an address that is not exactly one local part, one "@" and a
	// domain that survives IDNA. Callers fail closed on it.
	ErrEmailInvalid = errors.New("not a valid email address")
	// ErrDomainInvalid refuses something that is not a well-formed domain name.
	ErrDomainInvalid = errors.New("not a valid domain name")
)

// domainProfile is the IDNA profile every domain is normalised under. It is the LOOKUP
// profile (UTS-46 mapping, which lower-cases and folds) rather than the registration one,
// deliberately: the job here is to map every spelling a person or an identity provider might
// send onto the one stored spelling, so the input has to be MAPPED, not refused for being in
// the wrong case or width. The strictness is everything else: STD3 rules (no underscores or
// other non-hostname characters), the bidi rule, the DNS length limits and label validation
// (an invalid punycode label is refused, never passed through). Non-transitional, so "ß" stays
// "ß" and encodes to its own punycode rather than collapsing into "ss" (two different
// domains).
var domainProfile = idna.New(
	idna.MapForLookup(),
	idna.StrictDomainName(true),
	idna.BidiRule(),
	idna.VerifyDNSLength(true),
	idna.Transitional(false),
)

// normalizeHost is the IDNA step shared by NormalizeDomain and CanonicalEmail: lower-case
// ASCII, punycode for internationalised names, no trailing dot. It does not judge whether
// the name has enough labels to be a registrable domain.
func normalizeHost(raw string) (string, error) {
	// The IDNA mapper turns an invalid byte into U+FFFD rather than refusing it, so
	// malformed UTF-8 has to be refused here or it would be "normalised" into something.
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrDomainInvalid)
	}
	d := strings.TrimSpace(raw)
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return "", fmt.Errorf("%w: empty", ErrDomainInvalid)
	}
	// ONE trailing dot is the DNS root and is dropped above. A second is an empty label,
	// which the IDNA profile tolerates as a trailing root dot -- so "example.com.." would
	// come back as "example.com.", a second spelling of a stored domain.
	if strings.HasSuffix(d, ".") {
		return "", fmt.Errorf("%w: %q has an empty label", ErrDomainInvalid, raw)
	}
	// An address or a URL pasted where a domain belongs is a mistake worth naming rather than
	// letting the IDNA profile describe in its own words.
	if strings.ContainsAny(d, "@/:\\ ?#") {
		return "", fmt.Errorf("%w: %q must be a bare domain, not an address or URL", ErrDomainInvalid, raw)
	}
	ascii, err := domainProfile.ToASCII(d)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %v", ErrDomainInvalid, raw, err)
	}
	return strings.ToLower(ascii), nil
}

// NormalizeDomain returns the canonical stored form of a domain an identity provider may be
// asserted for: IDNA-mapped, lowercase ASCII (punycode for internationalised names), without
// a trailing dot, with at least two labels and not an IP address. Two inputs that name the
// same domain return the same string; anything that is not a domain name returns
// ErrDomainInvalid.
func NormalizeDomain(raw string) (string, error) {
	ascii, err := normalizeHost(raw)
	if err != nil {
		return "", err
	}
	if !strings.Contains(ascii, ".") {
		return "", fmt.Errorf("%w: %q has a single label", ErrDomainInvalid, raw)
	}
	if net.ParseIP(ascii) != nil {
		return "", fmt.Errorf("%w: %q is an IP address", ErrDomainInvalid, raw)
	}
	return ascii, nil
}

// CanonicalEmail returns the one spelling of an email address that is stored, looked up and
// compared: trimmed, the local part lower-cased, and the domain run through the same IDNA
// normalisation as every domain (so unicode and punycode spellings of a domain agree). An
// address that is not exactly "local@domain", or whose domain is not valid IDNA, returns
// ErrEmailInvalid and the caller must refuse it -- never fall back to the raw string.
//
// It is idempotent: CanonicalEmail of a canonical address is itself.
func CanonicalEmail(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrEmailInvalid)
	}
	e := strings.TrimSpace(raw)
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 || strings.Count(e, "@") != 1 {
		return "", fmt.Errorf("%w: expected local@domain", ErrEmailInvalid)
	}
	host, err := normalizeHost(e[at+1:])
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrEmailInvalid, err)
	}
	return strings.ToLower(e[:at]) + "@" + host, nil
}

// ErrEmailNotCanonical refuses storing an identity whose email is not already in canonical
// form: the store is the last line, so a caller that forgot CanonicalEmail fails loudly
// instead of creating a second spelling of an existing identity.
var ErrEmailNotCanonical = errors.New("identity email is not in canonical form")

// requireCanonicalEmail is the store's guard on every path that inserts an identity.
func requireCanonicalEmail(email string) error {
	c, err := CanonicalEmail(email)
	if err != nil {
		return err
	}
	if c != email {
		return fmt.Errorf("%w: %q", ErrEmailNotCanonical, email)
	}
	return nil
}

// EmailDomain returns the domain of an address already in canonical form.
func EmailDomain(canonical string) string {
	return canonical[strings.LastIndex(canonical, "@")+1:]
}
