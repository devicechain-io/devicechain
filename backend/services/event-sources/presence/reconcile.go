// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// TenantLister names every tenant on the instance. Reconciliation cannot discover
// tenants any other way: a tenant whose whole fleet dropped appears in neither the
// broker's inventory nor any recent traffic, and it is exactly that tenant whose
// devices are stuck asserted-online.
type TenantLister interface {
	TenantTokens(ctx context.Context) ([]string, error)
}

// StoredDevice is what the projection currently holds for one asserted device of this
// source. Active distinguishes the two halves of the diff, and SessionId is what makes a
// repair for an INACTIVE row applicable at all — see reconcileTenant.
type StoredDevice struct {
	Tenant      string
	DeviceToken string
	SessionId   uint64
	Active      bool
}

// ProjectionReader reads what the device-state projection currently believes about
// this source's devices, keyed by DeviceKey.
//
// 🔑 KEYED BY DEVICE TOKEN, NOT EXTERNAL ID. The existing adapter.Reconciler is
// external-id keyed and SKIPS every row without one (adapter/ingest.go), which is right
// for Sparkplug and LwM2M — their devices are addressed by a transport-native identity —
// and wrong for plain MQTT, where external id is optional and usually absent. Reused
// here it would report almost every connected device as "not asserted online", and the
// connect direction would re-emit a StateChange for the whole fleet on every pass: a
// durable event row per device per pass, through the resolver, the projection, DETECT
// and the historian.
//
// 🔴 IT READS THE INACTIVE ROWS TOO, and that is not for symmetry. The repair a device
// needs is the one for a row that reads OFFLINE, and such a repair has to carry a
// session id presence.Decide will accept. Without the stored id there is nothing to
// compare the broker's against, and a regressed session produces a repair that is
// rejected on this pass and on every pass after it.
type ProjectionReader interface {
	AssertedStates(ctx context.Context, tenant, source string) (map[string]StoredDevice, error)
}

// ReconcileMetrics are the repair pass's operator signals.
type ReconcileMetrics struct {
	// Runs counts completed passes, labelled by outcome ("complete", "partial",
	// "failed"). A pass that could not prove the inventory complete does only half
	// its job, and that must be visible as a shape rather than inferred from silence.
	Runs *prometheus.CounterVec
	// Repaired counts synthetic transitions emitted, labelled by direction
	// ("connect", "disconnect"). In a healthy instance this is near zero; a standing
	// rate means advisories are being lost.
	//
	// 🔴 EMITTED, NOT APPLIED, AND NOTHING HERE CAN NARROW THAT. Whether the projection
	// accepts a transition is decided asynchronously and downstream, so this counter
	// cannot distinguish a repair that landed from one presence ordering threw away.
	// That gap used to be reachable in a way that mattered — a repair rejected on every
	// pass forever, counted as a success every time — which is why reconcileTenant now
	// constructs repairs Decide will accept rather than leaving the counter to imply it.
	Repaired *prometheus.CounterVec
	// SkippedDisconnects counts devices that WOULD have been marked offline had the
	// inventory been provably complete. It is the cost of the safety rule, and
	// without it an instance that never proves completeness looks identical to one
	// with nothing to repair.
	SkippedDisconnects prometheus.Counter
}

func (m ReconcileMetrics) run(outcome string) {
	if m.Runs != nil {
		m.Runs.WithLabelValues(outcome).Inc()
	}
}

func (m ReconcileMetrics) repaired(direction string, n int) {
	if m.Repaired != nil && n > 0 {
		m.Repaired.WithLabelValues(direction).Add(float64(n))
	}
}

