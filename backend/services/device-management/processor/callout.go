// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// AuthCalloutSubject is the well-known subject the NATS server publishes
// authorization requests on (config-mode auth callout, ADR-025).
const AuthCalloutSubject = "$SYS.REQ.USER.AUTH"

// authCalloutQueue is the queue group the responder subscribes under, so that
// across multiple device-management replicas exactly one replica handles each
// authorization request (rather than every replica racing to answer it).
//
// It is deliberately NOT namespaced per ADR-048 instance (unlike the device
// subject tree, which is). The auth callout is broker-global by construction —
// one `$SYS.REQ.USER.AUTH` subject and one `auth_callout.issuer` account key per
// broker — so on a shared broker all instances necessarily share a single callout
// responder pool; a per-instance queue group would merely give every instance's
// pool a copy of each request and have them race grant against deny. Correct
// per-instance attribution on a shared broker — the responder resolving which
// instance a connecting device belongs to (from the connect credentials, rather
// than assuming its own, as authorize does via c.instanceId) and signing the JWT
// for that instance's tree — is part of the shared-vs-dedicated infra-profile work
// (ADR-048 D2). Today a broker serves a single instance, so the responder's own
// instance id is the correct scope.
const authCalloutQueue = "dc-device-callout"

// genericAuthFailure is the single error string returned for every device
// authentication failure. It is deliberately non-specific: a device (or an
// attacker) learns only that the connection was rejected, never which check
// failed (bad tenant vs unknown credential vs wrong secret), so the callout is
// not an oracle for probing the credential store.
const genericAuthFailure = "device authentication failed"

// CalloutResponder answers NATS auth-callout requests for device connections
// (ADR-025). A device connecting to the MQTT gateway presents
// username="{tenant}:{credentialId}" / password=secret AND an MQTT client id under
// "{instanceId}:{tenant}:{deviceToken}"; this resolves the credential (ADR-014) —
// a password through credential.Checker, under a per-username backoff — and, on
// success, mints a NATS user JWT confining the connection to that one device's
// subjects. Internal services present the static service credential and are exempt
// from the callout, so only device connections ever reach here.
//
// The client id is part of the contract rather than firmware's business because it
// is the key the broker files a device's MQTT session under — see the comment on
// the check in authorize, and messaging.DeviceClientID for the shape.
type CalloutResponder struct {
	conn       *nats.Conn
	api        model.DeviceManagementApi
	issuerSeed string
	instanceId string
	ttl        time.Duration
	now        func() time.Time
	sub        *nats.Subscription
	// tenantDeleted reports whether a tenant has been through the ADR-077 delete door.
	// A closure rather than the resolver type so this package does not take a
	// dependency on core/governance for one boolean, and so a test can drive the gate
	// without a live user-management — the same injection shape event-sources uses for
	// its ingest limiter. Never nil; see NewCalloutResponder.
	tenantDeleted func(tenant string) bool
	// creds compares every MQTT password behind DeviceCredentialPolicy's backoff. Never
	// nil: NewCalloutResponder refuses to build a responder without one.
	creds *credential.Checker
	// unavailableLog rate-limits the warning for an attempt store that cannot be
	// reached. While it is down EVERY password connect is refused, so a line per
	// connect would be one per device in a reconnecting fleet.
	unavailableLog rateLimitedLog
}

// rateLimitedLog lets one line through per interval and counts the ones it held back.
type rateLimitedLog struct {
	mu         sync.Mutex
	last       time.Time
	suppressed int
}

// unavailableLogInterval is the most often the callout logs an unreachable attempt store.
const unavailableLogInterval = time.Minute

// allow reports whether a line may be written at now, and how many were held back
// since the last one that was.
func (r *rateLimitedLog) allow(now time.Time) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.last.IsZero() && now.Sub(r.last) < unavailableLogInterval {
		r.suppressed++
		return false, 0
	}
	held := r.suppressed
	r.last, r.suppressed = now, 0
	return true, held
}

