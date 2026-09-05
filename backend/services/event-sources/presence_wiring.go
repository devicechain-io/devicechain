// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-event-sources/presence"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/svcclient"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// brokerPresence holds everything the $SYS tap owns, so shutdown can undo exactly what
// startup did. nil when the tap is off.
var brokerPresence *presenceRuntime

type presenceRuntime struct {
	conn    *nats.Conn
	stops   []func()
	cancel  context.CancelFunc
	stopped chan struct{}
}

// startBrokerPresence turns plain-MQTT presence from a guess into the broker's own
// answer (ADR-067).
//
// 🔑 IT IS OFF UNLESS THREE THINGS ARE TRUE, and each absence is a legitimate
// deployment rather than a misconfiguration to shout about:
//
//   - a SYS credential is configured. An instance whose broker predates the
//     system-account login has none, and connecting anyway would fail authorization on
//     every attempt — a reconnect storm in place of a feature. Absent ⇒ off ⇒ MQTT
//     presence stays inferred, exactly as in the release before this one.
//   - a GATEWAY source exists. The tap describes devices connected to the platform's
//     own broker, which are precisely the devices the capture-stream source ingests. An
//     instance pointed only at a foreign broker has devices we hold no advisories for.
//   - user-management is reachable for service calls. Reconciliation must enumerate
//     tenants, and without it the tap could assert devices online but never repair a
//     death the broker never announced — the one failure this design refuses to ship.
//
// It never returns an error. Presence is an enrichment of ingest, not a precondition
// for it: a broker that will not serve the system account must not stop this service
// from accepting telemetry.
func startBrokerPresence(ctx context.Context) {
	infra := Microservice.InstanceConfiguration.Infrastructure
	cfg := Configuration.BrokerPresence

	// The tap emits under the GATEWAY source's id rather than a name of its own, and
	// the reason is not cosmetic: device-state records the source of the last event
	// that touched a device (the Source merge in Api.MergeDeviceState, in
	// device-state/model/api.go), so presence under a
	// different name would make a device's row alternate between two sources on every
	// event — and the reconciler's source-scoped read would then see the device only
	// half the time, repairing it intermittently.
	source, ok := GatewaySourceId, GatewaySourceId != ""
	switch {
	case !cfg.IsEnabled():
		log.Info().Msg("Broker-asserted MQTT presence is disabled by configuration; MQTT presence stays inferred.")
		tapOff(presence.TapOffDisabled)
		return
	case infra.Nats.Auth.SysUser == "" || infra.Nats.Auth.SysPassword == "":
		log.Info().Msg("No NATS system-account credential is configured, so broker-asserted MQTT presence is " +
			"off and MQTT presence stays inferred. Re-run the bring-up to mint one.")
		tapOff(presence.TapOffNoSystemCredential)
		return
	case !ok:
		log.Info().Msg("No event source is pointed at the platform broker, so there are no broker " +
			"connection advisories to read; MQTT presence stays inferred.")
		tapOff(presence.TapOffNoGatewaySource)
		return
	// The PORT is part of "configured", not a detail. Every service call this tap makes
	// builds its URL as host:port, and the instance config's own Validate already refuses
	// a user-management coordinate missing either half — so this branch is unreachable on
	// a validated instance and is written to stay total anyway, because a predicate that
	// reads half a coordinate is one edit away from being the live check.
	case infra.ServiceAuth.Secret == "" || infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0:
		log.Warn().Msg("Broker-asserted MQTT presence needs service-to-service calls to enumerate tenants " +
			"and read presence state, which are not configured. It stays OFF rather than running without " +
			"its repair path: a device whose disconnect the broker never announced would otherwise read " +
			"as connected forever.")
		tapOff(presence.TapOffNoServiceAuth)
		return
	}

	conn, err := dialSystemAccount(ctx, infra.Nats)
	if err != nil {
		log.Error().Err(err).Msg("Could not reach the NATS system account within the startup window; " +
			"broker-asserted MQTT presence is OFF for this pod's run and MQTT presence stays inferred. " +
			"An unreachable broker is the instance's condition rather than this replica's — the MQTT " +
			"gateway is in that same broker — so the devices this source asserted are released back to " +
			"inferred after a settle window, unless the next log line says the release could not start.")
		tapOff(presence.TapOffBrokerUnreachable)
		return
	}

	tap := presence.NewTap(Microservice.InstanceId, source, presenceEmitter(), presence.Gate(ingestGate), presenceMetrics())
	stopTap, err := tap.Subscribe(conn)
	if err != nil {
		conn.Close()
		log.Error().Err(err).Msg("Could not subscribe to the broker's connection advisories; " +
			"broker-asserted MQTT presence is OFF.")
		tapOff(presence.TapOffSubscribeFailed)
		return
	}
	// The tap is running: clear every off-reason, not just the one that might be standing.
	// See presence.AllTapOffReasons.
	for _, reason := range presence.AllTapOffReasons() {
		PresenceTapOffGauge.WithLabelValues(string(reason)).Set(0)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	rt := &presenceRuntime{conn: conn, stops: []func(){stopTap}, cancel: cancel, stopped: make(chan struct{})}

	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-sources",
		[]string{string(auth.TenantRead), string(auth.StateRead)})
	umURL := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	dsURL := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceState.Hostname, infra.DeviceState.Port)
	reconciler := presence.NewReconciler(tap, presence.NewRequester(conn),
		presence.NewGraphQLTenantLister(client, umURL, Microservice.InstanceId),
		presence.NewGraphQLProjectionReader(client, dsURL),
		reconcileMetrics(), cfg.InventoryGatherWindow(), time.Now,
		presence.PassTimeoutFor(cfg.ReconcileInterval()))

	// The canary dials the MQTT gateway, which terminates TLS against the SAME private
	// per-instance CA the system-account connection above verifies. Handing it the same
	// config is not tidiness: without it a probe to an ssl:// URL verifies against the
	// system roots and fails on every healthy instance, turning the one alarmable metric
	// into a standing false alarm.
	canaryTLS, err := infra.Nats.TLSConfig(infra.Nats.Hostname)
	if err != nil {
		log.Error().Err(err).Msg("Could not build TLS for the broker-presence canary; it is disabled. " +
			"Presence still runs, but a silently dead subscription is now indistinguishable from an idle fleet.")
	}
	var canary *presence.Canary
	if err == nil {
		canary = presence.NewCanary(Microservice.InstanceId, replicaName(), mqttGatewayURL(infra.Nats),
			infra.Nats.Auth.User, infra.Nats.Auth.Password, canaryTLS, canaryMetrics(), cfg.CanaryDeadline())
		if stopCanary, werr := canary.Watch(conn); werr != nil {
			// 🔑 THE PROBE IS DROPPED WITH THE SUBSCRIPTION, not left running. A probe
			// with nothing observing it misses by construction, so keeping it would
			// alarm continuously about a tap that is fine — replacing "we cannot tell
			// whether presence is being read" with a louder wrong answer.
			log.Error().Err(werr).Msg("The broker-presence canary could not subscribe, so it is disabled. " +
				"Presence still runs, but a silently dead subscription is now indistinguishable from " +
				"an idle fleet.")
			canary = nil
		} else {
			rt.stops = append(rt.stops, stopCanary)
		}
	}

	if stopWaker := attachCommandWake(tap, infra); stopWaker != nil {
		rt.stops = append(rt.stops, stopWaker)
	}

	// 🔴 A TYPED NIL IS NOT A NIL INTERFACE. canary is a *presence.Canary that is
	// deliberately left nil when there is no observer to see a probe; assigning it
	// straight into a prober would produce a NON-nil interface holding a nil pointer, so
	// the loop's nil check would pass and every probe would panic on a fleet that had
	// merely declined to run a canary.
	var probe prober
	if canary != nil {
		probe = canary
	}
	go runPresenceLoops(runCtx, rt, reconciler, probe, cfg.ReconcileInterval(), cfg.CanaryInterval())
	brokerPresence = rt
}

