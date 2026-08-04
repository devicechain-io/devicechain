// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	nats "github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-microservice/core"
)

// Erasing one tenant's MQTT GATEWAY state (ADR-077) — the half a subject filter cannot
// reach, and the reason the broker store used to be unable to report clean.
//
// # 🔑 Nothing here is computed. Everything here is READ
//
// That sentence is the whole design, and it is a correction of an earlier claim rather
// than a flourish. Pinning the device client id (mqtt_clientid.go) was justified at the
// time as making these records "computable from our own DB", and that was FALSE: $MQTT_sess
// files a session under an 8-character digest of the client id, and $MQTT_qos2in carries
// the raw id as a single subject token, which NATS wildcards cannot match a prefix of.
// Neither is addressable from anything the platform stores.
//
// What the pin actually bought is ATTRIBUTION, and attribution is enough, because every
// address this erasure needs is written down somewhere the broker will hand over:
//
//   - the client id — in the $MQTT_sess record's own PAYLOAD ("id"), which is what makes
//     an otherwise anonymous record nameable;
//   - the QoS 2 PUBREL subject and its consumer's durable name — in the same payload
//     ("pubrel"), as the server's own values, so the digest never has to be recomputed;
//   - a subscription consumer's filter — on the CONSUMER itself, and it is
//     "$MQTT.msgs.{instance}.{tenant}...", so the tenant is right there in it;
//   - the parked QoS 2 packets — under subjects the stream will enumerate, each carrying
//     the client id verbatim.
//
// So this file reimplements no server internal, and in particular it never calls anything
// resembling getHash. A reimplementation would be a claim about another project's
// unexported code that compiles perfectly whether or not it is true — the exact shape of
// defect this epic keeps producing. The one derivation that remains (an idHash read off a
// session record's SUBJECT, for a session that has no pubrel consumer to read it from) is
// cross-checked against the server's own value by a live-broker test rather than trusted.
//
// # Why this reads the whole session stream
//
// There is no filter that selects one tenant's sessions, so the scan is total, exactly as
// the key-value purge's is. It is read in BATCHES through one ordered consumer rather than
// one request per record, and that is a liveness requirement, not a throughput preference:
// a scan that cannot finish inside PurgeTimeout restarts from the beginning on the next
// pass and times out in the same place, forever. Multi-pass idempotency does not rescue a
// scan whose progress is not durable. Batching is what keeps the whole stream inside one
// pass at fleet scale.

// mqttSessionRecord is the subset of nats-server's mqttPersistedSession this purge reads.
//
// 🔴 IT IS DELIBERATELY A LOCAL SNAPSHOT, NOT AN IMPORT. core/messaging does not depend on
// nats-server outside its tests and must not start: the server is a deployment artifact
// here, not a library. Declaring the shape locally is the same discipline a migration's
// snapshot structs follow — this describes the bytes on the wire at a point in time, and
// it should fail visibly if the server changes them rather than silently track a struct
// that moved underneath it.
//
// Unknown fields are ignored by encoding/json, which is correct here: a server that adds a
// field must not break the erasure. A server that RENAMES "id" would break it, which is
// why TestMqttSessionRecordMatchesWhatTheServerWrites reads a record a real broker wrote
// rather than one this test package composed.
type mqttSessionRecord struct {
	// ID is the MQTT client id — the only thing in the record that names an owner.
	ID string `json:"id,omitempty"`
	// Cons is the session's subscription consumers on $MQTT_msgs, keyed by subscription
	// id. Read for completeness of the sweep's cross-check, not as its primary path: a
	// consumer's own filter subject names the tenant, so consumers are swept from the
	// consumer list, which also reaches ones no surviving record mentions.
	Cons map[string]*mqttConsumerRef `json:"cons,omitempty"`
	// PubRel is the session's QoS 2 outbound consumer on $MQTT_out, carrying both the
	// durable name to delete and the subject to purge. Nil for a session that has never
	// completed a QoS 2 delivery, which is the common case.
	PubRel *mqttConsumerRef `json:"pubrel,omitempty"`
}

// mqttConsumerRef is the part of a persisted ConsumerConfig this purge acts on.
type mqttConsumerRef struct {
	Durable       string `json:"durable_name,omitempty"`
	FilterSubject string `json:"filter_subject,omitempty"`
}

