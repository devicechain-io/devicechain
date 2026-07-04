// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// AuthCalloutSubject is the well-known subject the NATS server publishes
// authorization requests on (config-mode auth callout, ADR-025).
const AuthCalloutSubject = "$SYS.REQ.USER.AUTH"

// authCalloutQueue is the queue group the responder subscribes under, so that
// across multiple device-management replicas exactly one instance handles each
// authorization request (rather than every replica racing to answer it).
const authCalloutQueue = "dc-device-callout"

// genericAuthFailure is the single error string returned for every device
// authentication failure. It is deliberately non-specific: a device (or an
// attacker) learns only that the connection was rejected, never which check
// failed (bad tenant vs unknown credential vs wrong secret), so the callout is
// not an oracle for probing the credential store.
const genericAuthFailure = "device authentication failed"

// CalloutResponder answers NATS auth-callout requests for device connections
// (ADR-025). A device connecting to the MQTT gateway (or NATS) presents
// username="{tenant}:{credentialId}" / password=secret; this resolves it via the
// ADR-014 AuthenticateDevice path and, on success, mints a NATS user JWT scoping
// the connection to its own tenant's subject tree. Internal services present the
// static service credential and are exempt from the callout, so only device
// connections ever reach here.
type CalloutResponder struct {
	conn       *nats.Conn
	api        model.DeviceManagementApi
	issuerSeed string
	ttl        time.Duration
	now        func() time.Time
	sub        *nats.Subscription
}

// NewCalloutResponder builds a responder over an established NATS connection (the
// service's own trusted connection), the device-management API, and the account
// issuer seed the minted user JWTs are signed with.
func NewCalloutResponder(conn *nats.Conn, api model.DeviceManagementApi, issuerSeed string) *CalloutResponder {
	return &CalloutResponder{
		conn:       conn,
		api:        api,
		issuerSeed: issuerSeed,
		ttl:        natsauth.DefaultUserJWTTTL,
		now:        time.Now,
	}
}

// Start subscribes to the auth-callout subject. Once subscribed, every non-exempt
// (i.e. device) connection is gated by handle. Each request is handled on its own
// goroutine so one slow AuthenticateDevice call (a DB round-trip + secret check)
// does not stall the whole queue — nats.go dispatches a subscription's callbacks
// serially, and a connect storm within the broker's auth window otherwise backs
// up. The DB connection pool is the natural backpressure on concurrency.
func (c *CalloutResponder) Start() error {
	sub, err := c.conn.QueueSubscribe(AuthCalloutSubject, authCalloutQueue, func(msg *nats.Msg) {
		go c.handle(msg)
	})
	if err != nil {
		return err
	}
	c.sub = sub
	log.Info().Msg("Device auth-callout responder subscribed; device connections are now broker-authenticated.")
	return nil
}

// Stop tears down the subscription.
func (c *CalloutResponder) Stop() error {
	if c.sub != nil {
		return c.sub.Unsubscribe()
	}
	return nil
}

// handle processes one authorization request: decode, decide, and reply with a
// scoped user JWT or a denial.
func (c *CalloutResponder) handle(msg *nats.Msg) {
	reqClaims, err := jwt.DecodeAuthorizationRequestClaims(string(msg.Data))
	if err != nil {
		// A malformed request can't be attributed to a user nkey/server; drop it
		// (the connection times out server-side) rather than sign a bogus reply.
		log.Warn().Err(err).Msg("Dropping undecodable auth-callout request.")
		return
	}
	req := reqClaims.AuthorizationRequest
	// The response (and the user JWT) must be keyed to the server-supplied user
	// nkey; without it jwt.NewAuthorization*Claims("") returns nil and respond
	// would panic (crashing the responder). A well-formed request always carries
	// one, so an empty value is a malformed/hostile request — drop it.
	if req.UserNkey == "" {
		log.Warn().Msg("Dropping auth-callout request with no user nkey.")
		return
	}
	userJWT, errMsg := c.authorize(req)
	c.respond(msg, req.Server.ID, req.UserNkey, userJWT, errMsg)
}

// authorize resolves a decoded request to either a signed user JWT (grant) or a
// non-empty error message (deny). It touches the credential store and signs a
// JWT but does no NATS I/O, so the grant/deny decision is unit-testable in
// isolation. Every failure returns the same generic message (see
// genericAuthFailure).
func (c *CalloutResponder) authorize(req jwt.AuthorizationRequest) (userJWT string, errMsg string) {
	tenant, presented, ok := parseDeviceCredential(req.ConnectOptions.Username, req.ConnectOptions.Password)
	if !ok {
		return "", genericAuthFailure
	}

	// AuthenticateDevice is tenant-scoped via the context tenant (the fail-closed
	// DB callback), so a credential is only ever resolved within its own tenant.
	ctx := core.WithTenant(context.Background(), tenant)
	if _, err := c.api.AuthenticateDevice(ctx, presented, c.now()); err != nil {
		log.Debug().Err(err).Str("tenant", tenant).Msg("Auth-callout rejected a device connection.")
		return "", genericAuthFailure
	}

	signed, err := natsauth.SignDeviceUserJWT(c.issuerSeed, req.UserNkey, tenant, c.now(), c.ttl)
	if err != nil {
		log.Error().Err(err).Msg("Auth-callout failed to sign a device user JWT.")
		return "", genericAuthFailure
	}
	return signed, ""
}

// respond signs and publishes the authorization response. Exactly one of userJWT
// / errMsg is non-empty.
func (c *CalloutResponder) respond(msg *nats.Msg, serverID, userNkey, userJWT, errMsg string) {
	resp, err := natsauth.EncodeAuthResponse(c.issuerSeed, serverID, userNkey, userJWT, errMsg)
	if err != nil {
		log.Error().Err(err).Msg("Auth-callout failed to encode a response.")
		return
	}
	if err := c.conn.Publish(msg.Reply, []byte(resp)); err != nil {
		log.Error().Err(err).Msg("Auth-callout failed to publish a response.")
	}
}

// parseDeviceCredential maps a connect username/password to a tenant and a
// PresentedCredential (ADR-025). The username is "{tenant}:{credentialId}" — the
// ":" delimiter is unambiguous because the ADR-042 token grammar excludes it from
// both tenant ids and credential ids. A non-empty password means MQTT_BASIC (the
// password is the compared secret); an empty password means ACCESS_TOKEN (the
// credentialId is itself the bearer). Returns ok=false when the username is
// malformed, which the caller turns into a generic denial.
func parseDeviceCredential(username, password string) (string, *model.PresentedCredential, bool) {
	parts := strings.SplitN(username, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", nil, false
	}
	tenant, credentialID := parts[0], parts[1]
	// Validate the tenant against the token grammar locally before it is spliced
	// into the device's `dc.{tenant}.>` permission subject. The grammar is already
	// enforced when a tenant is created (it excludes `.`/`*`/`>`/`:`), so this is
	// defense-in-depth that keeps the "no subject injection" property local to the
	// callout rather than resting on a distant invariant.
	if core.ValidateToken(tenant) != nil {
		return "", nil, false
	}
	if password != "" {
		secret := password
		return tenant, &model.PresentedCredential{
			CredentialType: string(model.CredentialMqttBasic),
			CredentialId:   credentialID,
			Secret:         &secret,
		}, true
	}
	return tenant, &model.PresentedCredential{
		CredentialType: string(model.CredentialAccessToken),
		CredentialId:   credentialID,
	}, true
}
