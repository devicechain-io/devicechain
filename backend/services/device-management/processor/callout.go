// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
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
	// xkey opens the broker-sealed requests and seals the replies; nil on a broker that does
	// not encrypt its callout. See WithRequestEncryption.
	xkey       *natsauth.XKeyCodec
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
	// tenantFenced reads this area's own erasure fence for a tenant (rdb.CheckTenantFence):
	// nil when none stands, an error otherwise — including when it cannot be read. Unlike
	// tenantDeleted it is local, uncached and never fails open, so it is what refuses a
	// mint once the purge has reached this schema even while the lifecycle gate still
	// reads "active". Never nil: NewCalloutResponder refuses to build a responder without
	// one.
	tenantFenced func(ctx context.Context, tenant string) error
	// creds compares every MQTT password behind DeviceCredentialPolicy's backoff. Never
	// nil: NewCalloutResponder refuses to build a responder without one.
	creds *credential.Checker
	// unavailableLog rate-limits the warning for an attempt store that cannot be
	// reached. While it is down EVERY password connect is refused, so a line per
	// connect would be one per device in a reconnecting fleet.
	unavailableLog rateLimitedLog
	// plaintextLog rate-limits the warning for a request that arrived unencrypted while an
	// xkey is configured.
	plaintextLog rateLimitedLog
	// misconfiguredLog rate-limits the warning for a stored credential that can never
	// authenticate. A restore next to the wrong root key makes that EVERY MQTT_BASIC
	// device, each reconnecting on its own backoff; the counter sees every one.
	misconfiguredLog rateLimitedLog

	// maxInFlight bounds how many requests are being authorized at once. A request over
	// the bound is answered with the generic denial at once (see dispatch) instead of
	// queueing on the credential store's connection pool until the broker has already
	// given up on it. sem holds one token per request in flight.
	maxInFlight int
	sem         chan struct{}
	// lookupTimeout bounds one request's credential work (database lookups and the
	// attempt store), below the broker's authorization timeout: past that the broker has
	// already refused the connect, so finishing the work only loads the store.
	lookupTimeout time.Duration
	// stopWait bounds how long Stop waits for handlers still in flight.
	stopWait time.Duration

	// base is the parent of every request's context; Stop cancels it after the drain
	// wait so a handler still blocked on a dependency is released.
	base   context.Context
	cancel context.CancelFunc

	// mu guards stopped against wg.Add, so a dispatch cannot add to the group after Stop
	// has begun waiting on it.
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup

	inFlight    atomic.Int64
	refusedBusy atomic.Int64
	metrics     *CalloutMetrics

	// storeFull reports whether the broker's MQTT message store is above its high-water mark
	// (messaging.MqttStoreBackoff), in which case every device connect is refused at once; nil
	// when the fail-closed MQTT acknowledgement is off. See WithStoreBackoff.
	storeFull func() bool
	// refusedStoreFull counts the connects storeFull refused. May be nil.
	refusedStoreFull prometheus.Counter
}

// Defaults for the bounds above. The lookup timeout sits under the broker's 5 s
// authorization timeout (deploy/opentofu/modules/nats).
const (
	DefaultCalloutMaxInFlight   = 256
	DefaultCalloutLookupTimeout = 3 * time.Second
	DefaultCalloutStopWait      = 5 * time.Second
)

// CalloutMetrics are the callout's load metrics. Optional: a responder without them
// still enforces the bounds.
type CalloutMetrics struct {
	InFlight    prometheus.Gauge
	RefusedBusy prometheus.Counter
	// PlaintextRequests counts requests answered while an xkey is configured but the request
	// arrived unencrypted. It is expected to be nonzero only across the broker roll that turns
	// encryption on; a standing nonzero rate means the broker is not sealing its requests.
	PlaintextRequests prometheus.Counter
	// X509 counts certificate connects (username "{tenant}:@x509") by result, one increment per
	// connect; see the x509Result constants. Every result exists at zero from startup.
	X509 *prometheus.CounterVec
	// JitInFlight is the certificate registrations being decided right now. It stays zero until
	// registration on first connect exists.
	JitInFlight prometheus.Gauge
}