// The gateway subject prefixes this purge reads and writes. They are nats-server's, fixed
// in its source rather than configurable, and they are spelled out here for the same
// reason the stream names are in mqtt_store.go: the alternative is scattering them through
// the code that uses them, where a typo becomes a filter that matches nothing.
const (
	// mqttSessSubjectPrefix precedes an optional JetStream domain token and then the
	// digest of the client id. The digest contains no ".", so it is always the last
	// dot-separated token of the subject — which is how mqttSessionIDHash recovers it
	// without needing to know whether a domain is configured.
	mqttSessSubjectPrefix = "$MQTT.sess."
	// mqttMsgsSubjectPrefix wraps an original subject, so a tenant's own subject space
	// appears under it verbatim.
	mqttMsgsSubjectPrefix = "$MQTT.msgs."
	// mqttQoS2InSubjectPrefix is followed by the raw client id as ONE token, then the
	// MQTT packet id.
	mqttQoS2InSubjectPrefix = "$MQTT.qos2.in."
	// mqttPubRelSubjectPrefix is followed by the digest of the client id.
	mqttPubRelSubjectPrefix = "$MQTT.out.pubrel."
)

// MqttGatewayPurgeResult is what one sweep of the client-id-keyed gateway state removed.
type MqttGatewayPurgeResult struct {
	// Sessions is how many $MQTT_sess records were deleted.
	Sessions int64
	// Consumers is how many durable consumers were deleted, across $MQTT_msgs and
	// $MQTT_out.
	Consumers int64
	// Messages is how many parked QoS 2 messages were purged, across $MQTT_qos2in and
	// $MQTT_out.
	Messages int64
	// UnownedSessions counts session records whose client id is not device-shaped at all.
	//
	// 🔑 IT IS NOT A DEFERRAL, AND THE REASONING IS LOAD-BEARING RATHER THAN A JUDGEMENT
	// CALL. With the callout pinning the shape, a session record that is not device-shaped
	// was not filed by a device, so it belongs to no tenant — the edge agent's uplink is
	// the standing example, and it must not appear in a tenant's ledger, let alone block
	// its completion forever. What the count is for is the case that reasoning does NOT
	// cover: a record filed BEFORE the pin, which would be genuine tenant data nobody can
	// attribute. Pre-GA an instance is recreated rather than migrated, so there are none —
	// and a non-zero count here is how that assumption announces it has stopped holding.
	UnownedSessions int64
}

// Total is what the coordinator counts as rows erased.
//
// It sums all three because the settle window restarts on any pass that erased anything,
// and a pass that deleted only a session record has demonstrably just found this tenant's
// data in the broker. Reporting zero for it would measure the window from a moment the
// broker still held that record.
func (r MqttGatewayPurgeResult) Total() int64 { return r.Sessions + r.Consumers + r.Messages }

