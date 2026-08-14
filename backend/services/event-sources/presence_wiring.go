// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
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
	// that touched a device (device-state/model/api.go:133-135), so presence under a
	// different name would make a device's row alternate between two sources on every
	// event — and the reconciler's source-scoped read would then see the device only
	// half the time, repairing it intermittently.
	source, ok := GatewaySourceId, GatewaySourceId != ""
	switch {
	case !cfg.IsEnabled():
		log.Info().Msg("Broker-asserted MQTT presence is disabled by configuration; MQTT presence stays inferred.")
		return
	case infra.Nats.Auth.SysUser == "" || infra.Nats.Auth.SysPassword == "":
		log.Info().Msg("No NATS system-account credential is configured, so broker-asserted MQTT presence is " +
			"off and MQTT presence stays inferred. Re-run the bring-up to mint one.")
		return
	case !ok:
		log.Info().Msg("No event source is pointed at the platform broker, so there are no broker " +
			"connection advisories to read; MQTT presence stays inferred.")
		return
	case infra.ServiceAuth.Secret == "" || infra.UserManagement.Hostname == "":
		log.Warn().Msg("Broker-asserted MQTT presence needs service-to-service calls to enumerate tenants " +
			"and read presence state, which are not configured. It stays OFF rather than running without " +
			"its repair path: a device whose disconnect the broker never announced would otherwise read " +
			"as connected forever.")
		return
	}

	conn, err := dialSystemAccount(infra.Nats)
	if err != nil {
		log.Error().Err(err).Msg("Could not connect to the NATS system account; broker-asserted MQTT " +
			"presence is OFF and MQTT presence stays inferred.")
		return
	}

	tap := presence.NewTap(Microservice.InstanceId, source, presenceEmitter(), presence.Gate(ingestGate), presenceMetrics())
	stopTap, err := tap.Subscribe(conn)
	if err != nil {
		conn.Close()
		log.Error().Err(err).Msg("Could not subscribe to the broker's connection advisories; " +
			"broker-asserted MQTT presence is OFF.")
		return
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
	rt.conn.Close()
}

// dialSystemAccount opens the SECOND broker connection this service holds — the
// data-plane one authenticates as the shared service user, this one as the
// system-account user. They are distinct connections because they are distinct
// accounts; a single connection lands in one account only.
func dialSystemAccount(cfg config.NatsConfiguration) (*nats.Conn, error) {
	url := fmt.Sprintf("nats://%s:%d", cfg.Hostname, cfg.Port)
	opts := []nats.Option{
		nats.Name("event-sources-presence"),
		nats.UserInfo(cfg.Auth.SysUser, cfg.Auth.SysPassword),
		nats.MaxReconnects(-1),
		// 🔑 RETRY, BECAUSE THE LIKELIEST FIRST DIAL IS THE ONE THAT FAILS. The bring-up
		// rolls the broker with the new system-account user in the same run that
		// restarts the services, so this service can start while the broker is mid-roll.
		// Without this, nats.Connect fails once, the tap is disabled for the pod's
		// lifetime, and the only symptom is a capability quietly absent — the failure
		// mode the canary exists to catch, except the canary is disabled too.
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
	return nats.Connect(url, opts...)
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
	PresenceEmittedCounter    *prometheus.CounterVec
	PresenceSkippedCounter    *prometheus.CounterVec
	PresenceRefusedCounter    prometheus.Counter
	PresenceFailedCounter     prometheus.Counter
	PresenceRegressedCounter  prometheus.Counter
	PresenceReconcileCounter  *prometheus.CounterVec
	PresenceRepairedCounter   *prometheus.CounterVec
	PresenceWithheldCounter   prometheus.Counter
	PresenceRegressedGauge    prometheus.Gauge
	PresenceCanaryOkCounter   prometheus.Counter
	PresenceCanaryMissCounter prometheus.Counter

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