// NewCalloutMetrics registers the callout's metrics on the microservice.
func NewCalloutMetrics(ms *core.Microservice) *CalloutMetrics {
	return &CalloutMetrics{
		InFlight: ms.NewGauge("callout_in_flight",
			"Device auth-callout requests being authorized right now."),
		RefusedBusy: ms.NewCounter("callout_refused_busy_total",
			"Device auth-callout requests refused with the generic denial because the in-flight bound was reached."),
		PlaintextRequests: ms.NewCounter("callout_plaintext_requests_total",
			"Device auth-callout requests that arrived unencrypted although an xkey is configured (expected only across the broker roll that enables encryption)."),
		X509: newX509Counter(ms),
		JitInFlight: ms.NewGauge("callout_jit_in_flight",
			"Certificate registrations on first connect being decided right now."),
	}
}

// Configure overrides the bounds; a non-positive value keeps the current one. Call it
// before Start.
func (c *CalloutResponder) Configure(maxInFlight int, lookupTimeout, stopWait time.Duration, metrics *CalloutMetrics) {
	if maxInFlight > 0 {
		c.maxInFlight = maxInFlight
		c.sem = make(chan struct{}, maxInFlight)
	}
	if lookupTimeout > 0 {
		c.lookupTimeout = lookupTimeout
	}
	if stopWait > 0 {
		c.stopWait = stopWait
	}
	if metrics != nil {
		c.metrics = metrics
	}
}

// Limits reports the bounds in force and whether the load metrics are attached. A responder is
// built with the Default* bounds, so this is what a wiring test reads to prove what the service
// actually runs with.
func (c *CalloutResponder) Limits() (maxInFlight int, lookupTimeout, stopWait time.Duration, metrics bool) {
	return cap(c.sem), c.lookupTimeout, c.stopWait, c.metrics != nil
}

// WithRequestEncryption makes the responder open broker-sealed requests and seal its replies
// with the curve seed. It is the other half of the broker's auth_callout.xkey: the broker
// encrypts each authorization request (which carries the device's CONNECT password) to the
// public key of this seed, so only this responder can read it. Call it before Start.
//
// It refuses a seed that is not a curve seed, and a seed whose public key is not wantPublic when
// that is given: a Secret from another instance, or the issuer seed in the wrong key, would
// otherwise start cleanly and then fail to open a single request.
//
// 🔴 WHAT SEALING DOES AND DOES NOT ESTABLISH. The responder's public key is public, so anyone can
// seal a request to it: encryption authenticates nothing. The broker's request JWT is signed by
// the server's nkey, but that key is generated at server start (nats-server's NewServer) and
// cannot be seeded or read from configuration, so there is no known issuer to pin. What stands
// between a forged request and a grant is therefore the broker's publish-deny on the callout
// subject (TestNoClientInTheCalloutAccountCanPublishARequest). A password grant still needs the
// device's secret on top of that; a CERTIFICATE grant (username {tenant}:@x509) needs only a
// public certificate, so it is the first grant for which the deny is the whole defence. The one
// thing this adds for it: with an xkey configured, an @x509 request that arrives UNSEALED is
// refused (result "unsealed"), since the broker never sends one and only a publisher bypassing it
// would. Residual risk, plainly: a publisher that could publish on the callout subject AND seal to
// the public key could forge a certificate grant, and an instance with no xkey configured has only
// the deny.
//
// 🔴 PLAINTEXT IS STILL ANSWERED. A request without the server's xkey header is answered as
// before (and counted, and logged). That is what lets this responder be rolled out BEFORE the
// broker is told to encrypt, so the broker roll never meets a responder that cannot read it.
//
// It is not a downgrade path once the broker encrypts, and the reason is the BROKER, not this
// code: a broker configured with auth_callout.xkey never sends an unencrypted request, so the only
// source of one would be a client publishing on the callout subject, and nats-server denies that
// publish to every client authenticated into the callout account (the service logins included)
// and isolates the subject from every other account. Were that ever to change, answering a forged
// request would hand its sender a password oracle around the broker and an issuer-signed grant, so
// the deny is pinned by TestNoClientInTheCalloutAccountCanPublishARequest.
func (c *CalloutResponder) WithRequestEncryption(seed, wantPublic string) (*CalloutResponder, error) {
	codec, err := natsauth.NewXKeyCodec(seed)
	if err != nil {
		return nil, err
	}
	if wantPublic != "" {
		got, err := codec.Public()
		if err != nil {
			return nil, err
		}
		if got != wantPublic {
			return nil, errXKeyMismatch
		}
	}
	c.xkey = codec
	return c, nil
}