// errNoCredentialChecker is NewCalloutResponder's refusal to build a responder that
// would compare device passwords with no backoff.
var errNoCredentialChecker = errors.New("the device auth callout needs a credential checker: " +
	"without one every MQTT password would be compared unthrottled")

// NewCalloutResponder builds a responder over an established NATS connection (the
// service's own trusted connection), the device-management API, the account issuer
// seed the minted user JWTs are signed with, and the instance id the minted device
// permissions are scoped under (ADR-048), so a device is confined to its own
// instance's subject tree on a shared broker.
//
// tenantDeleted gates connects on the ADR-077 tenant lifecycle. A nil closure disables
// the gate (every tenant reads live), which is what an instance with no reachable
// user-management gets — the same fail-open the resolver itself takes, made explicit
// here so an unwired gate behaves like an unresolvable one rather than panicking or
// refusing everything. main logs when it takes that path.
//
// creds is the Checker every MQTT password is compared through (built over the
// device credential-attempt store, with DeviceCredentialPolicies). It is REQUIRED: a
// nil one is refused with an error rather than defaulting to a compare with no backoff.
func NewCalloutResponder(conn *nats.Conn, api model.DeviceManagementApi, creds *credential.Checker, issuerSeed, instanceId string, tenantDeleted func(string) bool) (*CalloutResponder, error) {
	if creds == nil {
		return nil, errNoCredentialChecker
	}
	if tenantDeleted == nil {
		tenantDeleted = func(string) bool { return false }
	}
	return &CalloutResponder{
		conn:          conn,
		api:           api,
		creds:         creds,
		issuerSeed:    issuerSeed,
		instanceId:    instanceId,
		ttl:           natsauth.DefaultUserJWTTTL,
		now:           time.Now,
		tenantDeleted: tenantDeleted,
	}, nil
}