// Reconciler repairs the difference between what the broker holds and what the
// projection believes.
//
// 🔴 IT IS NOT A BACKSTOP, IT IS THE ONLY ACCOUNT OF SOME DEATHS. A graceful broker
// shutdown emits NO disconnect advisories for the connections it was holding —
// measured against nats-server v2.14.4 — so every rolling broker upgrade leaves every
// connected device asserted-online. Nothing else corrects that: an asserted row is
// skipped by the inactivity sweep (device-state/model/api.go:425), and data events
// cannot flip an asserted device's Active (api.go:196). Both existing asserted sources
// repair their own missed deaths for the same reason (sparkplug host.go:567,
// lwm2m main.go:566); this one would have been the first that could not.
type Reconciler struct {
	tap      *Tap
	requests Requester
	tenants  TenantLister
	reads    ProjectionReader
	metrics  ReconcileMetrics
	gather   time.Duration
	now      func() time.Time
	// clusterHighWater is the largest cluster this process has ever been told about.
	//
	// 🔴 WITHOUT IT, A FULL PARTITION DECLARES ITSELF COMPLETE. Completeness compares
	// the servers that answered against the size they CLAIM, and a server that has lost
	// its routes reports itself alone — so in a partition every reachable server claims
	// a cluster of one, the subset we can reach satisfies its own claim, and the pass
	// happily marks the unreachable side's devices offline. That is the mass false death
	// the completeness rule exists to prevent, arriving through the rule itself.
	//
	// A high-water mark closes it because a cluster does not shrink in normal operation:
	// once three servers have been seen, two answering is a partition, not a fact about
	// the cluster. A DELIBERATE scale-down does leave this too high, and the consequence
	// is that deaths are withheld — counted and logged, never silent — until the process
	// restarts. Withholding is the safe direction, and paying for it with a pod restart
	// on a rare operator action is the right trade.
	clusterHighWater int
}

// 🔑 A REPAIR AND THE TRANSITION IT REPLACES SHARE A DEDUP ID, WHICH IS WANTED AND
// SURPRISING. presenceDedupID keys on (tenant, device, session, state), so two repair
// passes emitting the same synthetic transition inside the JetStream dedup window
// collapse to one write — idempotent, which is the point. The consequence to know before
// reading a test result: a repair issued moments after the transition it is replacing can
// be SUPPRESSED rather than applied, so an integration test that reconciles twice inside
// one dedup window sees a single event and can read as "reconciliation is broken".
//
// NewReconciler binds a repair pass. now may be nil (⇒ time.Now).
func NewReconciler(tap *Tap, requests Requester, tenants TenantLister, reads ProjectionReader,
	metrics ReconcileMetrics, gather time.Duration, now func() time.Time) *Reconciler {
	if now == nil {
		now = time.Now
	}
	return &Reconciler{tap: tap, requests: requests, tenants: tenants, reads: reads,
		metrics: metrics, gather: gather, now: now}
}

// Run performs one repair pass.
func (r *Reconciler) Run(ctx context.Context) error {
	inv, err := FetchInventory(ctx, r.requests, r.tap.instanceId, r.gather)
	if err != nil {
		r.metrics.run("failed")
		return err
	}
	inv = r.applyClusterHighWater(inv)
	tenants, err := r.tenants.TenantTokens(ctx)
	if err != nil {
		r.metrics.run("failed")
		return err
	}

	connects, disconnects, withheld := 0, 0, 0
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			break
		}
		stored, err := r.reads.AssertedStates(ctx, tenant, r.tap.source)
		if err != nil {
			// One tenant's read failing must not abort the others: the remaining
			// tenants' devices are equally stuck. It does cost this tenant a pass.
			log.Warn().Err(err).Str("tenant", tenant).
				Msg("Could not read presence state while reconciling; this tenant is skipped for this pass.")
			continue
		}
		c, d, w := r.reconcileTenant(ctx, tenant, inv, stored)
		connects, disconnects, withheld = connects+c, disconnects+d, withheld+w
	}

	r.metrics.repaired("connect", connects)
	r.metrics.repaired("disconnect", disconnects)
	if withheld > 0 && r.metrics.SkippedDisconnects != nil {
		r.metrics.SkippedDisconnects.Add(float64(withheld))
	}
	outcome := "complete"
	if !inv.Complete {
		outcome = "partial"
		log.Warn().Int("serversAnswered", inv.Servers).Int("serversExpected", inv.Expected).
			Int("withheldDisconnects", withheld).
			Msg("Broker connection inventory was incomplete; devices were only ever marked ONLINE this pass.")
	}
	r.metrics.run(outcome)
	if connects > 0 || disconnects > 0 {
		log.Info().Int("connects", connects).Int("disconnects", disconnects).
			Msg("Reconciled broker-asserted presence against the broker's live connections.")
	}
	return nil
}

