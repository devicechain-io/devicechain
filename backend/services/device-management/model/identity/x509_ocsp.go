// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

// The pure parts of an anchor's OCSP settings: the instance policy's shape, the mode vocabulary
// and the responder URL check. Applying them to an anchor is model's (applyOcspSettings).

import (
	"net/url"
	"strings"

	"github.com/devicechain-io/dc-microservice/x509trust"
)

// MaxOcspResponderUrlLength bounds an anchor's responder URL (the column is varchar(2048)).
const MaxOcspResponderUrlLength = 2048

// OcspPolicy is the instance-wide part of the OCSP check, from the fixed configuration. The zero
// value is the safe one: nothing may be switched on.
type OcspPolicy struct {
	// Enabled is the master switch (deviceManagement.callout.ocsp.enabled).
	Enabled bool
	// AllowRequireCurrent additionally lets an anchor use REQUIRE_CURRENT.
	AllowRequireCurrent bool
}

// OcspModeFromEnum is the mode for a GraphQL enum name.
func OcspModeFromEnum(name string) (x509trust.OCSPMode, bool) {
	if m := x509trust.OCSPMode(name); m.Valid() {
		return m, true
	}
	return "", false
}

// ValidateOcspResponderUrl refuses a URL that cannot be a responder: http or https only, a host,
// no userinfo, no fragment. It is advice at write time; the egress guard at dial time is the
// boundary (a host that resolves to a private address passes here and is refused there).
func ValidateOcspResponderUrl(raw string) error {
	if len(raw) > MaxOcspResponderUrlLength {
		return InvalidAnchor("ocspResponderUrl is at most %d characters", MaxOcspResponderUrlLength)
	}
	if strings.TrimSpace(raw) != raw || raw == "" {
		return InvalidAnchor("ocspResponderUrl must be an http or https URL with no surrounding space")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return InvalidAnchor("ocspResponderUrl must be an http or https URL with a host")
	}
	if u.User != nil {
		return InvalidAnchor("ocspResponderUrl must not carry credentials")
	}
	if strings.Contains(raw, "#") {
		return InvalidAnchor("ocspResponderUrl must not carry a fragment")
	}
	return nil
}
