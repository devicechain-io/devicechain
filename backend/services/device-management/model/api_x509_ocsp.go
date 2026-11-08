// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"fmt"
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

// SetOcspPolicy installs the policy. It is called once while the service wires itself.
func (api *Api) SetOcspPolicy(p OcspPolicy) { api.ocspPolicy.Store(&p) }

// OcspPolicy is the policy in force; the zero value (everything off) until SetOcspPolicy is called.
func (api *Api) OcspPolicy() OcspPolicy {
	if p := api.ocspPolicy.Load(); p != nil {
		return *p
	}
	return OcspPolicy{}
}

// OcspModeOf is the anchor's OCSP mode: OFF when none is stored. A stored value outside the
// vocabulary is an ERROR, never a default: a mode this build does not know is refused rather than
// guessed (an unknown mode that read as OFF would silently stop checking).
func (a *DeviceTrustAnchor) OcspModeOf() (x509trust.OCSPMode, error) {
	if !a.OcspMode.Valid {
		return x509trust.OCSPOff, nil
	}
	if m := x509trust.OCSPMode(a.OcspMode.String); m.Valid() {
		return m, nil
	}
	return "", fmt.Errorf("trust anchor %q stores the unknown OCSP mode %q", a.Token, a.OcspMode.String)
}

// OcspResponderUrlOf is the anchor's own (tenant-set) responder URL, "" when it has none (each
// certificate's own Authority Information Access entry is used).
func (a *DeviceTrustAnchor) OcspResponderUrlOf() string {
	if !a.OcspResponderUrl.Valid {
		return ""
	}
	return a.OcspResponderUrl.String
}

// ocspModeFromEnum is the mode for a GraphQL enum name.
func ocspModeFromEnum(name string) (x509trust.OCSPMode, bool) {
	if m := x509trust.OCSPMode(name); m.Valid() {
		return m, true
	}
	return "", false
}

// validateOcspResponderUrl refuses a URL that cannot be a responder: http or https only, a host,
// no userinfo, no fragment. It is advice at write time; the egress guard at dial time is the
// boundary (a host that resolves to a private address passes here and is refused there).
func validateOcspResponderUrl(raw string) error {
	if len(raw) > MaxOcspResponderUrlLength {
		return invalidAnchor("ocspResponderUrl is at most %d characters", MaxOcspResponderUrlLength)
	}
	if strings.TrimSpace(raw) != raw || raw == "" {
		return invalidAnchor("ocspResponderUrl must be an http or https URL with no surrounding space")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return invalidAnchor("ocspResponderUrl must be an http or https URL with a host")
	}
	if u.User != nil {
		return invalidAnchor("ocspResponderUrl must not carry credentials")
	}
	if strings.Contains(raw, "#") {
		return invalidAnchor("ocspResponderUrl must not carry a fragment")
	}
	return nil
}

// applyOcspSettings folds the OCSP mode and responder URL of an anchor update onto the locked
// anchor row (the caller saves it).
//
// Switching to ENFORCE_KNOWN or REQUIRE_CURRENT is refused while the instance's OCSP switch is
// off (nothing would be checked, and the anchor page would say otherwise). REQUIRE_CURRENT, whose
// contract is to refuse what it cannot confirm, is refused unless the instance allows it, and
// needs a responder URL the anchor itself names: with only certificates' own entries there is no
// way, at this point, to know a responder exists for them. The responder URL is TENANT-set (this
// is the tenant data plane, device-write permission) and is dialled with the same reach as the
// tenant's webhooks: the instance egress guard, operator allow list included.
func (api *Api) applyOcspSettings(locked *DeviceTrustAnchor, request *DeviceTrustAnchorUpdateRequest) error {
	if !request.OcspMode.Set && !request.OcspResponderUrl.Set {
		return nil
	}
	mode, err := locked.OcspModeOf()
	if err != nil {
		return err
	}
	respUrl := locked.OcspResponderUrlOf()
	if request.OcspMode.Set {
		if request.OcspMode.Value == nil {
			return invalidAnchor("ocspMode cannot be cleared: send OFF, ENFORCE_KNOWN or REQUIRE_CURRENT")
		}
		m, ok := ocspModeFromEnum(*request.OcspMode.Value)
		if !ok {
			return invalidAnchor("ocspMode must be OFF, ENFORCE_KNOWN or REQUIRE_CURRENT")
		}
		if m != x509trust.OCSPOff && m != mode {
			policy := api.OcspPolicy()
			if !policy.Enabled {
				return invalidAnchor("ocspMode %s is refused: OCSP is switched off on this instance "+
					"(deviceManagement.callout.ocsp.enabled), so no responder would ever be asked", m)
			}
			if m == x509trust.OCSPRequireCurrent && !policy.AllowRequireCurrent {
				return invalidAnchor("ocspMode REQUIRE_CURRENT is refused: this instance does not allow it " +
					"(deviceManagement.callout.ocsp.allowRequireCurrent). It refuses every certificate whose status cannot be " +
					"confirmed, which is only safe once the answer cache is kept warm")
			}
		}
		mode = m
		locked.OcspMode = sql.NullString{String: string(m), Valid: true}
	}
	if request.OcspResponderUrl.Set {
		if request.OcspResponderUrl.Value == nil {
			respUrl = ""
			locked.OcspResponderUrl = sql.NullString{}
		} else {
			if err := validateOcspResponderUrl(*request.OcspResponderUrl.Value); err != nil {
				return err
			}
			respUrl = *request.OcspResponderUrl.Value
			locked.OcspResponderUrl = sql.NullString{String: respUrl, Valid: true}
		}
	}
	if mode == x509trust.OCSPRequireCurrent && respUrl == "" {
		return invalidAnchor("ocspMode REQUIRE_CURRENT needs ocspResponderUrl: with only the certificates' own " +
			"entries there is no way to know a responder exists for them")
	}
	return nil
}
