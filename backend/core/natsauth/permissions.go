// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"fmt"
	"slices"
	"strings"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// This file GENERATES the broker permissions of every platform login from the declarations the
// services are already held to: core/streams (who writes, reads and administers each stream, and
// under which consumer name), core/kv (who writes and reads each bucket) and the control-subject
// builders in core/messaging. dcctl calls AreaPermissions at render time and hands the broker the
// result verbatim, and every area's permission test configures its embedded broker from the same
// call, so there is no second description of what an area may do to keep in step with the first.
// A declaration drives the in-process refusal (messaging/catalog.go), the test broker and the
// real broker.
//
// 🔴 WHAT A GRANT IS EXACT ABOUT, AND WHAT IT IS NOT. A stream or bucket is named, never
// wildcarded; a durable consumer is named exactly (the name is derived from the same declaration
// that creates it), so one area cannot rewrite, steal from or ack another's. A writer's publish
// subjects are its streams' own subjects. What it is NOT yet exact about is administration: until
// the stream-administration step moves stream creation to one dedicated login, an area keeps
// STREAM.CREATE and STREAM.UPDATE on the streams it opens, and an EPHEMERAL reader keeps a
// wildcard consumer name on its stream (the client library names the consumer). Both are listed
// in the residue the design records, not hidden here.

// jsAPI is the JetStream API subject prefix every administrative request is published under.
const jsAPI = "$JS.API."

// Rules is one direction's allow and deny lists, in the shape nats-server's configuration reads.
type Rules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// Permissions is one login's permission block, serialised exactly as nats-server's configuration
// (and the nats chart's config.merge) reads it. The lists are sorted and de-duplicated, so the
// same inputs always produce the same bytes and a diff of two renders is a diff of the grants.
type Permissions struct {
	Publish   *Rules `json:"publish,omitempty"`
	Subscribe *Rules `json:"subscribe,omitempty"`
	// AllowResponses lets the login publish ONE reply to a message it received that carried a
	// reply subject, for a bounded time, with no publish grant on the reply subject itself. It is
	// how a responder (the device callout, the session-kick and tenant-purge handlers, a census)
	// answers a requester's per-area inbox without being allowed to publish into any inbox.
	AllowResponses bool `json:"allow_responses,omitempty"`
}

// grant accumulates one login's lists.
type grant struct {
	pub, sub map[string]struct{}
}

func newGrant() *grant {
	return &grant{pub: map[string]struct{}{}, sub: map[string]struct{}{}}
}

func (g *grant) publish(subjects ...string) {
	for _, s := range subjects {
		g.pub[s] = struct{}{}
	}
}

func (g *grant) subscribe(subjects ...string) {
	for _, s := range subjects {
		g.sub[s] = struct{}{}
	}
}

