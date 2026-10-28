// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/x509"
	"errors"
	"strings"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/nats-io/jwt/v2"
	"github.com/rs/zerolog/log"
)

// REGISTRATION ON FIRST CONNECT, BY CERTIFICATE. A device whose certificate pins no credential
// and whose chain reaches the anchor of one of the tenant's X.509 provisioning profiles is
// registered by that profile (model.ProvisionDevice, the same decision registration by secret
// makes) and then connects as the pinned device it has just become.

// x509ResultJitRevoked is registration refusing a device whose credential from this profile an
// operator disabled (ALREADY_PROVISIONED with the "revoked" reason), which stays refused until
// the device is reset.
const x509ResultJitRevoked = "jit_revoked"

// jitResultForCode is the counter result for a registration refusal code.
func jitResultForCode(code string) string { return "jit_" + strings.ToLower(code) }

// JitRegistrar is the model half registration on first connect calls. *model.Api is the
// implementation.
type JitRegistrar interface {
	X509ProfileAnchors(ctx context.Context) ([]model.X509ProfileAnchor, error)
	ProvisionDevice(ctx context.Context, request *model.ProvisionRequest) (*model.ProvisionResult, error)
}

// Allower is a token bucket's admission: core.TenantRateLimiter (keyed by tenant) and
// sourcelimit.Limiter (keyed by source) are the implementations.
type Allower interface {
	Allow(key string) bool
}

// JitConfig turns registration on first connect on, and bounds it (see unknownX509Pin).
type JitConfig struct {
	Registrar JitRegistrar
	// Tenants is the per-tenant ceiling: the SAME limiter the self-registration endpoint charges,
	// so a tenant's registrations by secret and by certificate share one allowance. Required.
	Tenants Allower
	// Sources is the per-source bucket, keyed on the client address the broker saw. Nil turns it
	// OFF, which is the default: it is only worth having where the MQTT Service preserves client
	// addresses, since behind a source NAT every device would share one bucket.
	Sources Allower
	// MaxInFlight is how many registrations one replica works on at once, inside the callout's
	// own bound. A pinned certificate never waits on it.
	MaxInFlight int
}

type jitState struct {
	reg     JitRegistrar
	tenants Allower
	sources Allower
	sem     chan struct{}
}

var errJitConfig = errors.New("registration on first connect needs a registrar, a per-tenant limiter and a positive in-flight bound")

// WithCertificateRegistration turns registration on first connect on. Without it a certificate
// that pins nothing is refused (unknown_pin). Call it before Start.
func (c *CalloutResponder) WithCertificateRegistration(cfg JitConfig) (*CalloutResponder, error) {
	if cfg.Registrar == nil || cfg.Tenants == nil || cfg.MaxInFlight < 1 {
		return nil, errJitConfig
	}
	c.jit = &jitState{reg: cfg.Registrar, tenants: cfg.Tenants, sources: cfg.Sources,
		sem: make(chan struct{}, cfg.MaxInFlight)}
	return c, nil
}

// JitEnabled reports whether registration on first connect is on, and its in-flight bound. A
// wiring test reads this to prove what the service actually runs with.
func (c *CalloutResponder) JitEnabled() (enabled bool, maxInFlight int, perSource bool) {
	if c.jit == nil {
		return false, 0, false
	}
	return true, cap(c.jit.sem), c.jit.sources != nil
}