// errXKeyMismatch is WithRequestEncryption's refusal of a seed that is not the key the broker
// was configured with.
var errXKeyMismatch = errors.New("the callout xkey seed does not match the public key the broker " +
	"is configured to encrypt to; the Secret and the instance configuration came from different mints")

// openRequest returns the plaintext authorization request in msg, and the server curve key to
// seal the reply to ("" when the request was not encrypted). ok is false when the request cannot
// be read at all; the broker times that connect out.
func (c *CalloutResponder) openRequest(msg *nats.Msg) (data []byte, replyTo string, ok bool) {
	serverKey := msg.Header.Get(natsauth.AuthRequestXKeyHeader)
	if serverKey == "" {
		if c.xkey != nil {
			if c.metrics != nil && c.metrics.PlaintextRequests != nil {
				c.metrics.PlaintextRequests.Inc()
			}
			if allow, held := c.plaintextLog.allow(c.now()); allow {
				log.Warn().Int("alsoHeld", held).Msg("Auth-callout answered an UNENCRYPTED request although an " +
					"xkey is configured. Expected only while the broker rolls onto auth_callout.xkey.")
			}
		}
		return msg.Data, "", true
	}
	if c.xkey == nil {
		log.Warn().Msg("Dropping an encrypted auth-callout request: no xkey seed is configured here.")
		return nil, "", false
	}
	plain, err := c.xkey.Open(msg.Data, serverKey)
	if err != nil {
		log.Warn().Err(err).Msg("Dropping an auth-callout request that could not be decrypted.")
		return nil, "", false
	}
	return plain, serverKey, true
}

// WithStoreBackoff makes the responder refuse every device connect, at once and before any
// credential work, while storeFull reports the broker's MQTT message store above its
// high-water mark (messaging.MqttStoreBackoff.Engaged). refused counts those refusals and may
// be nil. Call it before Start.
//
// It is the back-off ahead of overflow. With the fail-closed MQTT acknowledgement on, a full
// store refuses a publish by closing the device's connection, and the device reconnects. While
// the store is nearly full, letting it back in only has it publish into the same store and be
// closed again, after a credential check that loads this service and its database during the
// very overload that caused it. Refusing the connect instead leaves the devices already
// connected to drain the store, and the refusal is lifted once it is read down.
//
// The answer is the generic denial every other refusal gets, so it says nothing about the
// device. The broker turns it into MQTT CONNACK code 5 (not authorized): its MQTT gateway has
// no path that sends code 3 (server unavailable) for an auth callout's refusal.
func (c *CalloutResponder) WithStoreBackoff(storeFull func() bool, refused prometheus.Counter) *CalloutResponder {
	c.storeFull = storeFull
	c.refusedStoreFull = refused
	return c
}

// InFlight is how many requests are being authorized right now.
func (c *CalloutResponder) InFlight() int64 { return c.inFlight.Load() }

// RefusedBusy is how many requests the in-flight bound has refused.
func (c *CalloutResponder) RefusedBusy() int64 { return c.refusedBusy.Load() }

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

