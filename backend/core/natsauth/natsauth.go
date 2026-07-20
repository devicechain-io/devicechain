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
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"golang.org/x/crypto/bcrypt"
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

// servicePasswordBcryptCost is the bcrypt cost the service-password hash is minted
// at — matching `nats server passwd`'s default (11), a balance of brute-force
// resistance against the one-time per-connect verification cost on the broker.
const servicePasswordBcryptCost = 11

// Credentials is the set of broker-auth secrets minted once at bootstrap. The
// public issuer key + the BCRYPT service-password hash go into the NATS server
// config (a plain ConfigMap — so the hash, never the plaintext, is what a
// configmap-reader sees); the issuer seed + the PLAINTEXT service password go into
// the services' instance config (a Secret) — services present the plaintext and
// the broker bcrypt-compares it. The seed is used only by the device-management
// callout responder. See ADR-025 provisioning.
type Credentials struct {
	// IssuerPublic is the account nkey public key (A...) placed in the server
	// config's auth_callout.issuer — the trust anchor for callout-minted JWTs.
	IssuerPublic string
	// IssuerSeed is the account nkey seed (SA...) the callout responder signs
	// device user JWTs with. Secret.
	IssuerSeed string
	// ServicePassword is the plaintext password for the shared ServiceUser static
	// login, presented by every internal service. Secret — goes into the instance
	// config Secret, NOT the broker ConfigMap.
	ServicePassword string
	// ServicePasswordBcrypt is the bcrypt hash of ServicePassword, placed verbatim
	// in the broker's auth_users password field. nats-server detects the $2a$
	// prefix and bcrypt-compares the presented plaintext, so the broker config
	// carries no recoverable secret.
	ServicePasswordBcrypt string
}

// GenerateCredentials mints a fresh set of broker-auth credentials: an account
// nkey (the callout issuer) and a random service password (plus its bcrypt hash
// for the broker config). Uses crypto/rand throughout so nothing is predictable.
// The plaintext and its hash are minted together here so they can never drift.
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
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), servicePasswordBcryptCost)
	if err != nil {
		return Credentials{}, fmt.Errorf("hashing service password: %w", err)
	}
	return Credentials{
		IssuerPublic:          pub,
		IssuerSeed:            string(seed),
		ServicePassword:       pw,
		ServicePasswordBcrypt: string(hash),
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

// DevicePermissions returns the pub/sub subject allow-list for one device bound to
// tenant within instanceId.
//
// The grant is PER-DEVICE, not per-tenant. It previously allowed pub and sub across
// the whole `{instanceId}.{tenant}.>` tree, which had two consequences that were
// enforced by convention rather than by the broker:
//
//   - every device received every command issued to every device in its tenant, and
//     isolation rested on each device choosing to filter on the envelope's
//     deviceToken. A device that did not filter, or a compromised one, read them all.
//   - a device could publish anywhere under the tenant tree, including onto topics
//     the platform gives meaning to.
//
// Both are now closed at the authorization layer, which is the only place they can
// be closed against a device that misbehaves on purpose:
//
//   - SUB is the device's OWN command subject, plus the MQTT delivery subject.
//   - PUB is the device's own events topic and the shared command-responses subject
//     (one subject, one consumer; a response names its command by token, and the
//     tenant is derived from the subject rather than the payload).
//
// The instance-id prefix still closes the cross-instance hole on a shared broker
// (ADR-048): two instances' devices never share a subject tree even for
// identically-named tenants.
// It returns an error rather than a permission set when the device token is not
// token-grammar-safe. The token is spliced into three subjects, so one carrying
// "." / "*" / ">" would mint live wildcards INSIDE a device's own JWT — e.g. a token
// of `x.*` yields SUB "…device-commands.x.*". The DB grammar guard (ADR-042) should
// make that unreachable, but the tenant in this same flow gets a local check for
// exactly this reason rather than resting on a distant invariant, and the signed
// grant is the higher-blast-radius splice of the two.
func DevicePermissions(instanceId, tenant, deviceToken string) (jwt.Permissions, error) {
	if err := core.ValidateToken(deviceToken); err != nil {
		return jwt.Permissions{}, fmt.Errorf("refusing to build a grant for an invalid device token: %w", err)
	}
	// Built from the shared declaration, not a literal: the gateway subscribes to
	// the matching wildcard, so a shape written twice could let the grant and the
	// subscription drift apart — which is exactly how internal subjects ended up
	// inside the ingest subscription. See messaging.DeviceEventsWildcard.
	events := messaging.DeviceEventsSubject(instanceId, tenant, deviceToken)
	responses := fmt.Sprintf("%s.%s.%s", instanceId, tenant, messaging.SubjectCommandResponses)
	commands := messaging.DeviceScopedSubject(instanceId, tenant, messaging.SubjectDeviceCommands, deviceToken)

	var p jwt.Permissions
	p.Pub.Allow.Add(events, responses)
	p.Sub.Allow.Add(commands, MqttDeliverySubject)
	return p, nil
}

// SignDeviceUserJWT builds and signs the user JWT the callout returns for an
// authenticated device. subject MUST be the server-supplied user nkey from the
// authorization request (req.UserNkey); the JWT places the user in AppAccount
// (via aud) with instance-and-tenant-scoped permissions (ADR-048), signed by the
// issuer account seed.
func SignDeviceUserJWT(issuerSeed, userNkey, instanceId, tenant, deviceToken string, now time.Time, ttl time.Duration) (string, error) {
	akp, err := nkeys.FromSeed([]byte(issuerSeed))
	if err != nil {
		return "", fmt.Errorf("loading issuer seed: %w", err)
	}
	uc := jwt.NewUserClaims(userNkey)
	uc.Name = tenant
	uc.Audience = AppAccount
	perms, err := DevicePermissions(instanceId, tenant, deviceToken)
	if err != nil {
		return "", err
	}
	uc.Permissions = perms
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
