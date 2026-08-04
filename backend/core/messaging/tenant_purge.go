// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	nats "github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-microservice/core"

	"github.com/devicechain-io/dc-microservice/streams"
)

// Erasing one tenant from the broker (ADR-077).
//
// This is the broker's half of the store-shaped erasure, and it sits here rather than in
// the service that runs the purge for the same reason the Postgres half sits in
// core/tenantpurge: the thing that has to be right is the SUBJECT ARITHMETIC, and the
// subject arithmetic is this package's. A caller reconstructing "{inst}.{tenant}.{suffix},
// unless it is per-device, and then also the capture shape" is writing a filter that does
// not fail when a shape changes — it silently matches nothing, and "purged 0 messages" is
// indistinguishable from "there was nothing to purge".
//
// # 🔴 An empty filter purges the WHOLE STREAM
//
// That is the one mistake in this file that cannot be walked back: a stream purge with no
// subject filter deletes every tenant's messages, and JetStream will do it without
// complaint. So the tenant is checked before anything is built, the filter is checked
// again immediately before it is sent, and both checks name what they are preventing.
// This is the broker twin of tenantpurge.checkSweepable's empty-token guard.

// Connection is the broker handle a tenant purge needs, narrowed to the one method it
// uses so the caller can hold a NatsManager without this package's consumers depending on
// the whole of it. *NatsManager satisfies it.
//
// Conn may return nil — the manager exposes it whether or not a connection has been
// established — so a caller must check rather than assume.
type Connection interface {
	Conn() *nats.Conn
}

// TenantPurgeResult is what one broker purge removed.
type TenantPurgeResult struct {
	// Messages is the total purged across every stream.
	Messages int64
	// PerStream is the count each stream contributed, keyed by stream name. Streams that
	// contributed nothing are omitted — a wall of zeroes buries the lines that matter.
	PerStream map[string]int64
	// Gateway is what the client-id-keyed sweep removed: MQTT sessions, their consumers
	// and their parked QoS 2 packets. Kept apart from Messages because those are counts of
	// different things, and Rows totals them.
	Gateway MqttGatewayPurgeResult
	// Deferrals names broker state this pass HOLDS AND DID NOT ERASE, in sentences meant
	// for whoever is deciding whether an erasure claim can be made.
	//
	// 🔑 IT IS A MEASUREMENT NOW, NOT A CONSTANT, AND THAT IS THE POINT OF THE CHANGE. It
	// used to be a fixed list of three things a subject filter cannot reach, which meant
	// the broker store could never report clean and no purge could ever complete. Two of
	// those are now erased (tenant_purge_mqtt.go) and the third is covered by the settle
	// window (see RetainedCacheWindow). What is left is reported only when this pass
	// actually OBSERVED it — so an empty slice here is a statement about this broker on
	// this pass rather than an absence of imagination.
	Deferrals []string
}

// Rows is what the coordinator records as erased: every kind of thing removed, summed.
//
// Summing rather than reporting messages alone matters because the settle window restarts
// on any pass that erased anything. A pass that deleted only a session record has just
// found this tenant's data in the broker, and reporting zero would measure the window from
// a moment the broker demonstrably still held it.
func (r TenantPurgeResult) Rows() int64 { return r.Messages + r.Gateway.Total() }

// mqttTenantStreams are the gateway streams whose SUBJECTS carry the tenant, so a subject
// filter reaches them exactly the way it reaches a platform stream.
//
// Both wrap an original subject: nats-server publishes to "$MQTT.msgs.{subject}" and
// "$MQTT.rmsgs.{subject}", so a tenant's messages sit under a prefix of our own subject
// space. The other three gateway streams key on the MQTT CLIENT ID instead and are not
// reachable this way at all; they are erased by reading rather than by filtering, in
// tenant_purge_mqtt.go.
var mqttTenantStreams = []struct{ stream, prefix string }{
	{MqttMessageStore, "$MQTT.msgs."},
	{MqttRetainedStore, "$MQTT.rmsgs."},
}

