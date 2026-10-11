// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// A superuser asserts that an identity provider speaks for an email domain: every sign-in
// whose verified email is at that domain is routed to that provider and is trusted to be
// who the provider says. That makes a domain assertion a statement about WHO OWNS THE
// DOMAIN, which is exactly what these validators protect:
//
//   - a public suffix (com, co.uk) belongs to nobody, and an assertion for it would route —
//     and trust — the whole of that namespace;
//   - a freemail domain (gmail.com) belongs to a provider that will mint an address for
//     anyone, so "a verified address there" proves nothing about the person's employer;
//   - two spellings of one domain (Example.COM, an IDN and its punycode) must be ONE
//     assertion, or the uniqueness that keeps a domain with a single provider is bypassed
//     by capitalisation.

var (
	// ErrDomainInvalid refuses something that is not a well-formed domain name.
	ErrDomainInvalid = errors.New("not a valid domain name")
	// ErrDomainPublicSuffix refuses a domain that is itself a public suffix.
	ErrDomainPublicSuffix = errors.New("a public suffix cannot be asserted for an identity provider")
	// ErrDomainFreemail refuses a domain run by a mailbox provider that issues addresses to anyone.
	ErrDomainFreemail = errors.New("a freemail domain cannot be asserted for an identity provider")
)

// domainProfile is the IDNA profile domains are normalised under: UTS-46 mapping (which
// lowercases and folds), STD3 rules, and the DNS length limits. Non-transitional, so "ß"
// stays "ß" and encodes to its own punycode rather than collapsing into "ss".
var domainProfile = idna.New(
	idna.MapForLookup(),
	idna.StrictDomainName(true),
	idna.BidiRule(),
	idna.VerifyDNSLength(true),
	idna.Transitional(false),
)

// NormalizeDomain returns the canonical stored form of a domain: IDNA-mapped, lowercase
// ASCII (punycode for internationalised names), without a trailing dot. Two inputs that
// name the same domain return the same string; anything that is not a domain name returns
// ErrDomainInvalid.
//
// It deliberately does NOT judge whether the domain may be asserted — see ValidateDomain.
func NormalizeDomain(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return "", fmt.Errorf("%w: empty", ErrDomainInvalid)
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
	ascii = strings.ToLower(ascii)
	if !strings.Contains(ascii, ".") {
		return "", fmt.Errorf("%w: %q has a single label", ErrDomainInvalid, raw)
	}
	if net.ParseIP(ascii) != nil {
		return "", fmt.Errorf("%w: %q is an IP address", ErrDomainInvalid, raw)
	}
	return ascii, nil
}

// ValidateDomain normalises a domain and refuses it unless it is one an identity provider
// may be asserted for: not a public suffix and not a freemail domain (nor a subdomain of
// one). It returns the normalised form, which is what must be stored.
func ValidateDomain(raw string) (string, error) {
	d, err := NormalizeDomain(raw)
	if err != nil {
		return "", err
	}
	// EffectiveTLDPlusOne errors exactly when the name IS a public suffix ("co.uk", "com",
	// "github.io"): there is no registrable label above it. A name below a suffix — including
	// one below a private suffix like github.io, which its owner does control — passes.
	if _, err := publicsuffix.EffectiveTLDPlusOne(d); err != nil {
		return "", fmt.Errorf("%w: %q", ErrDomainPublicSuffix, d)
	}
	if isFreemail(d) {
		return "", fmt.Errorf("%w: %q", ErrDomainFreemail, d)
	}
	return d, nil
}

// isFreemail reports whether d is, or is a subdomain of, a listed freemail domain.
func isFreemail(d string) bool {
	for {
		if _, ok := freemailDomains[d]; ok {
			return true
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			return false
		}
		d = d[i+1:]
	}
}

// freemailDomains are mailbox providers that issue an address to anyone who asks. The
// list ships in code on purpose: it is a refusal the superuser cannot configure away, and
// a list in a setting would be a list an attacker with admin access edits first. It is a
// floor, not a catalogue — the providers that matter, not every regional one — and a
// domain missing from it is still protected by the connection's own required claims
// (a Google connection must pin hd; an Entra connection must pin tid).
var freemailDomains = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, d := range []string{
		// Google, Microsoft, Yahoo, Apple, AOL
		"gmail.com", "googlemail.com",
		"outlook.com", "hotmail.com", "live.com", "msn.com", "passport.com", "windowslive.com",
		"outlook.fr", "outlook.de", "outlook.es", "outlook.it", "outlook.jp",
		"hotmail.co.uk", "hotmail.fr", "hotmail.de", "hotmail.es", "hotmail.it", "live.co.uk", "live.fr",
		"yahoo.com", "ymail.com", "rocketmail.com", "yahoo.co.uk", "yahoo.co.jp", "yahoo.fr",
		"yahoo.de", "yahoo.es", "yahoo.it", "yahoo.ca", "yahoo.com.au", "yahoo.in", "yahoo.com.br",
		"icloud.com", "me.com", "mac.com",
		"aol.com", "aim.com",
		// Privacy-focused providers
		"proton.me", "protonmail.com", "pm.me", "tutanota.com", "tutanota.de", "tuta.io", "tuta.com",
		"fastmail.com", "fastmail.fm", "hey.com", "zoho.com", "zohomail.com", "mailfence.com",
		// Portal mail
		"gmx.com", "gmx.net", "gmx.de", "gmx.at", "web.de", "mail.com", "email.com", "inbox.com",
		"yandex.com", "yandex.ru", "ya.ru", "mail.ru", "bk.ru", "inbox.ru", "list.ru", "rambler.ru",
		"qq.com", "163.com", "126.com", "sina.com", "sohu.com", "naver.com", "daum.net", "hanmail.net",
		// Access-provider mail, where the customer base is the public
		"comcast.net", "verizon.net", "att.net", "sbcglobal.net", "bellsouth.net", "cox.net",
		"charter.net", "earthlink.net", "btinternet.com", "sky.com", "virginmedia.com", "talktalk.net",
		"orange.fr", "wanadoo.fr", "free.fr", "laposte.net", "libero.it", "t-online.de", "freenet.de",
		"rogers.com", "shaw.ca", "sympatico.ca", "bigpond.com", "optusnet.com.au",
		// Disposable
		"mailinator.com", "guerrillamail.com", "10minutemail.com", "yopmail.com", "tempmail.com",
	} {
		m[d] = struct{}{}
	}
	return m
}()