// api grants publish on "$JS.API.<tail>" for each tail.
func (g *grant) api(tails ...string) {
	for _, t := range tails {
		g.pub[jsAPI+t] = struct{}{}
	}
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// permissions finishes the grant. The device-events deny rides on every login, as it did on the
// shared one: nothing the platform writes is in that namespace, and an allow-list that one day
// grew a wildcard over it must not become a way to forge a device.
func (g *grant) permissions(responses bool) Permissions {
	return Permissions{
		Publish:        &Rules{Allow: sortedKeys(g.pub), Deny: ServicePublishDeny()},
		Subscribe:      &Rules{Allow: sortedKeys(g.sub)},
		AllowResponses: responses,
	}
}

// Areas that hold a role no catalog can express because the subject is not a stream's. Each one
// is named here, next to its reason, and the grant follows from the NAME of the area, so an area
// that is renamed breaks the build of the test that pins it rather than the broker.
const (
	// calloutArea answers the broker's device authorization requests.
	calloutArea = "device-management"
	// detectPurgeResponderArea evaluates tenant eviction requests for the DETECT engine; the
	// requester is detectPurgeRequesterArea.
	detectPurgeResponderArea = "event-processing"
	detectPurgeRequesterArea = "user-management"
	// sessionKickResponderArea owns the system-account login that ends a device's session; the
	// requester, which asks it to when a credential is revoked, is sessionKickRequesterArea.
	sessionKickResponderArea = "event-sources"
	sessionKickRequesterArea = "device-management"
	// mqttGatewayArea administers the MQTT gateway's message store (the fail-closed loop).
	mqttGatewayArea = "event-sources"
	// mqttBackoffArea answers device connects and backs them off while the gateway's message
	// store is deep: it reads the store's depth.
	mqttBackoffArea = "device-management"
	// tenantPurgeArea erases a deleted tenant's data across every stream and bucket.
	tenantPurgeArea = "user-management"
)

// AuthCalloutRequestSubject is the subject nats-server publishes a device authorization request
// on, in the callout's account.
const AuthCalloutRequestSubject = "$SYS.REQ.USER.AUTH"

// AreaPermissions is the broker permission block of one area's login in the platform account.
//
// It fails for an area that holds no login (mcp, ai-inference: neither opens a broker
// connection) and for an instance id that is not a single subject token, because the instance id
// is spliced into every subject and stream name below.
func AreaPermissions(instanceId, area string) (Permissions, error) {
	if err := checkInstanceToken(instanceId); err != nil {
		return Permissions{}, err
	}
	if !config.NeedsNatsLogin(area) {
		return Permissions{}, fmt.Errorf("natsauth: area %q holds no broker login, so it has no permissions to generate", area)
	}
	g := newGrant()

	// Replies. The area's connections use "_INBOX_<area>" (messaging.InboxPrefix); pull fetches,
	// request replies, KV reads and push deliveries all arrive there, and nowhere else is readable.
	g.subscribe(messaging.InboxPrefix(area) + ".>")

	for _, st := range streams.All {
		streamGrant(g, instanceId, area, st)
	}
	for _, b := range kv.All {
		bucketGrant(g, instanceId, area, b)
	}
	controlGrant(g, instanceId, area)
	exceptionGrant(g, instanceId, area)

	// Every area may answer a request it received (a census, a purge, a kick, an authorization):
	// the reply goes to the requester's own inbox through allow_responses, not through a grant on
	// any inbox.
	return g.permissions(true), nil
}

// checkInstanceToken refuses an instance id that would not be one token of a subject.
func checkInstanceToken(instanceId string) error {
	if instanceId == "" || strings.ContainsAny(instanceId, ".*> \t\r\n") {
		return fmt.Errorf("natsauth: instance id %q is not a single subject token", instanceId)
	}
	return nil
}

// streamGrant adds what area may do to one declared stream.
func streamGrant(g *grant, instance, area string, st streams.Stream) {
	var mine []streams.Reader
	for _, r := range streams.ReadersOf(st.Suffix) {
		if r.Area == area {
			mine = append(mine, r)
		}
	}
	writes := streams.Writes(st.Suffix, area)
	admin := streams.Administers(st.Suffix, area)
	if !writes && !admin && len(mine) == 0 {
		return
	}
	name := messaging.StreamName(instance, st.Suffix)

	// Looking at the stream: ensureStream reads it before it creates or reconciles it, the
	// sampler and the writers' backpressure read its state.
	g.api("STREAM.INFO." + name)

	if admin {
		g.api("STREAM.CREATE."+name, "STREAM.UPDATE."+name)
		if st.RecreateOnRetentionChange {
			// A retention change is deleted and re-added once the single reader has drained it.
			g.api("STREAM.DELETE." + name)
		}
	}

	if writes {
		g.publish(messaging.StreamSubjects(instance, st.Suffix)...)
		// A writer follows its stream's leader by listening for the election advisory.
		g.subscribe("$JS.EVENT.ADVISORY.STREAM.LEADER_ELECTED." + name)
		// A stream that refuses its writers when a reader falls behind is gated by that reader's
		// backlog, which the writer reads off the reader's durable: state only, the writer cannot
		// fetch from or change it.
		if streams.AppliesBackpressure(st.Suffix) {
			for _, reader := range streams.BackpressureReadersFor(st.Suffix) {
				g.api("CONSUMER.INFO." + name + "." + messaging.DurableName(instance, reader, st.Suffix))
			}
		}
	}

	for _, r := range mine {
		switch r.Mode {
		case streams.ReadDurable, streams.ReadAckFloor:
			for _, d := range messaging.DeclaredDurables(instance, st.Suffix, r) {
				durableVerbs(g, name, d)
			}
			if st.Shape != streams.ShapeAdvisory {
				// The max-delivery recorder fetches the original of a message whose deliveries
				// ran out, from the stream the area reads, and removes it from a work-queue stream
				// once its letter is written.
				g.api("STREAM.MSG.GET."+name, "DIRECT.GET."+name, "DIRECT.GET."+name+".>")
				if st.Retention == streams.RetentionWorkQueue {
					g.api("STREAM.MSG.DELETE." + name)
				}
			}
		case streams.ReadEphemeral:
			// The client library names an ephemeral consumer, so its name cannot be granted
			// exactly. The stream-administration step replaces these reads with reads by sequence.
			g.api(
				"CONSUMER.CREATE."+name+".>",
				"CONSUMER.INFO."+name+".*",
				"CONSUMER.DELETE."+name+".*",
				"CONSUMER.MSG.NEXT."+name+".*",
				"STREAM.MSG.GET."+name,
				"DIRECT.GET."+name,
				"DIRECT.GET."+name+".>",
			)
			g.publish("$JS.ACK." + name + ".*.>")
		case streams.ReadLive:
			g.subscribe(messaging.StreamSubject(instance, st.Suffix))
		}
	}
}

// durableVerbs grants exactly the verbs a named pull consumer needs on its stream: create or
// update it (with or without the filter subject in the request, and by the legacy durable verb),
// read its state, fetch from it, acknowledge on it, delete it (the ack-floor recreate), and ask
// its leader to move.
func durableVerbs(g *grant, stream, durable string) {
	g.api(
		"CONSUMER.CREATE."+stream+"."+durable,
		"CONSUMER.CREATE."+stream+"."+durable+".>",
		"CONSUMER.DURABLE.CREATE."+stream+"."+durable,
		"CONSUMER.INFO."+stream+"."+durable,
		"CONSUMER.DELETE."+stream+"."+durable,
		"CONSUMER.MSG.NEXT."+stream+"."+durable,
		"CONSUMER.LEADER.STEPDOWN."+stream+"."+durable,
	)
	g.publish("$JS.ACK." + stream + "." + durable + ".>")
}

// bucketGrant adds what area may do to one declared KV bucket in the platform account.
//
// A bucket is a stream (KV_<name>) and a key write is a publish into its subjects, so the writers
// list is a publish grant: whoever may put into device-by-token decides which device an event
// resolves as.
func bucketGrant(g *grant, instance, area string, b kv.Bucket) {
	for _, bucket := range messaging.KvBucketNames(instance, b, area) {
		stream := messaging.KvStreamName(bucket)
		// The client library's bucket lookup reads the account's JetStream limits first.
		g.api("INFO")
		g.api("STREAM.INFO."+stream, "STREAM.CREATE."+stream, "STREAM.UPDATE."+stream)
		if kv.Writes(b.Name, area) {
			g.publish("$KV." + bucket + ".>")
		}
		if kv.Reads(b.Name, area) {
			// A read is a direct get; a watch, a key listing and a history are an ordered
			// consumer the client creates, names and deletes itself.
			g.api(
				"STREAM.MSG.GET."+stream,
				"DIRECT.GET."+stream,
				"DIRECT.GET."+stream+".>",
				"CONSUMER.CREATE."+stream+".>",
				"CONSUMER.INFO."+stream+".*",
				"CONSUMER.DELETE."+stream+".*",
			)
		}
	}
}

// controlGrant adds the core-NATS subjects outside every stream, each built by the code that uses
// it.
func controlGrant(g *grant, instance, area string) {
	// Cross-replica cache eviction: an area that keeps a cache bucket broadcasts evictions for it
	// on its own subject and listens there.
	for _, b := range kv.All {
		if b.Tier == kv.Cache && b.Account == kv.App && slices.Contains(b.Admins(), area) {
			s := messaging.CacheEvictSubjects(instance, area)
			g.publish(s)
			g.subscribe(s)
			break
		}
	}
	// The consumer-leader census: every area with a durable reader asks its own replicas which
	// server they are attached to, and answers.
	if slices.Contains(streams.RecorderAreas(), area) {
		s := messaging.CensusSubject(instance, area)
		g.publish(s)
		g.subscribe(s)
	}
	if area == calloutArea {
		g.subscribe(AuthCalloutRequestSubject)
	}
	if area == detectPurgeResponderArea {
		g.subscribe(messaging.DetectPurgeSubject(instance))
	}
	if area == detectPurgeRequesterArea {
		g.publish(messaging.DetectPurgeSubject(instance))
	}
	if area == sessionKickResponderArea {
		g.subscribe(messaging.SessionKickSubject(instance))
	}
	if area == sessionKickRequesterArea {
		g.publish(messaging.SessionKickSubject(instance))
	}
}

// exceptionGrant adds the two named exceptions to "an area touches what the catalog declares for
// it". Each is something an area does to the broker that no catalog entry describes, kept here,
// next to its reason, so that widening one is a visible edit to this function.
func exceptionGrant(g *grant, instance, area string) {
	switch area {
	case mqttGatewayArea:
		// EVENT-SOURCES OWNS THE MQTT GATEWAY'S STORES. The gateway's streams are created and named
		// by nats-server, so no catalog lists them; event-sources bounds them (ReconcileMqttStores)
		// and runs the fail-closed loop on the message store, which is a runtime state machine
		// rather than a reconcile: it marks and unmarks the stream (UPDATE), creates the stream when
		// the gateway has not yet (CREATE), and creates, moves, drains and deletes its three
		// durables there, reading the messages it holds to forward them. That is stream UPDATE and
		// consumer verbs on streams that carry device traffic, and it is no wider than what the
		// area already does as the authenticated transport that writes inbound-events.
		for _, s := range messaging.MqttStoreNames() {
			g.api("STREAM.INFO."+s, "STREAM.UPDATE."+s)
		}
		g.api("STREAM.CREATE."+messaging.MqttMessageStore,
			"STREAM.MSG.GET."+messaging.MqttMessageStore,
			"DIRECT.GET."+messaging.MqttMessageStore,
			"STREAM.NAMES")
		for _, d := range messaging.MqttFailClosedConsumerNames(instance) {
			durableVerbs(g, messaging.MqttMessageStore, d)
		}
	case mqttBackoffArea:
		// Reading the depth of the MQTT message store (the connect back-off). State only.
		g.api("STREAM.INFO." + messaging.MqttMessageStore)
	case tenantPurgeArea:
		// USER-MANAGEMENT ERASES A DELETED TENANT. It purges the tenant's subjects from every
		// stream the platform declares (and the gateway's subject-carrying stores), deletes the
		// gateway consumers that name the tenant, reads and deletes its session records, and
		// removes its keys from the cache buckets of the areas that keep them. PURGE is the
		// destructive verb the design reserves for this one area: it can delete data, not forge it.
		// A tenant's keys are removed by purging their subjects (never by a KV write), so the area
		// holds no publish grant on any bucket it does not own.
		for _, st := range streams.All {
			if st.Shape == streams.ShapeAdvisory {
				continue
			}
			g.api("STREAM.PURGE." + messaging.StreamName(instance, st.Suffix))
		}
		for _, stream := range messaging.TenantScopedBucketStreams(instance) {
			g.api("STREAM.INFO."+stream, "STREAM.PURGE."+stream)
		}
		// ...and tells the replicas that cache them to let go of the tenant's entries.
		g.publish(messaging.TenantCacheEvictionSubjects(instance))
		for _, s := range []string{messaging.MqttMessageStore, messaging.MqttRetainedStore, messaging.MqttPubRelStore, messaging.MqttQoS2InStore} {
			g.api("STREAM.INFO."+s, "STREAM.PURGE."+s)
		}
		for _, s := range []string{messaging.MqttMessageStore, messaging.MqttPubRelStore} {
			g.api("CONSUMER.LIST."+s, "CONSUMER.DELETE."+s+".*")
		}
		g.api("STREAM.INFO."+messaging.MqttSessionStore,
			"STREAM.MSG.DELETE."+messaging.MqttSessionStore,
			"CONSUMER.CREATE."+messaging.MqttSessionStore+".>",
			"CONSUMER.INFO."+messaging.MqttSessionStore+".*",
			"CONSUMER.DELETE."+messaging.MqttSessionStore+".*")
		// An earlier release kept the sign-in buckets in the shared account; retiring those copies
		// reads and deletes them.
		for _, bucket := range messaging.IdentityBucketNames(instance) {
			stream := messaging.KvStreamName(bucket)
			g.api("STREAM.INFO."+stream, "STREAM.DELETE."+stream)
		}
	}
}

// ServicePermissions is the block of the shared `dc_service` login, retained on an upgraded
// instance so pods that have not yet rolled onto their own login stay connected: no allow-list, a
// publish deny on the device-events namespace. It is retired by a later upgrade.
func ServicePermissions() Permissions {
	return Permissions{Publish: &Rules{Deny: ServicePublishDeny()}}
}

// AdminPermissions is the block of the broker-administration login. Nothing mounts its Secret
// yet, so it keeps the shared login's permissions; the stream-administration step that mounts it
// narrows it to stream and bucket administration.
func AdminPermissions() Permissions { return ServicePermissions() }

// SysPermissions is the block of the system-account login the broker-presence tap holds.
func SysPermissions() Permissions {
	return Permissions{
		Publish:   &Rules{Allow: sortedCopy(SysPublishAllow())},
		Subscribe: &Rules{Allow: sortedCopy(SysSubscribeAllow())},
	}
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// ServiceUsers is every static login's permissions for one instance, by account then username:
// the shared login, the broker administrator's, each area's own login, and the system-account
// login. It is the one value dcctl hands the broker's renderer (as JSON) and the one value a test
// broker is configured from. The identity account's login has no entry: the account is its own
// boundary and the login is held to nothing inside it.
func ServiceUsers(instanceId string) (map[string]map[string]Permissions, error) {
	return ServiceUsersFor(instanceId, config.NatsLoginAreas())
}

// ServiceUsersFor is ServiceUsers restricted to the given areas' logins, for an installation that
// deploys only some areas (`dcctl broker permissions --profile`). An area that holds no login
// (mcp, ai-inference) is named in the answer by its absence, not refused: a profile lists them.
func ServiceUsersFor(instanceId string, areas []string) (map[string]map[string]Permissions, error) {
	if err := checkInstanceToken(instanceId); err != nil {
		return nil, err
	}
	app := map[string]Permissions{
		ServiceUser: ServicePermissions(),
		AdminUser:   AdminPermissions(),
	}
	for _, area := range areas {
		if !config.NeedsNatsLogin(area) {
			continue
		}
		p, err := AreaPermissions(instanceId, area)
		if err != nil {
			return nil, err
		}
		app[LoginUser(area)] = p
	}
	return map[string]map[string]Permissions{
		AppAccount:    app,
		SystemAccount: {SysUser: SysPermissions()},
	}, nil
}