// tapOff records WHY broker-asserted presence is not running and, on the two paths where
// it can be sure, releases the devices this source has left asserted.
//
// 🔴 THE GAUGE IS SET ON EVERY PATH, INCLUDING THE ONES THAT CANNOT RELEASE ANYTHING, and
// that is most of what this function is worth. A tap that never started is invisible from
// outside: a long-lived MQTT fleet legitimately emits no advisories for days, so the
// presence counters read the same whether presence is being asserted or silently is not.
// The canary covers a tap that started and then stopped working. Nothing covered this.
func tapOff(reason presence.TapOffReason) {
	PresenceTapOffGauge.WithLabelValues(string(reason)).Set(1)
	startPresenceDemotion(reason)
}

// startPresenceDemotion drains this source's asserted rows back to inferred, when the
// reason the tap is off is one that can be trusted to mean it.
//
// 🔑 IT ACTS ON THREE OF THE SIX BAIL PATHS, and the line is what the evidence is made of.
// A written `enabled: false` and a missing system-account credential are CONFIGURATION:
// every replica of the instance reads the same values and reaches the same conclusion, so
// a demotion is the instance speaking, not one replica guessing.
//
// 🔴 AN UNREACHABLE BROKER JOINED THEM, AND THAT REVERSES WHAT THIS COMMENT USED TO SAY.
// It said a failed dial was this replica's own bad luck. That was written when a "failed
// dial" meant one nats.Connect returning an error — but the dial retries, so it now means
// this pod could not reach or authenticate to the system account for thirty CONTINUOUS
// seconds. The two ways that happens are a broker that is down or rolling and a
// system-account credential the broker refuses, and both are instance-wide. The first is
// also the case where demotion is simply CORRECT: the MQTT gateway lives in that same
// broker, so if it is unreachable no device is connected through it, and a graceful broker
// shutdown announces no deaths at all.
//
// The residual replica-local case — this pod's network is broken while its peers are fine
// — is self-limiting rather than argued away: the drain emits over the DATA-PLANE
// connection, so a pod that cannot reach the broker cannot write the demotions either. Its
// rows stay asserted and the next pass retries them.
//
// A failed subscription on a CONNECTED conn stays replica-local. It gets the gauge and
// nothing more; the gauge is what makes it visible, which is the actual gap it had.
//
// 🔴 THE PRECONDITIONS ARE RE-CHECKED HERE RATHER THAN INFERRED FROM THE BRANCH, because
// the switch above is ORDERED: `!cfg.IsEnabled()` returns before GatewaySourceId or
// ServiceAuth are ever read, so arriving on that path says nothing whatever about them. A
// drain needs a source name to emit under and service-to-service calls to enumerate
// tenants and read the projection. Missing any of those, it says so and points at the
// manual door rather than starting a loop that can only fail.
func startPresenceDemotion(reason presence.TapOffReason) {
	if !reasonIsInstanceWide(reason) {
		return
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	if !drainEndpointsReady(GatewaySourceId, infra.ServiceAuth.Secret, infra.UserManagement.Hostname,
		infra.UserManagement.Port, infra.DeviceState.Hostname, infra.DeviceState.Port) {
		log.Warn().Str("reason", string(reason)).Msg("Broker-asserted MQTT presence is off, so the devices " +
			"this instance already asserted are frozen at their last known state — but releasing them " +
			"automatically needs a gateway source and service-to-service calls, which are not configured. " +
			"Release them with `dcctl presence demote` instead.")
		return
	}

	runCtx, cancel := context.WithCancel(context.Background())
	rt := &presenceRuntime{cancel: cancel, stopped: make(chan struct{})}

	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-sources",
		[]string{string(auth.TenantRead), string(auth.StateRead)})
	umURL := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	dsURL := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceState.Hostname, infra.DeviceState.Port)
	reader := presence.NewGraphQLProjectionReader(client, dsURL)

	demoter := presence.NewDemoter(GatewaySourceId,
		presence.NewPublisher(GatewaySourceId, presenceEmitter(), presence.Gate(ingestGate), presenceMetrics()),
		presence.NewGraphQLTenantLister(client, umURL, Microservice.InstanceId),
		reader, presence.NewRateWaiter(),
		presence.DemoterMetrics{Released: PresenceReleasedCounter, Remaining: PresenceStillAssertedGauge})

	interval := Configuration.BrokerPresence.ReconcileInterval()
	delay := presence.StartDelayFor(reason, demotionStartJitter())
	go func() {
		// Closes rt.stopped so stopBrokerPresence's five-second wait does not fire on every
		// disabled instance — the runtime is shaped the same whether the tap ran or not.
		defer close(rt.stopped)
		presence.RunDemoteLoop(runCtx, demoter, interval, delay, time.Now)
	}()
	brokerPresence = rt
}