// PurgeTenantMqttGateway erases the MQTT gateway state that keys on a client id rather
// than on a subject: persistent sessions, their subscription and PUBREL consumers, and
// their parked QoS 2 messages.
//
// It is idempotent. A second call finds no session record, no consumer whose filter names
// the tenant and no parked packet, and reports zero.
//
// # Ordering, and why the session record is deleted LAST
//
// Consumers and parked messages are removed first, and the record that named them only
// once they are gone. A failure part-way therefore leaves a record that still points at
// whatever survived, so the next pass finds it again. The reverse order loses the addresses
// on the way to using them — it would strand the pubrel consumer with nothing left able to
// name it. It is also the order nats-server's own session teardown uses.
//
// # What a live session does about it
//
// Nothing, and it does not need to. The tenant is in `purging`, so the device-plane gate
// already refuses its devices at CONNECT; an already-open connection can lose its consumer
// mid-flight, which is the intended outcome of erasing a deleted tenant, and it cannot
// re-establish one. Any record a still-connected client rewrites is found by the next pass,
// and the settle window is what makes "the last pass found nothing" mean something.
func PurgeTenantMqttGateway(ctx context.Context, nc *nats.Conn, instanceId, tenant string) (MqttGatewayPurgeResult, error) {
	var res MqttGatewayPurgeResult

	// The same two guards the subject purge opens with. The tenant one matters less here —
	// nothing below is a subject filter that could widen — but the value flows into a
	// string prefix that decides what gets DELETED, and a prefix built from an empty token
	// matches every client id in the instance.
	if err := core.ValidateToken(tenant); err != nil {
		return res, fmt.Errorf("refusing to purge the MQTT gateway for tenant %q: %w", tenant, err)
	}
	clientPrefix, err := DeviceClientIDTenantPrefix(instanceId, tenant)
	if err != nil {
		return res, fmt.Errorf("refusing to purge the MQTT gateway: %w", err)
	}
	// The tenant's own subject space, which is what a subscription consumer's filter is
	// built over. Equality is not a case: a filter is always the gateway prefix plus at
	// least the instance and tenant tokens, so there is always more subject after this.
	subjectPrefix := mqttMsgsSubjectPrefix + instanceId + "." + tenant + "."

	js, err := nc.JetStream()
	if err != nil {
		return res, fmt.Errorf("opening JetStream to purge the MQTT gateway for %q: %w", tenant, err)
	}

	// 1. Subscription consumers, swept from the CONSUMER LIST rather than from the session
	//    records that mention them. Their filter subject carries the tenant, so this reaches
	//    every one of them — including a consumer whose session record was lost, which on an
	//    interest-retention stream is the worst of the three residues, because it does not
	//    merely linger: it keeps NEW messages on the deleted tenant's subjects alive.
	consumers, err := deleteConsumersUnder(ctx, nc, js, MqttMessageStore, func(filter string) bool {
		return strings.HasPrefix(filter, subjectPrefix)
	})
	if err != nil {
		return res, err
	}
	res.Consumers += consumers

	// 2. The session records, and everything only they can name.
	sessions, err := readMqttSessions(ctx, js)
	if err != nil {
		return res, err
	}
	for _, s := range sessions {
		if !DeviceClientIDBelongsTo(s.record.ID, clientPrefix) {
			if !DeviceClientIDIsDeviceShaped(s.record.ID) {
				res.UnownedSessions++
			}
			continue
		}

		// The PUBREL consumer and its parked packets. Both addresses come from the record
		// where the server wrote them; the fallback derives the digest from the record's
		// own subject, for a session that never opened a QoS 2 delivery and so has no
		// pubrel block to read. Neither path computes the digest.
		pubRelSubject := ""
		if s.record.PubRel != nil {
			pubRelSubject = s.record.PubRel.FilterSubject
			if durable := s.record.PubRel.Durable; durable != "" {
				deleted, err := deleteConsumer(ctx, js, MqttPubRelStore, durable)
				if err != nil {
					return res, err
				}
				res.Consumers += deleted
			}
		}
		if pubRelSubject == "" {
			if hash := mqttSessionIDHash(s.subject); hash != "" {
				pubRelSubject = mqttPubRelSubjectPrefix + hash
			}
		}
		if pubRelSubject != "" {
			n, err := purgeSubject(ctx, nc, MqttPubRelStore, pubRelSubject)
			if err != nil {
				return res, err
			}
			res.Messages += n
		}

		// A consumer the record names but the filter sweep did not reach. This should find
		// nothing — a subscription consumer's filter always names the tenant — and it is
		// here because "should" is doing the work in that sentence, and the cost of being
		// wrong is an interest-retention consumer left holding a deleted tenant's traffic.
		// DeleteConsumer on an already-deleted consumer is not an error (see deleteConsumer).
		for _, c := range s.record.Cons {
			if c == nil || c.Durable == "" {
				continue
			}
			deleted, err := deleteConsumer(ctx, js, MqttMessageStore, c.Durable)
			if err != nil {
				return res, err
			}
			res.Consumers += deleted
		}

		if err := js.DeleteMsg(MqttSessionStore, s.sequence, nats.Context(ctx)); err != nil {
			// A record a peer replica or the server itself deleted between the read and
			// here is the outcome this wanted, not a failure.
			if errors.Is(err, nats.ErrMsgNotFound) {
				continue
			}
			return res, fmt.Errorf("deleting the MQTT session record for %q at sequence %d: %w",
				s.record.ID, s.sequence, err)
		}
		res.Sessions++
	}

	// 3. Parked inbound QoS 2 packets, swept from the SUBJECT LIST rather than from the
	//    session records. A client id appears in these subjects verbatim, and a clean-session
	//    client leaves packets here while leaving no session record at all — so deriving this
	//    from the records would miss exactly the clients that have nothing else to find.
	messages, err := purgeSubjectsMatching(ctx, nc, js, MqttQoS2InStore, mqttQoS2InSubjectPrefix,
		func(subject string) bool {
			return DeviceClientIDBelongsTo(mqttQoS2InClientID(subject), clientPrefix)
		})
	if err != nil {
		return res, err
	}
	res.Messages += messages

	return res, nil
}

// mqttSession is one $MQTT_sess record with the two things about it that are not in its
// payload: where it is, so it can be deleted, and what its subject is, so the digest it
// was filed under can be recovered.
type mqttSession struct {
	subject  string
	sequence uint64
	record   mqttSessionRecord
}