// errNoTenantFence is NewCalloutResponder's refusal to build a responder that could mint a
// broker credential for a tenant whose data this area is erasing.
var errNoTenantFence = errors.New("the device auth callout needs the erasure-fence read: " +
	"without it a credential could be minted for a tenant being deleted, valid long after " +
	"its credential rows are gone")

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
//
// tenantFenced reads this area's erasure fence for a tenant (main wires
// rdb.CheckTenantFence over the service's own database). It is REQUIRED for the same
// reason: a nil one is refused rather than defaulting to "no fence stands".
func NewCalloutResponder(conn *nats.Conn, api model.DeviceManagementApi, creds *credential.Checker, issuerSeed, instanceId string, tenantDeleted func(string) bool, tenantFenced func(context.Context, string) error) (*CalloutResponder, error) {
	if creds == nil {
		return nil, errNoCredentialChecker
	}
	if tenantFenced == nil {
		return nil, errNoTenantFence
	}
	if tenantDeleted == nil {
		tenantDeleted = func(string) bool { return false }
	}
	base, cancel := context.WithCancel(context.Background())
	return &CalloutResponder{
		conn:          conn,
		api:           api,
		creds:         creds,
		issuerSeed:    issuerSeed,
		instanceId:    instanceId,
		ttl:           natsauth.DefaultUserJWTTTL,
		now:           time.Now,
		tenantDeleted: tenantDeleted,
		tenantFenced:  tenantFenced,
		maxInFlight:   DefaultCalloutMaxInFlight,
		sem:           make(chan struct{}, DefaultCalloutMaxInFlight),
		lookupTimeout: DefaultCalloutLookupTimeout,
		stopWait:      DefaultCalloutStopWait,
		base:          base,
		cancel:        cancel,
	}, nil
}

// Start subscribes to the auth-callout subject. Once subscribed, every non-exempt
// (i.e. device) connection is gated by handle. Each request is handled on its own
// goroutine so one slow check (for a password, attempt-store reads and writes on
// JetStream plus a DB round-trip; for an access token, the DB round-trip) does not
// stall the whole queue — nats.go dispatches a subscription's callbacks serially, and
// a connect storm within the broker's auth window otherwise backs up. Concurrency is
// bounded (maxInFlight): a request over the bound is denied at once rather than queued
// behind the credential store, and every request's work carries a deadline below the
// broker's authorization timeout (lookupTimeout).
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
	sub, err := messaging.QueueSubscribeSynced(c.conn, AuthCalloutSubject, authCalloutQueue, c.dispatch)
	if err != nil {
		return err
	}
	c.sub = sub
	log.Info().Msg("Device auth-callout responder subscribed; device connections are now broker-authenticated.")
	return nil
}

// dispatch admits a request to a handler goroutine, or denies it at once when
// maxInFlight requests are already being authorized.
func (c *CalloutResponder) dispatch(msg *nats.Msg) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return // the broker times the connect out; another replica's queue member answers
	}
	if c.storeFull != nil && c.storeFull() {
		c.mu.Unlock()
		if c.refusedStoreFull != nil {
			c.refusedStoreFull.Inc()
		}
		c.refuseAtOnce(msg)
		return
	}
	select {
	case c.sem <- struct{}{}:
		c.wg.Add(1)
		c.mu.Unlock()
	default:
		c.mu.Unlock()
		c.refuseBusy(msg)
		return
	}
	c.inFlight.Add(1)
	if c.metrics != nil {
		c.metrics.InFlight.Inc()
	}
	go func() {
		defer func() {
			c.inFlight.Add(-1)
			if c.metrics != nil {
				c.metrics.InFlight.Dec()
			}
			<-c.sem
			c.wg.Done()
		}()
		c.handle(msg)
	}()
}

// refuseBusy answers a request the bound turned away with the generic denial, the same
// answer every other refusal gets, so the bound is not an oracle for anything.
func (c *CalloutResponder) refuseBusy(msg *nats.Msg) {
	c.refusedBusy.Add(1)
	if c.metrics != nil {
		c.metrics.RefusedBusy.Inc()
	}
	c.refuseAtOnce(msg)
}

// refuseAtOnce answers a request with the generic denial without authorizing it.
func (c *CalloutResponder) refuseAtOnce(msg *nats.Msg) {
	data, sealTo, ok := c.openRequest(msg)
	if !ok {
		return
	}
	reqClaims, err := jwt.DecodeAuthorizationRequestClaims(string(data))
	if err != nil || reqClaims.AuthorizationRequest.UserNkey == "" {
		return
	}
	req := reqClaims.AuthorizationRequest
	c.respond(msg, sealTo, req.Server.ID, req.UserNkey, "", genericAuthFailure)
}

// Stop tears down the subscription, then waits up to stopWait for requests still being
// authorized; whatever outlasts that is cancelled through its context.
func (c *CalloutResponder) Stop() error {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	var err error
	if c.sub != nil {
		err = c.sub.Unsubscribe()
	}
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(c.stopWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Warn().Int64("inFlight", c.inFlight.Load()).
			Msg("Auth-callout stop gave up waiting for in-flight requests; cancelling them.")
	}
	c.cancel()
	return err
}

