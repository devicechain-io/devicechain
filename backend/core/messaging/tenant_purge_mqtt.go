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
	"github.com/rs/zerolog/log"
)

// Erasing one tenant's MQTT GATEWAY state (ADR-077) — the half a subject filter cannot
// reach, and the reason the broker store used to be unable to report clean.
//
// # 🔑 Everything here is addressed by READING, and the two exceptions are named
//
// The gateway's streams key on an MQTT client id, or on a digest of one, so none of them
// answers a tenant-scoped subject filter (mqtt_clientid.go explains why in full, and
// corrects the claim that pinning the id made them computable — it did not). What the pin
// bought is ATTRIBUTION, and attribution is enough, because the broker writes down almost
// every address this needs:
//
//   - the client id, in the $MQTT_sess record's own PAYLOAD, which is what makes an
//     otherwise anonymous record nameable;
//   - a subscription consumer's filter, on the CONSUMER, as "$MQTT.msgs.{instance}.{tenant}…";
//   - a PUBREL consumer's filter, on the CONSUMER, as "$MQTT.out.pubrel.{digest}";
//   - the parked packets, under subjects the streams enumerate.
//
// So nothing resembling the server's getHash is reimplemented. Two SUBJECT LAYOUTS are
// read rather than values, and both are called out because a wrong belief about another
// project's layout compiles perfectly and then silently matches nothing:
//
//   - mqttSessionIDHash — the digest in a session's subject. Cross-checked against the
//     server's own digest by TestMqttSessionIDHashAgreesWithTheServer, in both JetStream
//     domain modes, because the domain-less case cannot distinguish a wrong derivation.
//   - mqttQoS2InClientID — the client id in a parked packet's subject. Cross-checked by
//     TestParkedQoS2SubjectsCarryTheClientIdVerbatim, which provokes a REAL parked packet
//     out of a real broker rather than seeding one.
//
// # Why this reads the whole session stream
//
// There is no filter that selects one tenant's sessions, so the scan is total, exactly as
// the key-value purge's is. It is read in BATCHES through one ordered consumer rather than
// one request per record, and that is a liveness requirement, not a throughput preference:
// a scan that cannot finish inside PurgeTimeout restarts from the beginning on the next
// pass and times out in the same place, forever. Multi-pass idempotency does not rescue a
// scan whose progress is not durable.

// mqttSessionRecord is the subset of nats-server's mqttPersistedSession this purge reads,
// which is one field: the client id, because attribution is the only thing the record is
// needed for. Every address is taken from the consumer or subject that carries it.
//
// 🔴 IT IS DELIBERATELY A LOCAL SNAPSHOT, NOT AN IMPORT. core/messaging does not depend on
// nats-server outside its tests and must not start: the server is a deployment artifact
// here, not a library. Declaring the shape locally is the same discipline a migration's
// snapshot structs follow — it describes the bytes on the wire at a point in time, and
// should fail visibly if the server changes them rather than silently track a struct that
// moved underneath it.
//
// encoding/json ignores what it does not recognise, which is correct — a server that ADDS
// a field must not break the erasure — but it also means a server that RENAMED "id" would
// leave every record unattributable and the purge would erase nothing while reporting
// success. TestMqttSessionRecordMatchesWhatTheServerWrites reads a record a real broker
// wrote, which is the only thing standing between that and silence.
type mqttSessionRecord struct {
	ID string `json:"id,omitempty"`
}

// The gateway subject prefixes this purge reads and writes. They are nats-server's, fixed
// in its source rather than configurable, and they are spelled out here for the same
// reason the stream names are in mqtt_store.go: the alternative is scattering them through
// the code that uses them, where a typo becomes a filter that matches nothing.
//
// TestMqttGatewaySubjectPrefixesAreTheServersOwn checks each against the subjects the
// server declared on the stream it created.
const (
	// mqttSessSubjectPrefix precedes an optional JetStream domain token and then the
	// digest of the client id. The digest's alphabet has no ".", so it is always the last
	// dot-separated token — which is how mqttSessionIDHash recovers it without needing to
	// know whether a domain is configured.
	mqttSessSubjectPrefix = "$MQTT.sess."
	// mqttMsgsSubjectPrefix wraps an original subject, so a tenant's own subject space
	// appears under it verbatim.
	mqttMsgsSubjectPrefix = "$MQTT.msgs."
	// mqttQoS2InSubjectPrefix is followed by the raw client id as ONE token, then the
	// MQTT packet id.
	mqttQoS2InSubjectPrefix = "$MQTT.qos2.in."
	// mqttPubRelSubjectPrefix is followed by the digest of the client id, and by nothing
	// else — unlike the session subject, it carries no domain token.
	mqttPubRelSubjectPrefix = "$MQTT.out.pubrel."
)

