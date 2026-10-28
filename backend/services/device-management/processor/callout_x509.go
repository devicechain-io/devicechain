// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/nats-io/jwt/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// X509Username is the credential id of the MQTT username "{tenant}:@x509", which selects the
// certificate path: the device authenticates with the client certificate it presented in the TLS
// handshake and presents no password.
const X509Username = "@x509"

// The results callout_x509_total is counted by. A closed list, so a series exists at zero for
// every one and an alert on a rate sees a zero series rather than none.
const (
	x509ResultOK                = "ok"
	x509ResultNoCert            = "no_cert"
	x509ResultChainInvalid      = "chain_invalid"
	x509ResultAnchorDisabled    = "anchor_disabled"
	x509ResultRevoked           = "revoked"
	x509ResultClientIDMismatch  = "client_id_mismatch"
	x509ResultUnknownPin        = "unknown_pin"
	x509ResultMisconfigured     = "misconfigured"
	x509ResultPasswordPresented = "password_presented"
	x509ResultUnsealed          = "unsealed"
	x509ResultCertMalformed     = "cert_malformed"
	x509ResultTenantRefused     = "tenant_refused"
	x509ResultError             = "error"
	x509ResultCertIgnored       = "x509_cert_ignored"
	// The registration results belong to registration on first connect, which counts them.
	x509ResultJitCreated     = "jit_created"
	x509ResultJitAdopted     = "jit_adopted"
	x509ResultJitRenewed     = "jit_renewed"
	x509ResultJitReplayed    = "jit_replayed"
	x509ResultJitRateLimited = "jit_rate_limited"
	x509ResultJitBusy        = "jit_busy"
)

var x509Results = []string{
	x509ResultOK, x509ResultNoCert, x509ResultChainInvalid, x509ResultAnchorDisabled, x509ResultRevoked,
	x509ResultClientIDMismatch, x509ResultUnknownPin, x509ResultMisconfigured, x509ResultPasswordPresented, x509ResultUnsealed, x509ResultCertMalformed,
	x509ResultTenantRefused, x509ResultError, x509ResultCertIgnored,
	x509ResultJitCreated, x509ResultJitAdopted, x509ResultJitRenewed, x509ResultJitReplayed,
	x509ResultJitRateLimited, x509ResultJitBusy,
}

// countX509 counts one certificate connect by result. A responder built without metrics counts
// nothing.
func (c *CalloutResponder) countX509(result string) {
	if c.metrics != nil && c.metrics.X509 != nil {
		c.metrics.X509.WithLabelValues(result).Inc()
	}
}

// refuseX509 counts a refusal and answers it with the generic denial every refusal gets, so the
// wire says nothing of which check failed. The counter is where an operator reads that.
func (c *CalloutResponder) refuseX509(result string) (string, string) {
	c.countX509(result)
	return "", genericAuthFailure
}

