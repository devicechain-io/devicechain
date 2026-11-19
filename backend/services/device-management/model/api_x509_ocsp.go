// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-device-management/model/identity"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

// SetOcspPolicy installs the policy. It is called once while the service wires itself.
func (api *Api) SetOcspPolicy(p OcspPolicy) { api.ocspPolicy.Store(&p) }

// OcspPolicy is the policy in force; the zero value (everything off) until SetOcspPolicy is called.
func (api *Api) OcspPolicy() OcspPolicy {
	if p := api.ocspPolicy.Load(); p != nil {
		return *p
	}
	return OcspPolicy{}
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
			return identity.InvalidAnchor("ocspMode cannot be cleared: send OFF, ENFORCE_KNOWN or REQUIRE_CURRENT")
		}
		m, ok := identity.OcspModeFromEnum(*request.OcspMode.Value)
		if !ok {
			return identity.InvalidAnchor("ocspMode must be OFF, ENFORCE_KNOWN or REQUIRE_CURRENT")
		}
		if m != x509trust.OCSPOff && m != mode {
			policy := api.OcspPolicy()
			if !policy.Enabled {
				return identity.InvalidAnchor("ocspMode %s is refused: OCSP is switched off on this instance "+
					"(deviceManagement.callout.ocsp.enabled), so no responder would ever be asked", m)
			}
			if m == x509trust.OCSPRequireCurrent && !policy.AllowRequireCurrent {
				return identity.InvalidAnchor("ocspMode REQUIRE_CURRENT is refused: this instance does not allow it " +
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
			if err := identity.ValidateOcspResponderUrl(*request.OcspResponderUrl.Value); err != nil {
				return err
			}
			respUrl = *request.OcspResponderUrl.Value
			locked.OcspResponderUrl = sql.NullString{String: respUrl, Valid: true}
		}
	}
	if mode == x509trust.OCSPRequireCurrent && respUrl == "" {
		return identity.InvalidAnchor("ocspMode REQUIRE_CURRENT needs ocspResponderUrl: with only the certificates' own " +
			"entries there is no way to know a responder exists for them")
	}
	return nil
}