// reasonIsInstanceWide reports whether a bail reason is evidence about the INSTANCE rather
// than about this replica. Only instance-wide evidence justifies emitting durable events
// for a whole fleet; see startPresenceDemotion.
func reasonIsInstanceWide(reason presence.TapOffReason) bool {
	return reason == presence.TapOffDisabled ||
		reason == presence.TapOffNoSystemCredential ||
		reason == presence.TapOffBrokerUnreachable
}

// drainEndpointsReady reports whether the drain has what it needs: a source name to emit
// under, a service credential, and both service endpoints it reads through.
//
// 🔴 IT IS A SEPARATE, TOTAL PREDICATE BECAUSE THE BAIL SWITCH IS ORDERED. `enabled:
// false` returns before GatewaySourceId or ServiceAuth are ever looked at, so the branch a
// caller arrived on carries NO information about these values. Deriving them from the
// branch would work for one path and be silently wrong for the other — which is the whole
// reason this is checked rather than assumed.
func drainEndpointsReady(source, serviceSecret, umHost string, umPort uint32, dsHost string, dsPort uint32) bool {
	// Both endpoints are checked as a COORDINATE. user-management's port was missing here
	// while device-state's was present, which is the asymmetry that makes a total predicate
	// worth writing down: a zero port renders "http://dc-user-management:0/graphql", which
	// fails every call rather than being absent, so the drain would start and never list a
	// tenant.
	return source != "" && serviceSecret != "" &&
		umHost != "" && umPort != 0 &&
		dsHost != "" && dsPort != 0
}