// Start subscribes to the auth-callout subject. Once subscribed, every non-exempt
// (i.e. device) connection is gated by handle. Each request is handled on its own
// goroutine so one slow check (for a password, attempt-store reads and writes on
// JetStream plus a DB round-trip; for an access token, the DB round-trip) does not
// stall the whole queue — nats.go dispatches a subscription's callbacks serially, and
// a connect storm within the broker's auth window otherwise backs up. The DB
// connection pool and JetStream's own request handling are the backpressure on
// concurrency.
//
// 🔴 SYNCED, and this is the sharpest instance of that rule on the platform. A bare
// QueueSubscribe returns before the server has registered anything, and the publisher
// here is nats-server ITSELF — no client connection, so no ordering to borrow. A
// device connecting in that window has its authorization request published to nobody,
// core NATS drops it, and the device is refused with a bare EOF while the line below
// says device connections are now authenticated. callout_race_test.go holds the SUB
// in flight and drives exactly that refusal.
//
// The queue group is broker-global (see authCalloutQueue), so during a rolling restart
// a peer replica already holds the interest and covers the gap. What this closes is
// the COLD START of the whole responder pool — which is precisely when a fleet-wide
// reconnect storm arrives.
func (c *CalloutResponder) Start() error {
	sub, err := messaging.QueueSubscribeSynced(c.conn, AuthCalloutSubject, authCalloutQueue, func(msg *nats.Msg) {
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

	// The ADR-077 lifecycle gate. A device credential outlives the operator's delete —
	// nothing cascades to the credential store — so without this a deleted tenant's
	// fleet reconnects and keeps writing into data that is being reclaimed underneath
	// it. Checked HERE, before the credential store is touched: it is a cached read, it
	// costs nothing on the common path, and refusing before the DB round-trip means a
	// deleted tenant's reconnect storm cannot load the credential store either.
	//
	// It rides on genericAuthFailure like every other refusal, deliberately: telling a
	// caller "that tenant is being deleted" would make the callout an oracle for which
	// tenants exist and what is happening to them.
	if c.tenantDeleted(tenant) {
		log.Debug().Str("tenant", tenant).Msg("Auth-callout refused a device connect for a deleted tenant.")
		return "", genericAuthFailure
	}

	// The credential lookup is tenant-scoped via the context tenant (the fail-closed
	// DB callback), so a credential is only ever resolved within its own tenant.
	ctx := core.WithTenant(context.Background(), tenant)
	// The authenticated DEVICE — not just "some device in this tenant" — decides the
	// grant, so the JWT can confine this connection to its own command subject and its
	// own events topic. The result was previously discarded, which is why the grant
	// could only ever be tenant-wide.
	var device *model.Device
	var err error
	if presented.Secret != nil {
		device, err = c.checkPassword(ctx, tenant, presented)
	} else {
		// 🔴 AN ACCESS-TOKEN CONNECT IS NOT THROTTLED, and that is a known gap rather
		// than an oversight. The token IS the credential id, matched by a database
		// equality lookup, so every guess is a different principal: a per-principal
		// backoff would slow nothing and write one attempt record per guess. Limiting
		// it needs a key the guesser cannot vary (per source or per tenant), which
		// this does not have.
		device, err = c.api.AuthenticateDevice(ctx, presented, c.now())
	}
	if err != nil {
		c.logAuthFailure(tenant, err)
		return "", genericAuthFailure
	}
	if device == nil || device.Token == "" {
		// Fail closed: without a device token the only grant we could mint would be a
		// tenant-wide one, which is exactly what this change removes.
		log.Error().Str("tenant", tenant).Msg("Auth-callout resolved a credential to no device token.")
		return "", genericAuthFailure
	}

	// An MQTT client id is a SESSION KEY: unchecked, any device that can authenticate at
	// all can evict any other device's session, across tenants. The full reasoning — and
	// why this admits a device-chosen discriminator rather than one exact value — is on
	// messaging.DeviceClientID.
	//
	// Two things about it are local to here, and neither is stated there.
	//
	// It is checked at the CALLOUT because nats-server consults the callout before it
	// looks a session up (mqttProcessConnect authorizes, then createOrRestoreSession), so
	// a refusal prevents the takeover instead of undoing one. And it cannot move earlier
	// within this function, however tempting the deleted-tenant gate above makes it look:
	// the required id is derived from the DEVICE TOKEN, which only a successful
	// credential check produces. So unlike the lifecycle gate, a wrong client id costs a
	// full credential check — the right trade anyway, since refusing sooner would mean
	// refusing on a value we had not yet earned the right to compare against. It is not
	// a credential FAILURE, so it is not charged to the password backoff: the check
	// that just succeeded has already cleared that username's record.
	//
	// A raw NATS connection reports an empty client id and is refused by the same
	// comparison. That is not collateral damage: a device credential is already pinned to
	// ConnectionTypeMqtt in the grant below, so a non-MQTT device connect could only ever
	// be rejected one step later — this is the same verdict, reached before a JWT is minted.
	required, err := messaging.DeviceClientID(c.instanceId, tenant, device.Token)
	if err != nil {
		// Unreachable while the ADR-042 grammar guard holds on all three values; a
		// denial rather than an admission if it ever does not.
		log.Error().Err(err).Str("tenant", tenant).Msg("Auth-callout could not derive the required MQTT client id.")
		return "", genericAuthFailure
	}
	if !messaging.DeviceClientIDMatches(req.ClientInformation.MQTT, required) {
		// Logged with both values because "my device will not connect" is otherwise
		// undiagnosable from the outside — the wire response is the same generic refusal
		// every other failure returns, deliberately, so the callout does not become an
		// oracle for which tenants and devices exist. Neither value is a secret.
		log.Debug().Str("tenant", tenant).Str("presented", req.ClientInformation.MQTT).
			Str("required", required).
			Msg("Auth-callout refused a device connect whose MQTT client id is not the device's own.")
		return "", genericAuthFailure
	}

	signed, err := natsauth.SignDeviceUserJWT(c.issuerSeed, req.UserNkey, c.instanceId, tenant, device.Token, c.now(), c.ttl)
	if err != nil {
		log.Error().Err(err).Msg("Auth-callout failed to sign a device user JWT.")
		return "", genericAuthFailure
	}
	return signed, ""
}

// checkPassword authenticates an MQTT_BASIC connect through the credential Checker, so
// its compare sits behind DeviceCredentialPolicy's backoff on the presented username.
//
// The principal is "{tenant}:{credentialId}" EXACTLY as presented. Both halves are
// matched byte for byte by the lookup (the tenant-scope predicate and credential_id =
// ?), so a spelling that differs, in case or otherwise, is a different principal AND
// names no credential the lookup can find: it cannot reach this username's credential
// under a fresh backoff.
//
// Every refusal is charged and compared, a real credential or not: an unknown, expired
// or misconfigured credential returns "" to the Checker, which compares its dummy and
// counts the attempt exactly as it does a wrong password, so neither the answer nor its
// timing tells a caller which usernames exist. What differs is only the returned
// error, which the caller logs and never sends.
//
// Two connects for one username that race (a reconnect overlapping a stale session)
// are both evaluated: the one whose charge loses the compare-and-set re-reads the
// record and charges on top, and with ten free attempts neither is delayed. Only a
// charge that loses three races in a row is refused as throttled (for a second), which
// takes at least three more concurrent connects for the same username.
func (c *CalloutResponder) checkPassword(ctx context.Context, tenant string, presented *model.PresentedCredential) (*model.Device, error) {
	var device *model.Device
	// reason is what the lookup found when it found no usable credential: the precise
	// refusal, or ErrCredentialMisconfigured. It never reaches the device.
	var reason error
	p := credential.Principal{Kind: credential.KindDeviceCredential, ID: tenant + ":" + presented.CredentialId}
	err := c.creds.Check(ctx, p, *presented.Secret, func(ctx context.Context) (string, error) {
		d, stored, err := c.api.ResolveDeviceCredential(ctx, presented, c.now())
		switch {
		case err == nil:
			device = d
			return stored, nil
		// Misconfigured is the operator's to fix (logged at Warn), but it is still a
		// refusal ON THE WIRE, so it is charged and compared like one: answering it
		// differently would make the callout an oracle for which stored credentials
		// are broken.
		case model.IsCredentialRefusal(err), errors.Is(err, model.ErrCredentialMisconfigured):
			reason = err
			return "", nil
		default:
			// The database failing: returned to the Checker unchanged, and still
			// charged (a failed lookup is not a free attempt).
			return "", err
		}
	})
	switch {
	case err == nil:
		return device, nil
	case reason != nil:
		// The lookup already said why; the Checker's mismatch adds nothing.
		return nil, reason
	case errors.Is(err, credential.ErrMismatch):
		return nil, model.ErrCredentialSecretMismatch
	default:
		return nil, err
	}
}

// logAuthFailure logs why a device connect was refused. A device's own wrong answer —
// model.IsCredentialRefusal, or a connect held back by the password backoff — is
// routine and stays at Debug. An attempt store that cannot be reached refuses every
// password connect, so it is a Warn, rate-limited. Anything else — the credential store
// failing, or a stored credential that can never authenticate — is invisible at Debug
// and is the operator's to fix, so it is a Warn. Either way the device learns nothing
// but the generic refusal.
func (c *CalloutResponder) logAuthFailure(tenant string, err error) {
	var throttled *credential.ThrottledError
	switch {
	case model.IsCredentialRefusal(err), errors.As(err, &throttled):
		log.Debug().Err(err).Str("tenant", tenant).Msg("Auth-callout rejected a device connection.")
	case errors.Is(err, credential.ErrUnavailable):
		if ok, held := c.unavailableLog.allow(c.now()); ok {
			log.Warn().Err(err).Int("suppressed", held).
				Msg("Auth-callout is refusing every MQTT password connect: the device credential " +
					"attempt store in JetStream cannot be reached, and a connect that cannot be " +
					"counted is not checked.")
		}
	default:
		log.Warn().Err(err).Str("tenant", tenant).
			Msg("Auth-callout could not authenticate a device connection: the credential store failed or holds a malformed credential.")
	}
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
	// into the device's `{instanceId}.{tenant}.>` permission subject. The grammar is already
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
