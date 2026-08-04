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
}

// mqttTenantStreams are the gateway streams whose SUBJECTS carry the tenant, so a subject
// filter reaches them exactly the way it reaches a platform stream.
//
// Both wrap an original subject: nats-server publishes to "$MQTT.msgs.{subject}" and
// "$MQTT.rmsgs.{subject}", so a tenant's messages sit under a prefix of our own subject
// space. The other three gateway streams key on the MQTT CLIENT ID instead and are not
// reachable this way; see TenantPurgeDeferrals.
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

// TenantPurgeDeferrals names the broker state a subject purge does NOT reach, in sentences
// meant for whoever is deciding whether an erasure claim can be made.
//
// 🔴 THESE ARE NOT A BACKLOG. Each one is tenant data that survives a purge, and the
// coordinator carries them into the deletion record so a completed purge cannot claim
// total erasure while they stand. They come off this list by being erased, not by being
// re-worded.
func TenantPurgeDeferrals() []string {
	return []string{
		"the broker still holds this tenant's MQTT SESSION state — one record per persistent " +
			"session in $MQTT_sess, naming the client's tenant-scoped subscriptions, plus its " +
			"parked QoS 2 records in $MQTT_qos2in and $MQTT_out. Those streams key on the MQTT " +
			"client id, which is chosen by device firmware and bound to no tenant, so no subject " +
			"filter reaches them. A successor at a reused token that presents the same client id " +
			"inherits the predecessor's subscriptions and its undelivered messages",
		"the broker still holds this tenant's DURABLE CONSUMERS on $MQTT_msgs and $MQTT_out. A " +
			"stream purge does not remove a consumer, and $MQTT_msgs is interest-retention — so " +
			"an orphaned consumer whose filter names this tenant's subjects actively keeps new " +
			"messages on that subject alive rather than merely lingering",
		"a purge of the retained-message stream does not take effect immediately: nats-server " +
			"answers a new subscriber from an in-memory retained cache before it reads JetStream, " +
			"so this tenant's retained payloads can still be delivered for up to two minutes " +
			"after the purge. The cache TTL is a compiled-in constant with no configuration knob",
	}
}

// RetainedCacheWindow is how long nats-server may keep serving a retained message after it
// has been purged from JetStream, from its in-memory cache (`mqttRetainedCacheTTL`, a
// package constant in the server with no configuration knob).
//
// 🔴 IT IS EXPORTED SO A CALLER'S SETTLE WINDOW CAN BE CHECKED AGAINST IT RATHER THAN
// ASSUMED TO COVER IT. A purge that completes inside this window writes a record saying a
// tenant's data is gone while the broker is still handing its retained payloads to new
// subscribers — which is exactly the "swept AND FENCED" ack being false.
const RetainedCacheWindow = 2 * time.Minute