// demotionStartJitter spreads the first drain pass across replicas that all restarted
// together, which is every replica of an instance whose configuration just changed. It is
// pacing, not security, so the default source is right.
func demotionStartJitter() time.Duration {
	return time.Duration(rand.Int63n(int64(30 * time.Second)))
}

// attachCommandWake gives the tap a way to tell command-delivery that a device is back,
// returning the waker's stop function, or nil when there is nothing to attach.
//
// 🔑 FAIL-OPEN, AND WHAT IS LOST IS LATENCY RATHER THAN CORRECTNESS. Without the wake a
// returning device's withheld commands are still released — by command-delivery's own
// reconcile pass, which walks the withheld set on a timer. So an instance with no
// command-delivery coordinate configured, or none deployed at all, keeps working with a
// slower release. That is why this logs at INFO and not WARN: it describes a supported
// shape, and a warning here would be noise on every profile that omits command delivery.
//
// It needs its own svcclient because the presence client above holds tenant:read and
// state:read; command:wake is a separate, system-tier authority granted to the transports
// that own device connections. Reusing that client would mean widening it.
func attachCommandWake(tap *presence.Tap, infra config.InfrastructureConfiguration) func() {
	if infra.ServiceAuth.Secret == "" || infra.CommandDelivery.Hostname == "" || infra.CommandDelivery.Port == 0 {
		log.Info().Msg("command-delivery is not configured here, so a returning device's withheld " +
			"commands are released by command-delivery's own periodic pass rather than the instant " +
			"the device reconnects.")
		return nil
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-sources",
		[]string{string(auth.CommandWake)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.CommandDelivery.Hostname, infra.CommandDelivery.Port)
	waker, stop := presence.NewWaker(presence.NewGraphQLReleaser(client, url), wakerMetrics())
	tap.WithWaker(waker)
	log.Info().Str("commandDelivery", url).
		Msg("A device's withheld commands will be released as soon as the broker reports it back.")
	return stop
}

// runPresenceLoops drives the repair pass and the canary.
//
// 🔴🔴 THEY ARE TWO GOROUTINES, AND THAT SEPARATION IS THE POINT. They used to be two
// arms of one select, which made the canary DOWNSTREAM of the thing it exists to watch: a
// reconcile pass that blocked meant Probe was never called again, so
// presence_canary_missed_total could not increment, and an instance whose presence had
// stopped working read exactly like a healthy idle one. The instrument and its subject
// died together.
//
// A deadline on the pass (presence.Run) turns "forever" into "late", but it does not make
// the canary independent — a canary that only reports while its subject is healthy is not
// an instrument. Both changes are needed and neither replaces the other.
//
// The first reconciliation runs IMMEDIATELY rather than after one interval. Startup is
// the moment the gap is most likely: this replica has just missed every advisory
// published while it was not running, and a broker restarted during that window
// announced no deaths at all.
// It takes the two intervals rather than the configuration they come from, because the
// property worth testing here is that the loops do not block each other — and a test of
// that has to drive THIS function, not the two it calls. Testing the callees would pass
// just as happily against a version that put both back on one select.
func runPresenceLoops(ctx context.Context, rt *presenceRuntime, r reconcileRunner,
	c prober, reconcileInterval, canaryInterval time.Duration) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runReconcileLoop(ctx, r, reconcileInterval)
	}()
	if c != nil {
		// A nil canary means no observer is subscribed, so a probe could only ever report
		// a false miss. No goroutine rather than a goroutine that skips every tick.
		wg.Add(1)
		go func() {
			defer wg.Done()
			runCanaryLoop(ctx, c, canaryInterval)
		}()
	}
	// 🔑 A CHANNEL CLOSED BEHIND THE WAIT, NOT THE WaitGroup ITSELF. stopBrokerPresence
	// waits on rt.stopped with a five-second cap, and a WaitGroup has no timed wait — so
	// a loop that refused to end would hang shutdown instead of being abandoned.
	go func() {
		wg.Wait()
		close(rt.stopped)
	}()
}