// handle processes one authorization request: decode, decide, and reply with a
// scoped user JWT or a denial.
func (c *CalloutResponder) handle(msg *nats.Msg) {
	data, sealTo, ok := c.openRequest(msg)
	if !ok {
		return
	}
	reqClaims, err := jwt.DecodeAuthorizationRequestClaims(string(data))
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
	// A request is TRUSTED, for the grants that need no secret, only if it came the way the
	// broker sends one: sealed to this responder's key when a key is configured. See
	// WithRequestEncryption for what that does and does not establish.
	userJWT, errMsg := c.authorizeRequest(req, c.xkey == nil || sealTo != "")
	c.respond(msg, sealTo, req.Server.ID, req.UserNkey, userJWT, errMsg)
}

// authorize resolves a decoded request to either a signed user JWT (grant) or a
// non-empty error message (deny). It touches the credential store and signs a
// JWT but does no NATS I/O, so the grant/deny decision is unit-testable in
// isolation. Every failure returns the same generic message (see
// genericAuthFailure).
func (c *CalloutResponder) authorize(req jwt.AuthorizationRequest) (userJWT string, errMsg string) {
	return c.authorizeRequest(req, true)
}

// authorizeRequest is authorize for a request whose arrival channel is known. trusted is false
// for a request that arrived unsealed while an xkey is configured, which the broker never sends;
// a certificate connect (which needs no secret from the device) is refused for one.
func (c *CalloutResponder) authorizeRequest(req jwt.AuthorizationRequest, trusted bool) (userJWT string, errMsg string) {
	tenant, presented, ok := parseDeviceCredential(req.ConnectOptions.Username, req.ConnectOptions.Password)
	if !ok {
		return "", genericAuthFailure
	}
	// A certificate connect is its own path (callout_x509.go): the credential is the TLS client
	// certificate, never anything in the username or password.
	if presented.CredentialType == string(model.CredentialX509Certificate) {
		return c.authorizeX509(req, tenant, presented, trusted)
	}
	// A certificate offered with a token or password username is ignored: the connect
	// authenticates on that credential and nothing else.
	if req.TLS != nil && len(req.TLS.Certs) > 0 {
		c.countX509(x509ResultCertIgnored)
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
	// tenants exist and what is happening to them. It is the ANSWER that is the same, not
	// the time: this refusal skips the attempt-store charge and the lookup, so it is
	// quicker than any refusal of a well-formed username (a malformed one is refused
	// before this gate, with less work still), and a caller who times it can tell a
	// tenant being deleted from one that is not. That is accepted: equalizing it would put the deleted
	// tenant's reconnect storm back onto the attempt store, which refusing here prevents.
	if c.tenantDeleted(tenant) {
		log.Debug().Str("tenant", tenant).Msg("Auth-callout refused a device connect for a deleted tenant.")
		return "", genericAuthFailure
	}

	// The credential lookup is tenant-scoped via the context tenant (the fail-closed
	// DB callback), so a credential is only ever resolved within its own tenant.
	ctx, cancel := context.WithTimeout(core.WithTenant(c.base, tenant), c.lookupTimeout)
	defer cancel()
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
		//
		// It goes through AuthenticateDeviceConnect, never AuthenticateDevice, which the
		// service's cached Api answers from memory: a connect granted on a copy a
		// revocation had not yet reached would hold its session for hours.
		device, err = c.api.AuthenticateDeviceConnect(ctx, presented, c.now())
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

	userJWT, why := c.grantSession(ctx, req, tenant, device)
	if why != "" {
		return "", genericAuthFailure
	}
	return userJWT, ""
}

// grantRefusal names which of grantSession's checks refused a connect, "" when none did. It is
// for the caller's counters and logs only; it never reaches the device.
type grantRefusal string

const (
	grantGranted        grantRefusal = ""
	grantRefusedDevice  grantRefusal = "device_disabled"
	grantRefusedClient  grantRefusal = "client_id_mismatch"
	grantRefusedFence   grantRefusal = "tenant_fenced"
	grantRefusedExpiry  grantRefusal = "expires_too_soon"
	grantRefusedFailure grantRefusal = "error"
)

// grantSession is the half of a device connect that is the same whatever the credential was:
// everything after the credential has named the device. The device must be enabled, the MQTT
// client id must be its own, the tenant's erasure fence must not stand, and the session is
// clamped to the credential's expiry before it is signed. The order matters and is part of the
// contract (see the notes on each check).
func (c *CalloutResponder) grantSession(ctx context.Context, req jwt.AuthorizationRequest, tenant string, device *model.Device) (string, grantRefusal) {
	// A disabled device is refused a session, with the same generic answer as every other
	// refusal. Checked after the credential succeeded, like the client-id pin below: it is a
	// property of the DEVICE, which only a successful credential check names, and it is not
	// a credential failure, so it is not charged to the password backoff. Both connect
	// lookups read the device's disabled-since column for this (connectDeviceFields,
	// authDeviceFields). A session already open when the device was disabled is not ended
	// here; its events are refused at resolution and its commands at enqueue.
	if device.IsDisabled() {
		log.Debug().Str("tenant", tenant).Str("device", device.Token).
			Msg("Auth-callout refused a device connect: the device is disabled.")
		return "", grantRefusedDevice
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
		return "", grantRefusedFailure
	}
	if !messaging.DeviceClientIDMatches(req.ClientInformation.MQTT, required) {
		// Logged with both values because "my device will not connect" is otherwise
		// undiagnosable from the outside — the wire response is the same generic refusal
		// every other failure returns, deliberately, so the callout does not become an
		// oracle for which tenants and devices exist. Neither value is a secret.
		log.Debug().Str("tenant", tenant).Str("presented", req.ClientInformation.MQTT).
			Str("required", required).
			Msg("Auth-callout refused a device connect whose MQTT client id is not the device's own.")
		return "", grantRefusedClient
	}

	// The erasure fence, read from this area's own schema immediately before minting. The
	// lifecycle gate above is a cached remote read that fails open, so after a delete it
	// can keep reading "active" for up to its TTL — or indefinitely while user-management
	// is unreachable — and until the purge sweeps this tenant's credential rows the lookup
	// above still succeeds. A credential minted then is valid for its whole lifetime, long
	// past the sweep. The fence is planted, in its own committed transaction, before that
	// sweep runs, so from that moment this refuses the mint without depending on anything
	// remote. Scoped by construction: a fence exists only for a tenant being deleted, so
	// this can never refuse a live tenant's connects, and a fence that cannot be read is a
	// local database that also could not have answered the lookup above.
	//
	// Checked after the credential succeeded rather than before it, so a live tenant's
	// failed connect costs nothing extra and the read is one per granted connect.
	if err := c.tenantFenced(ctx, tenant); err != nil {
		log.Debug().Err(err).Str("tenant", tenant).
			Msg("Auth-callout refused a device connect: this tenant's data is being erased, or the erasure fence could not be read.")
		return "", grantRefusedFence
	}

	// A session must not outlive the credential that opened it. The broker force-closes a
	// connection at its JWT's expiry, so clamping the JWT to the credential's own expiry makes
	// "this credential expires at T" true of a session already open, with no scheduler and no
	// kick. A credential with no expiry gets the full lifetime; revocation before expiry is the
	// kick's job (see model.SessionKicker).
	ttl, ok := clampSessionTTL(c.ttl, device.ConnectCredentialExpiresAt, c.now())
	if !ok {
		log.Debug().Str("tenant", tenant).Str("device", device.Token).
			Msg("Auth-callout refused a device connect: the credential expires too soon to mint a session.")
		return "", grantRefusedExpiry
	}
	signed, err := natsauth.SignDeviceUserJWT(c.issuerSeed, req.UserNkey, c.instanceId, tenant, device.Token, c.now(), ttl)
	if err != nil {
		log.Error().Err(err).Msg("Auth-callout failed to sign a device user JWT.")
		return "", grantRefusedFailure
	}
	return signed, grantGranted
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
// counts the attempt exactly as it does a wrong password, so the ANSWER never tells a
// caller which usernames exist: what differs is only the returned error, which the
// caller logs and never sends.
//
// 🔴 The TIMING is closer than the answer, not equal to it, and this does not claim
// otherwise. Both cost the same attempt-store charge, the byte-identical lookup statement
// (the owning device comes back on a JOIN, so there is no second one for a hit) and one
// compare. What an existing username adds is the one row that statement returns, and that
// row is a fixed set of columns (deviceCredentialForConnect): its cost does not grow with
// the device's metadata, name or description. Making a miss return a row as well would
// take hand-written SQL, which the tenant-scope callback does not see, so the tenant
// predicate would be written a second time by hand on this path; that trade is not made.
// The backoff bounds how many samples a caller gets per username; it does not equalize
// them.
//
// An access-token connect resolves through AuthenticateDeviceConnect instead, which reads every
// column of both rows. There the credential id IS the secret, so a row found is normally
// the grant itself and existence is the answer rather than a side channel; only a token
// that exists but is refused pays for that full row without connecting: one that is
// expired, presented from the wrong client id, held by a soft-deleted device, or whose
// device_id names a device in another tenant.
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
	p := credential.Principal{Kind: credential.KindDeviceCredential, ID: tenant + ":" + presented.CredentialId, Tenant: tenant}
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
	case errors.Is(err, model.ErrCredentialMisconfigured):
		if ok, held := c.misconfiguredLog.allow(c.now()); ok {
			log.Warn().Err(err).Str("tenant", tenant).Int("suppressed", held).
				Msg("Auth-callout could not authenticate a device connection: its stored credential " +
					"can never authenticate (no secret, or a digest made under a different root key). " +
					"Logged at most once a minute; credential_misconfigured_total counts every one.")
		}
	default:
		log.Warn().Err(err).Str("tenant", tenant).
			Msg("Auth-callout could not authenticate a device connection: the credential store failed or holds a malformed credential.")
	}
}