// authorizeX509 decides a connect whose username is "{tenant}:@x509". In order:
//
//  1. the deleted-tenant gate (as for every device connect);
//  2. a password is a refusal: this path has none to compare, and a client sending one is
//     confused about which credential it is presenting;
//  3. the presented certificates, bounded in count and size BEFORE any is parsed;
//  4. the pin lookup, UNCACHED (model.ResolveX509Connect): a disable must refuse the very next
//     connect, on every replica, so no answer is ever read from memory;
//  5. the credential's trust anchor, which must exist and be enabled;
//  6. the chain, verified against THAT anchor (x509trust.VerifyClientChain: the one definition of
//     the decision);
//  7. the same tail as every device connect (grantSession): the device enabled, the MQTT client
//     id its own, the erasure fence, the session clamped, then signed. The clamp is the earlier
//     of the credential's expiry and the earliest notAfter over the verified chain
//     (intermediates and anchor included), so a session never outlives a certificate that
//     vouched for it.
//
// Every refusal is the generic denial. The certificate is public and so is its pin, so none of
// this authenticates on what a device SAYS: the proof of possession is the TLS handshake on this
// very connection, which the broker has already completed before it asks us.
func (c *CalloutResponder) authorizeX509(req jwt.AuthorizationRequest, tenant string, presented *model.PresentedCredential, trusted bool) (string, string) {
	if !trusted {
		log.Warn().Str("tenant", tenant).Msg("Auth-callout refused a certificate connect whose request arrived unsealed " +
			"although an xkey is configured: the broker never sends one.")
		return c.refuseX509(x509ResultUnsealed)
	}
	if c.tenantDeleted(tenant) {
		log.Debug().Str("tenant", tenant).Msg("Auth-callout refused a certificate connect for a deleted tenant.")
		return c.refuseX509(x509ResultTenantRefused)
	}
	if presented.Secret != nil {
		log.Debug().Str("tenant", tenant).Str("host", req.ClientInformation.Host).
			Msg("Auth-callout refused a certificate connect that also presented a password.")
		return c.refuseX509(x509ResultPasswordPresented)
	}
	if req.TLS == nil || len(req.TLS.Certs) == 0 {
		log.Debug().Str("tenant", tenant).Str("host", req.ClientInformation.Host).
			Msg("Auth-callout refused a certificate connect: the client presented no certificate.")
		return c.refuseX509(x509ResultNoCert)
	}
	chain, err := x509trust.ParseChainPEM(req.TLS.Certs)
	if err != nil {
		log.Debug().Err(err).Str("tenant", tenant).Str("host", req.ClientInformation.Host).
			Msg("Auth-callout refused a certificate connect: the presented certificates are not acceptable.")
		return c.refuseX509(x509ResultCertMalformed)
	}
	pin := x509trust.Fingerprint(chain[0])

	ctx, cancel := context.WithTimeout(core.WithTenant(c.base, tenant), c.lookupTimeout)
	defer cancel()
	// 🔴 ResolveX509Connect, never a cached lookup. See the notes on it and on
	// AuthenticateDeviceConnect.
	found, err := c.api.ResolveX509Connect(ctx, pin, c.now())
	switch {
	case err == nil:
	case errors.Is(err, model.ErrCredentialNotResolved):
		return c.unknownX509Pin(ctx, req, tenant, chain)
	case errors.Is(err, model.ErrCredentialRevoked):
		log.Debug().Str("tenant", tenant).Str("host", req.ClientInformation.Host).
			Msg("Auth-callout refused a certificate connect: its credential is disabled.")
		return c.refuseX509(x509ResultRevoked)
	case errors.Is(err, model.ErrCredentialExpired):
		log.Debug().Str("tenant", tenant).Str("host", req.ClientInformation.Host).
			Msg("Auth-callout refused a certificate connect: its credential has expired.")
		return c.refuseX509(x509ResultChainInvalid)
	case errors.Is(err, model.ErrCredentialMisconfigured):
		log.Warn().Str("tenant", tenant).
			Msg("Auth-callout refused a certificate connect: the stored credential names no trust anchor and can never authenticate.")
		return c.refuseX509(x509ResultMisconfigured)
	default:
		c.logAuthFailure(tenant, err)
		return c.refuseX509(x509ResultError)
	}
	device := found.Device
	if device == nil || device.Token == "" {
		log.Error().Str("tenant", tenant).Msg("Auth-callout resolved a certificate credential to no device token.")
		return c.refuseX509(x509ResultError)
	}
	if found.Anchor == nil || !found.Anchor.Enabled {
		log.Debug().Str("tenant", tenant).Str("device", device.Token).Str("host", req.ClientInformation.Host).
			Msg("Auth-callout refused a certificate connect: its trust anchor is disabled.")
		return c.refuseX509(x509ResultAnchorDisabled)
	}
	anchorCert, err := found.Anchor.Certificate()
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Msg("Auth-callout could not read a stored trust anchor.")
		return c.refuseX509(x509ResultMisconfigured)
	}
	verified, err := x509trust.VerifyClientChain(anchorCert, chain, c.now())
	if err != nil {
		log.Debug().Err(err).Str("tenant", tenant).Str("device", device.Token).Str("host", req.ClientInformation.Host).
			Msg("Auth-callout refused a certificate connect: the chain does not verify to the credential's trust anchor.")
		return c.refuseX509(x509ResultChainInvalid)
	}
	// The device is a copy carrying the credential's expiry (withConnectExpiry); the session is
	// clamped to the earlier of that and the chain's.
	device.ConnectCredentialExpiresAt = earliestExpiry(device.ConnectCredentialExpiresAt, verified.NotAfter)

	userJWT, why := c.grantSession(ctx, req, tenant, device)
	switch why {
	case grantGranted:
		c.countX509(x509ResultOK)
		return userJWT, ""
	case grantRefusedDevice:
		// A disabled device is a withdrawal like a disabled credential.
		return c.refuseX509(x509ResultRevoked)
	case grantRefusedClient:
		return c.refuseX509(x509ResultClientIDMismatch)
	case grantRefusedFence:
		return c.refuseX509(x509ResultTenantRefused)
	case grantRefusedExpiry:
		return c.refuseX509(x509ResultChainInvalid)
	default:
		return c.refuseX509(x509ResultError)
	}
}

// unknownX509Pin is the connect whose leaf pins no credential. Today that is a refusal, counted
// as unknown_pin. It is the one place registration on first connect attaches: it is handed the
// presented chain and the tenant, and NOTHING here has authenticated either.
func (c *CalloutResponder) unknownX509Pin(_ context.Context, req jwt.AuthorizationRequest, tenant string, _ []*x509.Certificate) (string, string) {
	log.Debug().Str("tenant", tenant).Str("host", req.ClientInformation.Host).
		Msg("Auth-callout refused a certificate connect: no credential pins this certificate.")
	return c.refuseX509(x509ResultUnknownPin)
}

// earliestExpiry is the earlier of an optional credential expiry and a chain's notAfter.
func earliestExpiry(credentialExpiry sql.NullTime, chainNotAfter time.Time) sql.NullTime {
	if credentialExpiry.Valid && credentialExpiry.Time.Before(chainNotAfter) {
		return credentialExpiry
	}
	return sql.NullTime{Time: chainNotAfter, Valid: true}
}

// newX509Counter registers callout_x509_total with every result at zero.
func newX509Counter(ms *core.Microservice) *prometheus.CounterVec {
	v := ms.NewCounterVec("callout_x509_total",
		"Device connects by certificate (MQTT username {tenant}:@x509), by result. ok is a granted session; "+
			"no_cert, chain_invalid, anchor_disabled, revoked, client_id_mismatch, unknown_pin, misconfigured, "+
			"password_presented, unsealed, cert_malformed, tenant_refused and error are refusals, each answered with the same generic denial. "+
			"x509_cert_ignored counts a certificate presented with a token or password username, which authenticates "+
			"on that credential alone. The jit_ results are registration on first connect.",
		[]string{"result"})
	for _, r := range x509Results {
		v.WithLabelValues(r)
	}
	return v
}