// readMqttSessions reads every record in $MQTT_sess.
//
// A stream that does not exist holds nothing: nats-server creates the gateway's streams
// lazily, so an instance where no MQTT client has ever connected has none of them.
func readMqttSessions(ctx context.Context, js nats.JetStreamContext) ([]mqttSession, error) {
	info, err := js.StreamInfo(MqttSessionStore, nats.Context(ctx))
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", MqttSessionStore, err)
	}
	if info.State.Msgs == 0 {
		return nil, nil
	}

	// An ORDERED consumer: ephemeral, no acks, cleaned up by Unsubscribe, and flow-
	// controlled by the server so a large stream arrives in batches rather than one
	// round trip per record. Reading is all this needs — the deletes go through the
	// stream API by sequence, not through this subscription.
	// The filter is given explicitly rather than left to the stream's own subject list,
	// which carries the JetStream domain token when one is configured. "$MQTT.sess.>"
	// covers the stream either way, and stating it keeps this read from depending on a
	// deployment setting it has no other reason to know about.
	sub, err := js.SubscribeSync(mqttSessSubjectPrefix+">", nats.OrderedConsumer(),
		nats.BindStream(MqttSessionStore))
	if err != nil {
		return nil, fmt.Errorf("opening a read over %s: %w", MqttSessionStore, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	sessions := make([]mqttSession, 0, info.State.Msgs)
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading %s after %d of about %d records: %w",
				MqttSessionStore, len(sessions), info.State.Msgs, err)
		}
		meta, err := msg.Metadata()
		if err != nil {
			return nil, fmt.Errorf("reading the stream position of an %s record: %w", MqttSessionStore, err)
		}

		var rec mqttSessionRecord
		if err := json.Unmarshal(msg.Data, &rec); err != nil {
			// 🔴 STOP RATHER THAN SKIP. A record this cannot read is a session this purge
			// cannot attribute, and carrying on would report a clean broker over it. The
			// sequence is named because it is the only handle anyone has on it.
			return nil, fmt.Errorf("%s holds a record at sequence %d this platform cannot read, so "+
				"the sessions it names cannot be attributed to a tenant: %w",
				MqttSessionStore, meta.Sequence.Stream, err)
		}
		sessions = append(sessions, mqttSession{
			subject:  msg.Subject,
			sequence: meta.Sequence.Stream,
			record:   rec,
		})

		// The server's own count of what is left, which is what makes this terminate on a
		// fact rather than on a silence. A read that stopped at a timeout instead would
		// report the sessions it happened to get and call the rest absent.
		if meta.NumPending == 0 {
			return sessions, nil
		}
	}
}

// mqttSessionIDHash recovers the digest a session was filed under from its subject.
//
// The subject is "$MQTT.sess." then an optional JetStream domain token then the digest.
// The digest is drawn from an alphabet with no ".", so it is whatever follows the last
// dot — which is why this needs to know neither the digest's length nor whether a domain
// is configured.
//
// 🔴 THIS IS THE ONE PLACE THAT READS A SERVER LAYOUT RATHER THAN A SERVER VALUE, so it is
// the one place a wrong belief about nats-server could survive compilation. It is checked
// against the server's own value by TestMqttSessionIDHashAgreesWithTheServer, which reads
// the digest out of a real broker's pubrel filter subject and compares.
func mqttSessionIDHash(subject string) string {
	if !strings.HasPrefix(subject, mqttSessSubjectPrefix) {
		return ""
	}
	return subject[strings.LastIndex(subject, ".")+1:]
}

// mqttQoS2InClientID recovers the client id from a parked QoS 2 subject, which is
// "$MQTT.qos2.in.{clientID}.{packetID}" with the client id as a single token.
func mqttQoS2InClientID(subject string) string {
	rest, ok := strings.CutPrefix(subject, mqttQoS2InSubjectPrefix)
	if !ok {
		return ""
	}
	id, _, ok := strings.Cut(rest, ".")
	if !ok {
		return ""
	}
	return id
}

// purgeSubjectsMatching purges every subject under prefix that match reports as the
// tenant's, and returns how many messages went.
//
// It enumerates rather than filters because that is the only option: the tenant lives
// INSIDE a subject token here, and a NATS wildcard matches whole tokens only.
func purgeSubjectsMatching(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext,
	stream, prefix string, match func(subject string) bool) (int64, error) {
	info, err := js.StreamInfo(stream, &nats.StreamInfoRequest{SubjectsFilter: prefix + ">"}, nats.Context(ctx))
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("listing the subjects of %s: %w", stream, err)
	}

	var purged int64
	for subject := range info.State.Subjects {
		if err := ctx.Err(); err != nil {
			return purged, fmt.Errorf("scanning the subjects of %s was interrupted: %w", stream, err)
		}
		if !match(subject) {
			continue
		}
		n, err := purgeSubject(ctx, nc, stream, subject)
		if err != nil {
			return purged, err
		}
		purged += n
	}
	return purged, nil
}