// applyClusterHighWater raises the pass's expectation to the largest cluster ever seen,
// downgrading Complete when fewer servers answered than that. See clusterHighWater.
func (r *Reconciler) applyClusterHighWater(inv Inventory) Inventory {
	if inv.Expected > r.clusterHighWater {
		r.clusterHighWater = inv.Expected
	}
	// The count that ANSWERED is itself evidence of cluster size: three replies prove
	// three servers exist regardless of what any of them claims.
	if inv.Servers > r.clusterHighWater {
		r.clusterHighWater = inv.Servers
	}
	inv.Expected = r.clusterHighWater
	inv.Complete = inv.Servers > 0 && inv.Servers >= r.clusterHighWater && inv.Complete
	return inv
}

// reconcileTenant diffs one tenant both ways, returning (connects, disconnects,
// withheld-disconnects).
func (r *Reconciler) reconcileTenant(ctx context.Context, tenant string, inv Inventory,
	stored map[string]StoredDevice) (int, int, int) {
	connects, disconnects, withheld := 0, 0, 0

	// One stamp for the pass, used both as the transitions' OccurredAt and as their dedup
	// nonce, so every emission below is attributable to this pass and no two passes
	// collapse into one write. See PresenceEvent.DedupNonce.
	at := r.now().UTC()
	nonce := at.Format(time.RFC3339Nano)

	// Direction 1 — the broker holds a connection the projection does not know is
	// live. Positive evidence, so it is emitted whether or not the inventory is
	// complete: a missing server's reply costs a delayed repair, never a false claim.
	for key, live := range inv.Devices {
		if live.Tenant != tenant {
			continue
		}
		known, seen := stored[key]
		if seen && known.Active {
			continue
		}
		// 🔴 STAMPED AT NOW, NOT AT THE CONNECTION'S START, AND THE OBVIOUS CHOICE IS
		// THE BROKEN ONE. The start is the more truthful instant and it is what the lost
		// advisory would have carried — but presence.Decide takes a SAME-session
		// transition only when its time is newer than the stored one, and the projection
		// may hold this very session at a LATER time: after a synthetic death (which
		// reuses the stored session and stamps NOW), the row reads offline at a moment
		// well after the connection began. A repair carrying the start is then older
		// than the death, rejected, and rejected identically on every subsequent pass —
		// forever, silently, while the repair counter says it worked. That is the
		// failure that looks exactly like success, and it wedges a live device offline,
		// holding its commands.
		//
		// The session id is normally the CONNECTION's, so this remains the same session
		// the advisory would have named and the dedup id is unchanged. What is given up
		// is accuracy of LastConnectTime on the repair path alone: it records when the
		// platform re-established the truth rather than when the device connected. On a
		// path that exists because the accurate signal was already lost, that is the
		// right thing to trade.
		//
		// 🔴 EXCEPT WHEN THE CONNECTION'S OWN SESSION HAS GONE BACKWARDS, WHICH IS A
		// PERMANENT WEDGE AND NOT A RARE ONE ON A CLUSTER. Session ids are minted from
		// the wall clock of whichever broker node the device landed on. A reconnect onto
		// a node with a trailing clock carries a LOWER id than the projection is holding,
		// Decide takes a different session only when it is HIGHER, so the real CONNECT is
		// rejected — and the old session's DISCONNECT, being same-session-newer-time, is
		// not. The row reads offline while the device publishes. A repair carrying the
		// connection's own low id is rejected the same way, on this pass and on every
		// pass after it, forever, while the repair counter reports success.
		//
		// Reusing the STORED id makes the repair same-session-newer-time, which Decide
		// accepts. This is exactly the trick direction 2 below already relies on, applied
		// in the other direction, and it is the only claim here that is not the
		// connection's own: the projection then records this connection under the
		// session it was already tracking. The real session's eventual DISCONNECT will
		// be rejected for the same ordering reason — and repaired by direction 2, which
		// reuses the stored id and therefore converges.
		//
		// 🔑 THE TRADE, STATED SO NOBODY REDISCOVERS IT AS A BUG. Once a device is pinned
		// this way, its death is invisible to the advisory path and reaches the
		// projection ONLY through direction 2, which is gated on a provably complete
		// inventory. So a permanent false-OFFLINE (commands held forever) becomes a
		// false-ONLINE that lasts as long as the inventory cannot be proved complete —
		// counted by SkippedDisconnects and logged by the partial-pass warning. That is
		// the direction this codebase fails in everywhere else, and it is observable,
		// which the state it replaces was not.
		session := live.SessionId
		if seen && live.SessionId <= known.SessionId {
			session = known.SessionId
			// Only the STRICTLY lower case implicates a clock. Equality is the ordinary
			// recovery from a synthetic death: same connection, same session, the row
			// simply reads offline at a later instant than the connect. Saying "trailing
			// clock" there sends an operator auditing broker clocks for a missed advisory.
			if live.SessionId < known.SessionId {
				log.Warn().Str("tenant", live.Tenant).Str("device", live.DeviceToken).
					Uint64("brokerSession", live.SessionId).Uint64("storedSession", known.SessionId).
					Msg("Repairing a device whose broker session id is OLDER than the stored one; " +
						"a broker node's clock is trailing its peers.")
			}
		}
		if r.tap.Apply(ctx, Transition{
			Tenant:      live.Tenant,
			DeviceToken: live.DeviceToken,
			Event: adapter.PresenceEvent{
				Connected:  true,
				Reason:     "reconcile-connected",
				SessionId:  session,
				OccurredAt: at,
				DedupNonce: nonce,
			},
		}) {
			connects++
		}
	}

	// Direction 2 — the projection believes a device is online that the broker is not
	// holding. This is the direction that can LIE, so it is gated on completeness.
	for key, believed := range stored {
		if !believed.Active {
			continue
		}
		if _, live := inv.Devices[key]; live {
			continue
		}
		if !inv.Complete {
			// A device on a server that did not answer is absent from Devices and
			// indistinguishable from one that is genuinely gone. Marking it offline
			// would hold a live device's commands, so the pass withholds instead.
			withheld++
			continue
		}
		if r.tap.Apply(ctx, Transition{
			Tenant:      tenant,
			DeviceToken: believed.DeviceToken,
			Event: adapter.PresenceEvent{
				Connected: false,
				Reason:    "reconcile-not-connected",
				// 🔑 THE SESSION IS THE ONE THE PROJECTION ALREADY HOLDS, and this is
				// the difference between a repair that works and one that is silently
				// rejected forever. presence.Decide takes a DIFFERENT session only when
				// it is HIGHER; a synthetic death carrying some other id — the last
				// known connection's, or one minted here — would be lower than the
				// stored one and rejected on every pass, with nothing to say why.
				// Reusing the stored id makes this same-session-newer-time, which
				// Decide accepts and which is exactly what this event means: the
				// session the projection is tracking has ended.
				SessionId:  believed.SessionId,
				OccurredAt: at,
				DedupNonce: nonce,
			},
		}) {
			disconnects++
		}
	}
	return connects, disconnects, withheld
}