// MqttGatewayPurgeResult is what one sweep of the client-id-keyed gateway state removed.
type MqttGatewayPurgeResult struct {
	// Sessions is how many $MQTT_sess records were deleted.
	Sessions int64
	// Consumers is how many durable consumers were deleted, across $MQTT_msgs and $MQTT_out.
	Consumers int64
	// Messages is how many parked QoS 2 messages were purged, across $MQTT_qos2in and
	// $MQTT_out.
	Messages int64

	// UnownedSessions counts session records whose client id is not device-shaped, so no
	// tenant can be named for them and this purge left them alone.
	//
	// # 🔴 It is NOT a deferral, and getting that wrong deadlocks every purge on the instance
	//
	// An earlier draft made it one, reasoning that a non-device-shaped record "belongs to
	// no tenant" because the callout pins the shape — and then deferred on it anyway, which
	// is what a deferral does: it blocks completion until it goes away. It never goes away.
	// nats-server saves a session record on EVERY connect, clean sessions included ("we save
	// always in case we are running in cluster mode"), and clears it only at disconnect. So
	// the edge agent's uplink — a permanently-connected clean-session client whose id is
	// "dc-edge-agent-{instance}-{agent}" — holds a live record at all times, and on any
	// ADR-068 topology every tenant's purge would have deferred on it forever. That is the
	// exact never-completes condition this file exists to remove, reintroduced by the
	// comment claiming it could not happen.
	//
	// # 🔑 Why counting without deferring is the honest answer, not the convenient one
	//
	// The question a deferral answers is "can this survive and hurt someone". For gateway
	// state the harm is INHERITANCE: a successor tenant at a reused token whose device
	// presents the same client id takes over the predecessor's session. That requires the
	// successor's id to EQUAL the predecessor's — and a successor's device id is
	// device-shaped, because the callout refuses anything else. A record that is NOT
	// device-shaped therefore cannot be inherited by any device, whatever tenant filed it.
	// The pin closes that path by itself, without erasure.
	//
	// What is left is a record that is not a disclosure risk and has no operator remedy —
	// so it is counted and logged, where someone can see the assumption stop holding,
	// rather than blocking an erasure it does not endanger.
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
// the tenant or one of its digests, and no parked packet, and reports zero.
//
// # Ordering, and why the session records are deleted LAST
//
// A session record is the only thing that ties a tenant to a DIGEST, and the digest is the
// only way to reach that session's PUBREL consumer and parked packets. Delete the records
// first and a pass that then fails has thrown away the addresses on its way to using them,
// with nothing left able to name what it stranded. So the records go last, after everything
// they can name is gone, which is also the order nats-server's own session teardown uses.
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

	// The one guard, and it covers both fields: an invalid instance id or tenant is refused
	// here rather than composed into a prefix that decides what gets DELETED. A tenant token
	// carrying ":" would compose a prefix spanning two fields and select another tenant's
	// sessions; an empty one would match every client id in the instance.
	clientPrefix, err := deviceClientIDTenantPrefix(instanceId, tenant)
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
	//    interest-retention stream is the worst residue of the lot: it does not merely
	//    linger, it keeps NEW messages on the deleted tenant's subjects alive.
	consumers, err := deleteConsumersUnder(ctx, nc, js, MqttMessageStore, func(filter string) bool {
		return strings.HasPrefix(filter, subjectPrefix)
	})
	if err != nil {
		return res, err
	}
	res.Consumers += consumers

	// 2. The session records — read now, deleted at the end. This is where attribution
	//    happens and where the tenant's digests come from.
	sessions, err := readMqttSessions(ctx, js)
	if err != nil {
		return res, err
	}
	victims := make([]mqttSession, 0, len(sessions))
	digests := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		if strings.HasPrefix(s.record.ID, clientPrefix) {
			victims = append(victims, s)
			if h := mqttSessionIDHash(s.subject); h != "" {
				digests[h] = true
			}
			continue
		}
		// Not this tenant's. That covers two different things, and only the second is worth
		// counting: another TENANT's device, which its own purge will reach, and a record no
		// tenant owns at all, which nobody's ever will.
		if !deviceClientIDIsDeviceShaped(s.record.ID) {
			res.UnownedSessions++
		}
	}

	if len(digests) > 0 {
		// 3. The PUBREL consumers of those sessions, matched on the digest in their own
		//    filter subject. Sweeping them the same way as the subscription consumers — from
		//    the listing rather than from a record — is what reaches one whose session record
		//    was lost, and it means the server's durable-name convention is never rebuilt here.
		pubRelConsumers, err := deleteConsumersUnder(ctx, nc, js, MqttPubRelStore, func(filter string) bool {
			return digests[pubRelSubjectIDHash(filter)]
		})
		if err != nil {
			return res, err
		}
		res.Consumers += pubRelConsumers

		// 4. Their parked outbound PUBREL packets.
		pubRelMessages, err := purgeSubjectsMatching(ctx, nc, js, MqttPubRelStore, mqttPubRelSubjectPrefix,
			func(subject string) bool { return digests[pubRelSubjectIDHash(subject)] })
		if err != nil {
			return res, err
		}
		res.Messages += pubRelMessages
	}

	// 5. Parked INBOUND QoS 2 packets, swept from the subject list rather than from the
	//    session records. A client id appears in these subjects verbatim, and a client that
	//    abandons a QoS 2 handshake can leave packets here under an id whose session record
	//    has since been cleared — so deriving this from the records would miss exactly the
	//    clients that leave nothing else behind.
	messages, err := purgeSubjectsMatching(ctx, nc, js, MqttQoS2InStore, mqttQoS2InSubjectPrefix,
		func(subject string) bool {
			return strings.HasPrefix(mqttQoS2InClientID(subject), clientPrefix)
		})
	if err != nil {
		return res, err
	}
	res.Messages += messages

	// 6. And only now the records themselves.
	for _, s := range victims {
		if err := js.DeleteMsg(MqttSessionStore, s.sequence, nats.Context(ctx)); err != nil {
			// A record a peer replica or the server itself deleted between the read and here
			// is the outcome this wanted, not a failure.
			if errors.Is(err, nats.ErrMsgNotFound) {
				continue
			}
			return res, fmt.Errorf("deleting the MQTT session record for %q at sequence %d: %w",
				s.record.ID, s.sequence, err)
		}
		res.Sessions++
	}

	if res.UnownedSessions > 0 {
		// Warn rather than defer — see the field's own comment for why deferring here
		// deadlocks every purge on any instance running an edge agent.
		log.Warn().Str("tenant", tenant).Int64("sessions", res.UnownedSessions).
			Msg("MQTT session records could not be attributed to any tenant and were left alone. " +
				"Expected for the platform's own non-device clients; a record filed by a DEVICE " +
				"before the client id was pinned would look the same and would be tenant data.")
	}
	return res, nil
}