// deleteConsumersUnder deletes every durable consumer on stream whose filter subject match
// accepts, and returns how many went.
//
// # 🔴 Why this issues the API request instead of ranging over js.Consumers
//
// nats.go reports that listing over a CHANNEL, and a channel that closes because the API
// request failed is indistinguishable from a stream with no consumers. That is precisely
// the failure this subsystem exists to refuse — "deleted 0" reading identically to "there
// were none" — and the library gives a caller no way to tell them apart.
//
// The obvious patch is to check the yielded count against the consumer count StreamInfo
// reports, and it is WORSE THAN THE HOLE. Consumers come and go on this stream while the
// purge runs: any other tenant's MQTT client disconnecting from a clean session removes one
// mid-listing, so a short read is an ordinary event, and a purge that failed on it would
// fail on every pass on a busy instance while looking like a real problem. It is also a
// guard nothing could ever exercise — a check that cannot be made to fire is a check
// nobody knows is broken.
//
// Asking the API directly removes the choice: a failed request is an error, an empty page
// is an empty page, and paging is explicit. The consumers themselves are decoded into
// nats.ConsumerInfo, so every field name inside one is still the library's; only the
// envelope is spelled out here, and the live test would report zero deletions if that
// envelope were wrong.
func deleteConsumersUnder(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext,
	stream string, match func(filter string) bool) (int64, error) {
	// Confirm the stream exists first, so a lazily-created gateway stream reads as "nothing
	// to sweep" rather than as an API error on the listing.
	if _, err := js.StreamInfo(stream, nats.Context(ctx)); err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s before sweeping its consumers: %w", stream, err)
	}

	matched, err := consumersMatching(ctx, nc, stream, match)
	if err != nil {
		return 0, err
	}

	var deleted int64
	for _, name := range matched {
		if err := ctx.Err(); err != nil {
			return deleted, fmt.Errorf("sweeping the consumers of %s was interrupted: %w", stream, err)
		}
		n, err := deleteConsumer(ctx, js, stream, name)
		if err != nil {
			return deleted, err
		}
		deleted += n
	}
	return deleted, nil
}

// consumersMatching pages through a stream's consumers and returns the names of those whose
// filter subject match accepts.
func consumersMatching(ctx context.Context, nc *nats.Conn, stream string,
	match func(filter string) bool) ([]string, error) {
	var names []string
	for offset := 0; ; {
		payload, err := json.Marshal(struct {
			Offset int `json:"offset"`
		}{Offset: offset})
		if err != nil {
			return nil, err
		}
		msg, err := nc.RequestWithContext(ctx, "$JS.API.CONSUMER.LIST."+stream, payload)
		if err != nil {
			return nil, fmt.Errorf("listing the consumers of %s: %w", stream, err)
		}

		var resp struct {
			Error *struct {
				ErrCode     uint16 `json:"err_code"`
				Description string `json:"description"`
			} `json:"error"`
			Total     int                  `json:"total"`
			Consumers []*nats.ConsumerInfo `json:"consumers"`
		}
		if err := json.Unmarshal(msg.Data, &resp); err != nil {
			return nil, fmt.Errorf("reading the consumer listing for %s: %w", stream, err)
		}
		if resp.Error != nil {
			if resp.Error.ErrCode == streamNotFoundErrCode {
				return nil, nil
			}
			return nil, fmt.Errorf("listing the consumers of %s: %s", stream, resp.Error.Description)
		}

		for _, c := range resp.Consumers {
			if c != nil && match(c.Config.FilterSubject) {
				names = append(names, c.Name)
			}
		}
		// An empty page terminates as well as reaching the total. Without it a server that
		// reports a total it cannot page to — which a concurrent delete produces — spins
		// here forever, holding the purge coordinator's advisory lock.
		offset += len(resp.Consumers)
		if len(resp.Consumers) == 0 || offset >= resp.Total {
			return names, nil
		}
	}
}

// deleteConsumer removes one durable consumer, reporting 1 if it was there and 0 if it was
// not. A consumer that is already gone is the state this wanted, not a failure — the
// session teardown inside nats-server races this sweep by design, and both are trying to
// delete the same thing.
func deleteConsumer(ctx context.Context, js nats.JetStreamContext, stream, name string) (int64, error) {
	err := js.DeleteConsumer(stream, name, nats.Context(ctx))
	if err == nil {
		return 1, nil
	}
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return 0, nil
	}
	return 0, fmt.Errorf("deleting consumer %q on %s: %w", name, stream, err)
}