// respond signs and publishes the authorization response. Exactly one of userJWT
// / errMsg is non-empty.
func (c *CalloutResponder) respond(msg *nats.Msg, sealTo, serverID, userNkey, userJWT, errMsg string) {
	resp, err := natsauth.EncodeAuthResponse(c.issuerSeed, serverID, userNkey, userJWT, errMsg)
	if err != nil {
		log.Error().Err(err).Msg("Auth-callout failed to encode a response.")
		return
	}
	out := []byte(resp)
	// An encrypted request is answered encrypted. The broker would also accept a plaintext reply,
	// but the reply names the grant a device was given, which is no one else's business.
	if sealTo != "" {
		if out, err = c.xkey.Seal(out, sealTo); err != nil {
			log.Error().Err(err).Msg("Auth-callout failed to encrypt a response.")
			return
		}
	}
	if err := c.conn.Publish(msg.Reply, out); err != nil {
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
	// "@x509" selects the certificate path. '@' is outside core.ValidateToken, so the name can
	// never collide with a credential id an operator chose. The credential type alone routes it,
	// and the presented value carries no credential id to look up: a certificate connect that
	// somehow reached an id-based lookup would be refused there (the type is not presented by
	// id), not authenticated on the literal "@x509". A password is carried so the certificate
	// path can refuse it.
	if credentialID == X509Username {
		p := &model.PresentedCredential{CredentialType: string(model.CredentialX509Certificate)}
		if password != "" {
			secret := password
			p.Secret = &secret
		}
		return tenant, p, true
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

// minSessionTTL is the shortest session the callout will mint. The JWT's expiry is whole
// seconds, so anything under a second would be minted already expired.
const minSessionTTL = time.Second

// clampSessionTTL is the lifetime of a device session: the configured ttl, shortened to the
// time left on the credential that authenticated it. ok is false when the credential has
// less than minSessionTTL left, which is refused like any other expired credential rather
// than minted as a session the broker would close at once.
//
// Pure, so the clamp is testable without a broker. A credential with no expiry returns ttl
// unchanged.
func clampSessionTTL(ttl time.Duration, expiresAt sql.NullTime, now time.Time) (time.Duration, bool) {
	if !expiresAt.Valid {
		return ttl, true
	}
	remaining := expiresAt.Time.Sub(now)
	if remaining < minSessionTTL {
		return 0, false
	}
	return min(ttl, remaining), true
}