// PurgeTenant removes every message belonging to tenant from every stream whose subjects
// name it.
//
// It is idempotent: a second call purges nothing and reports zero, which is what the
// Store contract requires of a pass that runs after the work is done.
//
// A stream that does not exist is not an error. Streams are created lazily — by
// ensureStream for a platform stream, by nats-server itself for a gateway one — so an
// instance that has never used MQTT has no gateway streams, and an area that has never
// started has no stream of its own. "Not there" and "there and empty" are the same fact
// for an erasure.
func PurgeTenant(ctx context.Context, nc *nats.Conn, instanceId, tenant string) (TenantPurgeResult, error) {
	res := TenantPurgeResult{PerStream: map[string]int64{}}
	// 🔴 THE TOKEN IS VALIDATED HERE EVEN THOUGH IT CANNOT REACH STORAGE INVALID.
	// ADR-042's grammar callback rejects a NATS metacharacter at create/update, so this
	// should be unreachable — but "should be unreachable" is a claim about every write path
	// that has ever existed, and what it is guarding is not recoverable. A token carrying a
	// `>` or a `.` does not make this purge fail; it makes it match MORE, and a stream purge
	// has no undo. The same check already guards the publish path (nats.go), which is the
	// weaker consequence of the two. It also subsumes the empty case: an empty token is the
	// most direct way to end up with no filter at all.
	if err := core.ValidateToken(tenant); err != nil {
		return res, fmt.Errorf("refusing to purge the broker for tenant %q: %w — a token that is "+
			"not a plain identifier can widen the subject filter, and a widened purge deletes "+
			"other tenants' messages irrecoverably", tenant, err)
	}
	if strings.TrimSpace(instanceId) == "" {
		return res, fmt.Errorf("refusing to purge the broker with no instance id: every subject is " +
			"rooted at it, so the filter would match nothing and report a clean broker")
	}
	// 🔴 A DISCONNECTED CLIENT DOES NOT FAIL, IT WAITS. The manager connects with infinite
	// reconnects, so while the broker is down a request is buffered rather than refused and
	// the call blocks until the broker returns — which on the purge coordinator's single
	// goroutine stalls every store for every purging tenant, holding the advisory lock, with
	// no error and no log line. "Never reports clean" in its most invisible form, reachable
	// on the very first pass if the pod starts while the broker is down.
	//
	// THE DEADLINE IS THE GUARD, NOT THE IsConnected CHECK. A client does not learn it has
	// been disconnected until a ping fails, so asking first catches only an outage that has
	// already been noticed — worth having, because it turns the common case into an
	// immediate verdict, but it is not what makes the stall impossible.
	//
	// The deadline covers the WHOLE purge rather than each request. Per-request, an outage
	// costs one timeout per stream and the pass still stalls for minutes; here it costs one,
	// and the context the caller handed us has no deadline of its own (the coordinator's is
	// cancel-only, cancelled at service stop).
	if !nc.IsConnected() {
		return res, fmt.Errorf("the broker connection is down, so nothing can be purged for %q on "+
			"this pass", tenant)
	}
	ctx, cancel := context.WithTimeout(ctx, PurgeTimeout)
	defer cancel()

	// streams.Suffixes() is the complete declared set and needs no supplementing: a
	// dead-letter stream is its own declared entry (streams.All carries it, built with
	// DeadLetter), so deriving one here would purge it twice.
	for _, suffix := range streams.Suffixes() {
		stream := StreamName(instanceId, suffix)
		filter := TenantSubjectFilter(instanceId, tenant, suffix)
		n, err := purgeSubject(ctx, nc, stream, filter)
		if err != nil {
			return res, err
		}
		if n > 0 {
			res.PerStream[stream] += n
			res.Messages += n
		}
	}

	// The gateway's subject-carrying streams. Their filter is our own tenant filter under
	// the gateway's prefix — ">" rather than a per-suffix shape, because what lands here
	// is whatever a device published over MQTT, which is the device-events shape today and
	// need not stay that way.
	for _, m := range mqttTenantStreams {
		filter := m.prefix + instanceId + "." + tenant + ".>"
		n, err := purgeSubject(ctx, nc, m.stream, filter)
		if err != nil {
			return res, err
		}
		if n > 0 {
			res.PerStream[m.stream] += n
			res.Messages += n
		}
	}

	// The gateway's client-id-keyed state, which no subject filter reaches at all. It runs
	// LAST so a failure here does not cost the subject purge that already succeeded: the
	// caller retries the whole thing, and everything above is idempotent.
	gw, err := PurgeTenantMqttGateway(ctx, nc, instanceId, tenant)
	if err != nil {
		return res, err
	}
	res.Gateway = gw
	if gw.UnownedSessions > 0 {
		res.Deferrals = append(res.Deferrals, fmt.Sprintf("the broker holds %d MQTT session record(s) "+
			"whose client id does not have the pinned device shape, so they cannot be attributed to any "+
			"tenant and this purge did not touch them. Sessions filed by a non-device client — the edge "+
			"agent's uplink is the standing one — are expected here and belong to nobody. A session filed "+
			"by a DEVICE before the client id was pinned would look identical, and would be this tenant's "+
			"data. Both need telling apart by hand before an erasure is claimed", gw.UnownedSessions))
	}

	return res, nil
}

