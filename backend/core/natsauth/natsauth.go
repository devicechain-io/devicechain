// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package natsauth holds the broker-authentication primitives shared across the
// platform (ADR-025): minting the NATS auth-callout credentials, and signing the
// per-device user JWTs the callout responder returns.
//
// The device plane runs NATS auth callout in config mode (server 2.10+). A single
// `APP` account holds both the internal services (a static shared credential,
// exempt from the callout) and the devices (placed in APP by callout-minted JWTs
// with tenant-scoped permissions). The callout's trust anchor is an account nkey:
// its public key is the server-config `issuer`, and its seed — held only by the
// responder — signs the user JWTs. See ADR-025.
package natsauth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// AppAccount is the single NATS account every connection lands in — services
// (static, exempt) and devices (callout-minted) alike. Isolation is by per-user
// permission, not by account (ADR-025). It is the `aud` set on device user JWTs
// and the account name in the server config.
const AppAccount = "APP"

// ServiceUser is the shared static username every internal service presents to
// the broker. It is listed in the callout's auth_users, so service connections
// bypass the device callout and get the account's full permissions.
const ServiceUser = "dc_service"

// DefaultUserJWTTTL bounds how long a callout-minted device user JWT is valid.
// The NATS server arms a timer at the JWT's expiry that force-closes the device's
// connection, after which the reconnect re-runs the callout (an enabled-only
// credential lookup). So this is the maximum time a credential disabled/deleted
// mid-session keeps its LIVE connection: revocation takes effect immediately for
// new connects, and within one TTL for an already-connected device. 12h trades a
// bounded revocation window against reconnect churn; a shorter TTL (or a $SYS kick
// for prompt revocation) is a later refinement if a tenant needs it.
const DefaultUserJWTTTL = 12 * time.Hour

// Credentials is the set of broker-auth secrets minted once at bootstrap. The
// public issuer key + service password go into the NATS server config; the issuer
// seed + service password go into the services' instance config (the seed only
// the device-management callout responder uses). See ADR-025 provisioning.
type Credentials struct {
	// IssuerPublic is the account nkey public key (A...) placed in the server
	// config's auth_callout.issuer — the trust anchor for callout-minted JWTs.
	IssuerPublic string
	// IssuerSeed is the account nkey seed (SA...) the callout responder signs
	// device user JWTs with. Secret.
	IssuerSeed string
	// ServicePassword is the password for the shared ServiceUser static login.
	// Secret.
	ServicePassword string
}

// GenerateCredentials mints a fresh set of broker-auth credentials: an account
// nkey (the callout issuer) and a random service password. Uses crypto/rand
// throughout so nothing is predictable.
func GenerateCredentials() (Credentials, error) {
	akp, err := nkeys.CreateAccount()
	if err != nil {
		return Credentials{}, fmt.Errorf("creating issuer account key: %w", err)
	}
	pub, err := akp.PublicKey()
	if err != nil {
		return Credentials{}, fmt.Errorf("reading issuer public key: %w", err)
	}
	seed, err := akp.Seed()
	if err != nil {
		return Credentials{}, fmt.Errorf("reading issuer seed: %w", err)
	}
	pw, err := randomHex(24)
	if err != nil {
		return Credentials{}, fmt.Errorf("generating service password: %w", err)
	}
	return Credentials{
		IssuerPublic:    pub,
		IssuerSeed:      string(seed),
		ServicePassword: pw,
	}, nil
}

// MqttDeliverySubject is the internal JetStream delivery subject the NATS MQTT
// gateway subscribes a device to for QoS-1+ message delivery. A device's user
// permission allow-list must include it or QoS-1 SUBSCRIBE (e.g. command
// delivery) fails with a permission violation. The `<nuid>` suffixes are
// per-session and unguessable, and the gateway blocks devices from subscribing to
// `$MQTT.*` topics directly (mqttSubPrefix filter) — and device JWTs are pinned
// to the MQTT connection type (see SignDeviceUserJWT) so the credential can't open
// a raw NATS connection to abuse this subject — so granting it does not widen
// cross-tenant exposure.
const MqttDeliverySubject = "$MQTT.sub.>"

// DevicePermissions returns the pub/sub subject allow-list for a device bound to
// tenant. A device may only publish and subscribe under its own tenant's tree
// (`dc.{tenant}.>` — the MQTT-topic→subject mapping of `dc/{tenant}/#`), which is
// the subject-authorization binding that closes the cross-tenant hole (ADR-025).
// `dc.{tenant}` (no trailing token) is included because an MQTT `dc/{tenant}/#`
// filter maps to a subscription on both the wildcard tree and that level-up
// subject. Tenant granularity in v1; per-device (`dc.{tenant}.{token}.>`) is a
// later refinement.
func DevicePermissions(tenant string) jwt.Permissions {
	tree := fmt.Sprintf("dc.%s.>", tenant)
	levelUp := fmt.Sprintf("dc.%s", tenant)
	var p jwt.Permissions
	p.Pub.Allow.Add(tree)
	p.Sub.Allow.Add(tree, levelUp, MqttDeliverySubject)
	return p
}

// SignDeviceUserJWT builds and signs the user JWT the callout returns for an
// authenticated device. subject MUST be the server-supplied user nkey from the
// authorization request (req.UserNkey); the JWT places the user in AppAccount
// (via aud) with tenant-scoped permissions, signed by the issuer account seed.
func SignDeviceUserJWT(issuerSeed, userNkey, tenant string, now time.Time, ttl time.Duration) (string, error) {
	akp, err := nkeys.FromSeed([]byte(issuerSeed))
	if err != nil {
		return "", fmt.Errorf("loading issuer seed: %w", err)
	}
	uc := jwt.NewUserClaims(userNkey)
	uc.Name = tenant
	uc.Audience = AppAccount
	uc.Permissions = DevicePermissions(tenant)
	// Pin the credential to the MQTT connection type: the device plane is MQTT, so
	// a device credential must not be usable to open a raw NATS connection (which
	// would let it subscribe to the shared MqttDeliverySubject and read other
	// tenants' QoS-1 deliveries). The MQTT gateway path is unaffected.
	uc.AllowedConnectionTypes.Add(jwt.ConnectionTypeMqtt)
	// Only Expires honors the injected clock; jwt.Encode stamps IssuedAt itself.
	uc.Expires = now.Add(ttl).Unix()
	return uc.Encode(akp)
}

// EncodeAuthResponse builds and signs the AuthorizationResponseClaims the callout
// publishes back to the server. userNkey MUST be the request's user nkey (the
// server rejects a response whose Subject is not the expected user), and
// serverID MUST be the requesting server's id from req.Server.ID (the server
// checks the response Audience equals its own id). Exactly one of userJWT /
// errMsg is set: userJWT authorizes the connection, errMsg denies it (and is
// logged by the server). Signed by the issuer account seed.
func EncodeAuthResponse(issuerSeed, serverID, userNkey, userJWT, errMsg string) (string, error) {
	akp, err := nkeys.FromSeed([]byte(issuerSeed))
	if err != nil {
		return "", fmt.Errorf("loading issuer seed: %w", err)
	}
	rc := jwt.NewAuthorizationResponseClaims(userNkey)
	rc.Audience = serverID
	if errMsg != "" {
		rc.Error = errMsg
	} else {
		rc.Jwt = userJWT
	}
	return rc.Encode(akp)
}

// randomHex returns a hex-encoded random string of nbytes of entropy. Hex keeps
// the value free of any character that could need escaping in the NATS config or
// the instance-config JSON it travels through.
func randomHex(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