// reconcileRunner and prober are the two loops' seams onto their subjects.
//
// They exist so the INDEPENDENCE of the two loops is testable, which is the property the
// split was made for: the only way to show it is to block one loop and watch the other
// keep going, and against the concrete types that needs a broker and an MQTT dial.
type reconcileRunner interface {
	Run(ctx context.Context) error
}

type prober interface {
	Probe(ctx context.Context) error
}

// runReconcileLoop repairs the projection against the broker on its own schedule.
func runReconcileLoop(ctx context.Context, r reconcileRunner, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runOnce := func() {
		if err := r.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("Broker presence reconciliation failed; devices whose transitions " +
				"were missed stay as the projection last recorded them until the next pass.")
		}
	}
	runOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// runCanaryLoop proves the advisory subscription is still being read.
func runCanaryLoop(ctx context.Context, c prober, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Probe(ctx); err != nil && ctx.Err() == nil {
				log.Error().Err(err).Msg("Broker presence canary probe failed.")
			}
		}
	}
}

// stopBrokerPresence unwinds the tap. Safe to call when it never started.
func stopBrokerPresence() {
	rt := brokerPresence
	if rt == nil {
		return
	}
	brokerPresence = nil
	rt.cancel()
	select {
	case <-rt.stopped:
	case <-time.After(5 * time.Second):
	}
	for _, stop := range rt.stops {
		stop()
	}
	// MAY BE NIL. When the tap never started, this runtime holds only the demotion drain,
	// which has no broker connection — it reaches device-state and user-management over
	// GraphQL, not over NATS.
	if rt.conn != nil {
		rt.conn.Close()
	}
}

// dialSystemAccount opens the SECOND broker connection this service holds — the
// data-plane one authenticates as the shared service user, this one as the
// system-account user. They are distinct connections because they are distinct
// accounts; a single connection lands in one account only.
func dialSystemAccount(ctx context.Context, cfg config.NatsConfiguration) (*nats.Conn, error) {
	url := fmt.Sprintf("nats://%s:%d", cfg.Hostname, cfg.Port)
	opts := []nats.Option{
		nats.Name("event-sources-presence"),
		nats.UserInfo(cfg.Auth.SysUser, cfg.Auth.SysPassword),
		nats.MaxReconnects(-1),
		// 🔑 RETRY, BECAUSE THE LIKELIEST FIRST DIAL IS THE ONE THAT FAILS. The bring-up
		// rolls the broker with the new system-account user in the same run that
		// restarts the services, so this service can start while the broker is mid-roll.
		// Without this, nats.Connect fails once and the tap is disabled for the pod's
		// lifetime.
		//
		// 🔴 ON ITS OWN IT DOES NOT DELIVER THAT, AND AN EARLIER COMMENT HERE CLAIMED IT
		// DID. Under this option nats.Connect returns a non-nil conn and a NIL error
		// against a broker that is not there — it hands back a stub in RECONNECTING state
		// and dials in the background (core/messaging documents the same behaviour in
		// connect_wait_test.go). Every caller that reads connection-derived state then
		// gets a zero value, and here the first one to do so was Tap.Subscribe's Flush:
		// it timed out after ten seconds, the tap was disabled for the pod's lifetime
		// exactly as if the dial had failed, and it was recorded as SUBSCRIBE_FAILED —
		// which is a replica-local reason, so no demotion ran and the whole fleet stayed
		// ASSERTED with nothing but a gauge label naming the wrong cause. waitConnected
		// below is what makes the retry mean what this comment says.
		nats.RetryOnFailedConnect(true),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn().Err(err).Msg("Lost the NATS system-account connection; broker presence is not " +
				"being observed until it returns. Reconciliation repairs the gap.")
		}),
		nats.ReconnectHandler(func(*nats.Conn) {
			log.Info().Msg("Reconnected to the NATS system account; broker presence is being observed again.")
		}),
	}
	tlsCfg, err := cfg.TLSConfig(cfg.Hostname)
	if err != nil {
		return nil, err
	}
	if tlsCfg != nil {
		opts = append(opts, nats.Secure(tlsCfg))
	}
	conn, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, err
	}
	if err := waitConnected(ctx, conn, systemAccountConnectWait); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// systemAccountConnectWait is how long the initial system-account dial is given to