// purgeSubject purges one stream by subject filter and reports how many messages went.
//
// # Why this issues the API request instead of calling js.PurgeStream
//
// The library's wrapper returns only an error and discards the server's `purged` count —
// and that count is what the deletion record reports. Everything about the REQUEST still
// comes from the library: the payload is a marshalled nats.StreamPurgeRequest, so the
// filter's wire field name is the library's, not something spelled out here. That
// distinction matters more than it looks. `filter` misspelled in a hand-built payload is
// not a failed purge, it is a purge with NO filter — which JetStream executes happily,
// against every tenant on the stream.
func purgeSubject(ctx context.Context, nc *nats.Conn, stream, filter string) (int64, error) {
	if strings.TrimSpace(filter) == "" {
		return 0, fmt.Errorf("refusing to purge %s with an empty subject filter: JetStream would "+
			"delete every tenant's messages on that stream", stream)
	}

	payload, err := json.Marshal(&nats.StreamPurgeRequest{Subject: filter})
	if err != nil {
		return 0, err
	}
	msg, err := nc.RequestWithContext(ctx, "$JS.API.STREAM.PURGE."+stream, payload)
	if err != nil {
		return 0, fmt.Errorf("purging %s for %q: %w", stream, filter, err)
	}

	var resp struct {
		Error *struct {
			ErrCode     uint16 `json:"err_code"`
			Description string `json:"description"`
		} `json:"error"`
		Purged int64 `json:"purged"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return 0, fmt.Errorf("reading the purge response for %s: %w", stream, err)
	}
	if resp.Error != nil {
		// 10059 is stream-not-found. A stream that does not exist holds nothing, and
		// streams here are created lazily — an instance that has never used MQTT has no
		// gateway streams at all.
		if resp.Error.ErrCode == streamNotFoundErrCode {
			return 0, nil
		}
		return 0, fmt.Errorf("purging %s for %q: %s", stream, filter, resp.Error.Description)
	}
	return resp.Purged, nil
}

// streamNotFoundErrCode is JetStream's API error code for a stream that does not exist.
const streamNotFoundErrCode = 10059

// PurgeTimeout bounds one whole broker purge, so a broker that goes away mid-pass costs
// the coordinator one wait rather than an unbounded one.
//
// It is deliberately short relative to the coordinator's tick. Getting it wrong in the
// SHORT direction is cheap: a timeout does not cancel the purge — the request has been
// delivered, and the server does the work and replies to nobody — so the cost is one pass
// reporting a failure over work that actually happened, which the next pass sees as
// already done. Getting it wrong in the LONG direction stalls every tenant's purge.
//
// Exported so a caller can reason about it against its own tick.
const PurgeTimeout = 20 * time.Second

// The three things a subject purge could not reach, and what became of each.
//
// This used to be TenantPurgeDeferrals — a fixed list returned on every pass, which the
// coordinator turned into "not clean", which meant no purge could ever complete. The list
// is gone because all three are answered, and it is worth recording HOW, because two of
// the answers are erasures and one is an argument, and an argument is the kind of answer
// this epic has repeatedly got wrong.
//
//  1. MQTT SESSION STATE — erased. PurgeTenantMqttGateway reads $MQTT_sess, attributes each
//     record from its own payload, and deletes the record, its parked QoS 2 packets in
//     $MQTT_qos2in and $MQTT_out, and its PUBREL consumer.
//
//  2. DURABLE CONSUMERS — erased. A subscription consumer's filter subject names the
//     tenant, so they are swept from the consumer list, which also reaches a consumer whose
//     session record is gone. That one mattered most: $MQTT_msgs is interest-retention, so
//     an orphan does not merely linger, it keeps new messages on the deleted tenant's
//     subjects alive.
//
//  3. THE RETAINED-MESSAGE CACHE — argued, not erased, and it cannot be erased: the cache
//     is in-process in nats-server with no API. See RetainedCacheWindow for why the settle
//     window covers it, and for what has to stay true for that to hold.
//
// What replaces the list is TenantPurgeResult.Deferrals, which reports only what a pass
// actually observed.

// RetainedCacheWindow is how long nats-server may keep serving a retained message after it
// has been purged from JetStream, from its in-memory cache (`mqttRetainedCacheTTL`, a
// package constant in the server with no configuration knob).
//
// 🔴 IT IS EXPORTED SO A CALLER'S SETTLE WINDOW CAN BE CHECKED AGAINST IT RATHER THAN
// ASSUMED TO COVER IT. A purge that completes inside this window writes a record saying a
// tenant's data is gone while the broker is still handing its retained payloads to new
// subscribers — which is exactly the "swept AND FENCED" ack being false.
// user-management's config validation enforces that floor and refuses to start below it.
//
// # 🔑 Why this is no longer a deferral, in full, because the argument is the answer
//
// Three facts have to hold together, and each is checkable:
//
//   - A cache entry expires a fixed TTL after it was STORED, and a cache hit does not
//     extend it — nats-server's lookup path deletes an expired entry and never restamps a
//     live one. So an entry's life is bounded from the moment it was filled.
//   - An entry is only ever filled from a JetStream READ of the retained message. Once the
//     purge has removed the message, no later subscriber can refill it, so the last
//     possible fill is at the instant of the purge.
//   - A pass that erased anything restarts the settle window (Coordinator.eraseOne), and
//     completion requires the whole settle window to elapse after that. Since the settle
//     window is validated to exceed this one, the cache entry is expired before the purge
//     can complete.
//
// 🔴 THE THIRD IS THE ONE THAT ROTS. It rests on the coordinator restarting the window on
// a non-zero row count, and on the config floor. Neither is visible from this file, and
// both would keep compiling if they changed. They are pinned by tests on their own side —
// TestSettleWindowMustExceedTheRetainedCacheWindow in user-management's config, and the
// coordinator's own settle-restart test — and this comment is the pointer between them.
const RetainedCacheWindow = 2 * time.Minute