// mqttSession is one $MQTT_sess record with the two things about it that are not in its
// payload: where it is, so it can be deleted, and what its subject is, so the digest it was
// filed under can be recovered.
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
	// controlled by the server so a large stream arrives in batches rather than one round
	// trip per record. Reading is all this needs — the deletes go through the stream API by
	// sequence, not through this subscription.
	//
	// The filter is given explicitly rather than left to the stream's own subject list,
	// which carries the JetStream domain token when one is configured. "$MQTT.sess.>" covers
	// the stream either way, and stating it keeps this read from depending on a deployment
	// setting it has no other reason to know about.
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
// The subject is "$MQTT.sess." then an OPTIONAL JetStream domain token then the digest.
// The digest's alphabet has no ".", so it is whatever follows the last dot — which is why
// this needs to know neither the digest's length nor whether a domain is configured.
func mqttSessionIDHash(subject string) string {
	if !strings.HasPrefix(subject, mqttSessSubjectPrefix) {
		return ""
	}
	return subject[strings.LastIndex(subject, ".")+1:]
}

// pubRelSubjectIDHash recovers the digest from a PUBREL subject or consumer filter, which
// is "$MQTT.out.pubrel.{digest}" and carries no domain token.
func pubRelSubjectIDHash(subject string) string {
	hash, ok := strings.CutPrefix(subject, mqttPubRelSubjectPrefix)
	if !ok {
		return ""
	}
	return hash
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
// It enumerates rather than filters because that is the only option: what identifies the
// owner lives INSIDE a subject token here, and a NATS wildcard matches whole tokens only.
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
// # 🔴 Why this asks the API rather than ranging over js.Consumers
//
// nats.go reports that listing over a CHANNEL, and a channel that closes because the API
// request failed is indistinguishable from a stream with no consumers — "deleted 0" reading
// identically to "there were none", which is the purge failure this subsystem exists to
// refuse. The obvious patch, comparing the yielded count against the one StreamInfo reports,
// is worse: consumers legitimately come and go on this stream while a purge runs, so a short
// read is an ordinary event, and it is a guard nothing could ever make fire. Asking the API
// removes the choice — a failed request is an error and an empty page is an empty page.
//
// The consumers themselves decode into nats.ConsumerInfo, so every field name inside one is
// still the library's; only the envelope is spelled out here.
func deleteConsumersUnder(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext,
	stream string, match func(filter string) bool) (int64, error) {
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
// filter subject match accepts. A stream that does not exist has none — the gateway creates
// its streams lazily, so an instance that has never used MQTT has neither.
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
			Error     *jsAPIError          `json:"error"`
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
		// reports a total it cannot page to — which a concurrent delete produces — spins here
		// forever, holding the purge coordinator's advisory lock.
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