// unknownX509Pin is the connect whose leaf pins no credential: the one place registration on
// first connect attaches. It is handed the presented chain and the tenant, and NOTHING here has
// authenticated either. It returns the credential the connect goes on with, or a refusal's
// result; when it returns a credential, result is what the granted connect is counted as.
//
// In order, each step costing less than the one after it and NOTHING written before the last:
//
//  1. The client id the device must connect with, computed from the certificate's own common
//     name (the token registration will give it is a function of that name), is compared with
//     the one it presented. A mismatch is refused HERE: a connect that registered the device and
//     was then refused for its client id would leave a device and a credential behind.
//  2. The per-source bucket, when it is on.
//  3. Which of the tenant's X.509 profiles the chain belongs to: the profiles whose anchor the
//     chain verifies to (x509trust.VerifyClientChain, per anchor). None verifying is
//     chain_invalid. Of those matching, the usable ones are the profiles that are enabled and
//     unexpired with an enabled anchor; if none is, the connect is anchor_disabled (a matching
//     anchor is off) or jit_provisioning_failed (the profile is). More than one usable profile
//     is jit_ambiguous: a certificate cannot say which of two profiles at the same anchor was
//     meant, and guessing would register a device of the wrong type, so it is refused and the
//     operator gives each profile its own anchor.
//  4. The in-flight slot (jit_busy), then the tenant's ceiling (jit_rate_limited). Only a
//     tenant whose chain verified is charged, so an invented tenant name costs the limiter
//     nothing.
//  5. model.ProvisionDevice with the verified certificate. A refusal is counted jit_<code>.
//  6. The credential, read back through the same uncached lookup a pinned connect uses.
func (c *CalloutResponder) unknownX509Pin(ctx context.Context, req jwt.AuthorizationRequest, tenant string,
	chain []*x509.Certificate) (*model.X509Connect, string) {
	host := req.ClientInformation.Host
	if c.jit == nil {
		log.Debug().Str("tenant", tenant).Str("host", host).
			Msg("Auth-callout refused a certificate connect: no credential pins this certificate.")
		return nil, x509ResultUnknownPin
	}
	leaf := chain[0]
	cn := leaf.Subject.CommonName

	// 1. The derived client id.
	if err := model.ValidateCertificateExternalId(cn); err != nil {
		log.Debug().Str("tenant", tenant).Str("host", host).
			Msg("Auth-callout refused a certificate registration: the common name is not a valid device external id.")
		return nil, jitResultForCode(model.ProvisioningCodeInvalid)
	}
	required, err := messaging.DeviceClientID(c.instanceId, tenant, model.DeriveX509DeviceToken(cn))
	if err != nil {
		log.Error().Err(err).Str("tenant", tenant).Msg("Auth-callout could not derive the MQTT client id of a certificate registration.")
		return nil, x509ResultError
	}
	if !messaging.DeviceClientIDMatches(req.ClientInformation.MQTT, required) {
		log.Debug().Str("tenant", tenant).Str("host", host).Str("presented", req.ClientInformation.MQTT).Str("required", required).
			Msg("Auth-callout refused a certificate registration whose MQTT client id is not the one its certificate derives.")
		return nil, x509ResultClientIDMismatch
	}

	// 2. The per-source bucket.
	if c.jit.sources != nil && !c.jit.sources.Allow(sourcelimit.SourceKey(host)) {
		log.Debug().Str("tenant", tenant).Str("host", host).Msg("Auth-callout refused a certificate registration: the source is over its rate.")
		return nil, x509ResultJitRateLimited
	}

	// 3. The profile.
	candidates, err := c.jit.reg.X509ProfileAnchors(ctx)
	if err != nil {
		c.logAuthFailure(tenant, err)
		return nil, x509ResultError
	}
	if len(candidates) == 0 {
		log.Debug().Str("tenant", tenant).Str("host", host).
			Msg("Auth-callout refused a certificate connect: no credential pins this certificate and the tenant has no certificate profile.")
		return nil, x509ResultUnknownPin
	}
	now := c.now()
	type match struct {
		cand     model.X509ProfileAnchor
		verified *x509trust.Verified
	}
	var matched, usable []match
	for _, cand := range candidates {
		if cand.Anchor == nil {
			continue
		}
		anchorCert, err := cand.Anchor.Certificate()
		if err != nil {
			log.Warn().Err(err).Str("tenant", tenant).Msg("Auth-callout could not read a stored trust anchor.")
			continue
		}
		verified, err := x509trust.VerifyClientChain(anchorCert, chain, now)
		if err != nil {
			continue
		}
		m := match{cand: cand, verified: verified}
		matched = append(matched, m)
		if cand.Usable(now) {
			usable = append(usable, m)
		}
	}
	switch {
	case len(matched) == 0:
		log.Debug().Str("tenant", tenant).Str("host", host).
			Msg("Auth-callout refused a certificate registration: the chain does not verify to any certificate profile's trust anchor.")
		return nil, x509ResultChainInvalid
	case len(usable) == 0:
		for _, m := range matched {
			if m.cand.Anchor == nil || !m.cand.Anchor.Enabled {
				log.Debug().Str("tenant", tenant).Str("host", host).
					Msg("Auth-callout refused a certificate registration: the matching trust anchor is disabled.")
				return nil, x509ResultAnchorDisabled
			}
		}
		log.Debug().Str("tenant", tenant).Str("host", host).
			Msg("Auth-callout refused a certificate registration: the matching provisioning profile is disabled or expired.")
		return nil, jitResultForCode(model.ProvisioningCodeFailed)
	case len(usable) > 1:
		log.Warn().Str("tenant", tenant).Int("profiles", len(usable)).
			Msg("Auth-callout refused a certificate registration: the chain belongs to more than one provisioning profile. " +
				"Give each certificate profile its own trust anchor.")
		return nil, x509ResultJitAmbiguous
	}
	chosen := usable[0]

	// 4. The budgets.
	select {
	case c.jit.sem <- struct{}{}:
		defer func() { <-c.jit.sem }()
	default:
		log.Debug().Str("tenant", tenant).Str("host", host).Msg("Auth-callout refused a certificate registration: too many are in flight.")
		return nil, x509ResultJitBusy
	}
	if c.metrics != nil && c.metrics.JitInFlight != nil {
		c.metrics.JitInFlight.Inc()
		defer c.metrics.JitInFlight.Dec()
	}
	if !c.jit.tenants.Allow(tenant) {
		log.Debug().Str("tenant", tenant).Str("host", host).Msg("Auth-callout refused a certificate registration: the tenant is over its rate.")
		return nil, x509ResultJitRateLimited
	}

	// 5. Registration.
	res, err := c.jit.reg.ProvisionDevice(ctx, &model.ProvisionRequest{
		Profile: chosen.cand.Profile,
		Certificate: &model.X509Registration{
			Leaf:              leaf,
			NotAfter:          chosen.verified.NotAfter,
			AnchorId:          chosen.cand.Anchor.ID,
			AnchorFingerprint: chosen.cand.Anchor.FingerprintSha256,
		},
	})
	if err != nil {
		var refusal *model.ProvisioningRefusal
		if errors.As(err, &refusal) {
			log.Debug().Str("tenant", tenant).Str("host", host).Str("code", refusal.Code).Str("reason", refusal.Reason).
				Msg("Auth-callout refused a certificate registration.")
			if errors.Is(err, model.ErrProvisioningRevoked) {
				return nil, x509ResultJitRevoked
			}
			return nil, jitResultForCode(refusal.Code)
		}
		c.logAuthFailure(tenant, err)
		return nil, x509ResultError
	}
	var outcome string
	switch res.Outcome {
	case model.ProvisionCreated:
		outcome = x509ResultJitCreated
	case model.ProvisionAdopted:
		outcome = x509ResultJitAdopted
	case model.ProvisionReplayed:
		outcome = x509ResultJitReplayed
	default:
		log.Error().Str("tenant", tenant).Str("outcome", string(res.Outcome)).Msg("Auth-callout got an unknown registration outcome.")
		return nil, x509ResultError
	}

	// 6. The credential, as a pinned connect finds it.
	found, err := c.api.ResolveX509Connect(ctx, x509trust.Fingerprint(leaf), c.now())
	if err != nil {
		if r := c.x509LookupRefusal(tenant, req, err); r != x509ResultUnknownPin {
			return nil, r
		}
		// Registered a moment ago and gone already (a delete in between): a failure, not a pin
		// that was never there.
		return nil, x509ResultError
	}
	return found, outcome
}