// actually reach the broker before the tap gives up on this pod's run.
//
// It is longer than the ten-second Flush timeout it replaces, because the case it must
// survive is a NATS StatefulSet rolling alongside the Deployments, and shorter than
// anything that would matter to the lifecycle: this runs in the Starter's Postprocess,
// after the GraphQL server and every event source are already serving, so a wait here
// delays neither the probes nor ingest.
//
// A var, not a const, so a test can wait milliseconds for a broker it knows is not there.
var systemAccountConnectWait = 30 * time.Second

// waitConnected blocks until the connection has actually reached the broker, or until
// the deadline or ctx says it will not.
//
// 🔴 IT IS THE ONLY THING THAT CAN TELL THE TWO APART. nc.IsConnected() is false both
// for a broker that is momentarily unreachable and for one that this pod will never
// reach; polling it to a deadline is what turns the second into an answer. Without it
// every reader of connection-derived state gets a zero value and reports its own
// symptom — a Flush timeout, a bogus server version — instead of the cause.
func waitConnected(ctx context.Context, nc *nats.Conn, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if nc.IsConnected() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the system-account connection is still %s after %s", nc.Status(), timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// mqttGatewayURL is where the canary opens its probe connection.
func mqttGatewayURL(cfg config.NatsConfiguration) string {
	scheme := "tcp"
	if cfg.Tls.Enabled {
		scheme = "ssl"
	}
	// 🔴 THE HOSTNAME, NEVER AN IP. The broker's certificate carries DNS SANs and no IP
	// SAN, so a TLS probe to 127.0.0.1 fails verification while the identical probe to
	// the service name succeeds — and it fails as "canary could not connect", which
	// reads as a broken tap.
	return fmt.Sprintf("%s://%s:%d", scheme, cfg.Hostname, natsauth.MqttGatewayPort)
}

// replicaName distinguishes this pod's canary from its siblings'. A duplicate MQTT
// client id is a takeover, so replicas sharing one would evict each other in a loop and
// each read the eviction as its own failure. The pod name is the natural per-replica
// identity; the fallback keeps a single-process run (a dev box) working.
func replicaName() string {
	if name := os.Getenv("HOSTNAME"); name != "" {
		return sanitizeReplica(name)
	}
	return "local"
}

// sanitizeReplica reduces a hostname to the device-token grammar, which the canary's
// client id must satisfy: letters, digits, hyphens and underscores, starting with a
// letter or digit.
func sanitizeReplica(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	// A leading hyphen is outside the grammar, and a hostname may legitimately produce
	// one once the substitution above has run.
	for len(out) > 0 && (out[0] == '-' || out[0] == '_') {
		out = out[1:]
	}
	if len(out) == 0 {
		return "local"
	}
	return string(out)
}

// presenceEmitter writes presence transitions onto the same inbound-events stream every
// other source writes to, so a broker-asserted StateChange is resolved, projected,
// evaluated and stored by exactly the same path as a Sparkplug or LwM2M one.
//
// 🔴 authenticatedTransport IS TRUE, AND WITHOUT IT THE FEATURE IS INERT UNDER THE
// DEFAULT CONFIGURATION. A presence event carries a device token but no per-event
// credential, and under deviceAuthMode=required the resolver refuses a self-asserted
// token on an event that is not transport-authenticated — so every transition would be
// dropped at resolution, on a default instance, while the tap logged healthy emissions.
// The claim is true here for the same reason it is true for LwM2M and Sparkplug: the
// device authenticated to the broker, and the auth callout PINNED the MQTT client id to
// that device (device-management/processor/callout.go:230-247), so the token in the
// client id is the broker's word, not the device's.
func presenceEmitter() presence.Emitter {
	// "bp" namespaces this source's dedup ids so they can never collide with the
	// Sparkplug ("sp") or LwM2M ("lw") ids sharing the inbound dedup window.
	return adapter.NewEmitter(InboundEventsWriter, time.Now, "bp", true)
}

func presenceMetrics() presence.Metrics {
	return presence.Metrics{
		Emitted:           PresenceEmittedCounter,
		Skipped:           PresenceSkippedCounter,
		Refused:           PresenceRefusedCounter,
		Failed:            PresenceFailedCounter,
		RegressedSessions: PresenceRegressedCounter,
	}
}

func reconcileMetrics() presence.ReconcileMetrics {
	return presence.ReconcileMetrics{
		Runs:               PresenceReconcileCounter,
		Repaired:           PresenceRepairedCounter,
		SkippedDisconnects: PresenceWithheldCounter,
		RegressedSessions:  PresenceRegressedGauge,
	}
}

func canaryMetrics() presence.CanaryMetrics {
	return presence.CanaryMetrics{Observed: PresenceCanaryOkCounter, Missed: PresenceCanaryMissCounter}
}

func wakerMetrics() presence.WakerMetrics {
	return presence.WakerMetrics{
		Requested: CommandWakeRequestedCounter,
		Dropped:   CommandWakeDroppedCounter,
		Failed:    CommandWakeFailedCounter,
		Released:  CommandWakeReleasedCounter,
	}
}

// Metric declarations. Label sets are ours and small: state, skip reason, outcome,
// direction. 🔴 No tenant label on any of them (ADR-023 G.3) — the values here would be
// the unverified strings off a client id, which is an unbounded, device-influenceable
// cardinality vector.
var (
	PresenceEmittedCounter     *prometheus.CounterVec
	PresenceSkippedCounter     *prometheus.CounterVec
	PresenceRefusedCounter     prometheus.Counter
	PresenceFailedCounter      prometheus.Counter
	PresenceRegressedCounter   prometheus.Counter
	PresenceReconcileCounter   *prometheus.CounterVec
	PresenceRepairedCounter    *prometheus.CounterVec
	PresenceWithheldCounter    prometheus.Counter
	PresenceRegressedGauge     prometheus.Gauge
	PresenceTapOffGauge        *prometheus.GaugeVec
	PresenceReleasedCounter    prometheus.Counter
	PresenceStillAssertedGauge prometheus.Gauge
	PresenceCanaryOkCounter    prometheus.Counter
	PresenceCanaryMissCounter  prometheus.Counter

	CommandWakeRequestedCounter prometheus.Counter
	CommandWakeDroppedCounter   prometheus.Counter
	CommandWakeFailedCounter    prometheus.Counter
	CommandWakeReleasedCounter  prometheus.Counter
)

// initializePresenceMetrics registers the tap's counters. Called from
// initializeMetrics alongside the ingest ones.
//
// 🔑 THE CANARY COUNTERS ARE THE ONLY ONES WORTH ALARMING ON, and that is the whole
// argument for the canary's existence. A quiet fleet legitimately produces zero
// emissions for days, so presence_events_total reads the same whether the tap is
// healthy or dead; presence_canary_missed_total reads zero only when the chain has been
// exercised and worked.
func initializePresenceMetrics() {
	PresenceEmittedCounter = Microservice.NewCounterVec(
		"presence_events_total",
		"Broker-asserted presence transitions emitted, by state",
		[]string{"state"})
	PresenceSkippedCounter = Microservice.NewCounterVec(
		"presence_advisories_skipped_total",
		"Broker connection advisories that produced no presence event, by reason",
		[]string{"reason"})
	PresenceRefusedCounter = Microservice.NewCounter(
		"presence_events_refused_total",
		"Presence transitions refused by the ingest admission gate (deleted tenant or tenant ceiling)",
		nil)
	PresenceFailedCounter = Microservice.NewCounter(
		"presence_events_failed_total",
		"Presence transitions that could not be written to the inbound stream",
		nil)
	// 🔴 THE OLD HELP TEXT SAID "which the projection rejects as stale", AND THAT STOPPED
	// BEING TRUE when reconciliation gained a compare-and-set: a regressed session that
	// names the session the projection is holding is now ACCEPTED and re-files the device.
	// A metric description is read by whoever is deciding whether an alert matters, so one
	// that describes the old behaviour is worse than none.
	PresenceRegressedCounter = Microservice.NewCounter(
		"presence_sessions_regressed_total",
		"Presence transitions observed with a session id lower than this replica's high-water mark "+
			"(a broker node's clock may be trailing its peers). Diagnostic only: whether such a "+
			"transition is applied is decided downstream by the projection",
		nil)
	PresenceReconcileCounter = Microservice.NewCounterVec(
		"presence_reconcile_runs_total",
		"Presence reconciliation passes, by outcome",
		[]string{"outcome"})
	PresenceRepairedCounter = Microservice.NewCounterVec(
		"presence_reconcile_repaired_total",
		"Presence transitions emitted by reconciliation because an advisory was missed, by direction",
		[]string{"direction"})
	PresenceWithheldCounter = Microservice.NewCounter(
		"presence_reconcile_withheld_disconnects_total",
		"Devices that would have been marked offline had the broker inventory been provably complete",
		nil)
	PresenceRegressedGauge = Microservice.NewGauge(
		"presence_reconcile_regressed_sessions",
		"Devices found LIVE on a session id lower than the one the projection holds, as of the last "+
			"reconciliation pass. A standing non-zero value means the repairs are not converging",
		nil)
	PresenceTapOffGauge = Microservice.NewGaugeVec(
		"presence_tap_off",
		"1 when broker-asserted MQTT presence is NOT running on this replica, labelled by why. A "+
			"long-lived MQTT fleet emits no advisories for days, so the presence counters read the "+
			"same whether presence is being asserted or silently is not — this is the difference",
		[]string{"reason"})
	PresenceReleasedCounter = Microservice.NewCounter(
		"presence_released_total",
		"Devices handed back from asserted to inferred presence because this source stopped reading "+
			"the broker",
		nil)
	PresenceStillAssertedGauge = Microservice.NewGauge(
		"presence_still_asserted",
		"Devices this source still had asserted at the start of the last release pass. The work "+
			"empties itself, so a healthy drain walks this to zero and stays there",
		nil)
	PresenceCanaryOkCounter = Microservice.NewCounter(
		"presence_canary_observed_total",
		"Canary probes whose own broker connection was observed end to end",
		nil)
	PresenceCanaryMissCounter = Microservice.NewCounter(
		"presence_canary_missed_total",
		"Canary probes whose own broker connection was NOT observed — presence is not being read",
		nil)

	// The command-wake counters. 🔑 READ Dropped AND Failed AS LATENCY, NOT AS ERRORS:
	// both mean a returning device's withheld commands wait for command-delivery's
	// periodic reconcile pass instead of being released immediately, which is slower and
	// still correct. Released is the one that says the path works at all — it is the only
	// counter here that cannot move unless a wake reached command-delivery AND found
	// something to release.
	CommandWakeRequestedCounter = Microservice.NewCounter(
		"command_wake_requested_total",
		"Returning devices queued for a command wake",
		nil)
	CommandWakeDroppedCounter = Microservice.NewCounter(
		"command_wake_dropped_total",
		"Command wakes discarded because the queue was full; their commands are released later by "+
			"command-delivery's reconcile pass, so this is a latency signal rather than a loss",
		nil)
	CommandWakeFailedCounter = Microservice.NewCounter(
		"command_wake_failed_total",
		"Command wakes that could not reach command-delivery; the reconcile pass covers them",
		nil)
	CommandWakeReleasedCounter = Microservice.NewCounter(
		"command_wake_released_total",
		"Withheld commands returned to the delivery queue because their device reconnected",
		nil)
}
