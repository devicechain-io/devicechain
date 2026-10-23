// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

const permInstance = "inst1"

// subjectMatches is NATS subject matching for a literal subject against a permission pattern:
// tokens equal, "*" for one token, ">" for the rest.
func subjectMatches(pattern, subject string) bool {
	p, s := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i, tok := range p {
		if tok == ">" {
			return len(s) > i
		}
		if i >= len(s) {
			return false
		}
		if tok != "*" && tok != s[i] {
			return false
		}
	}
	return len(p) == len(s)
}

// allows reports whether the permissions let the login publish to the literal subject: matched
// by an allow entry and by no deny entry.
func (p Permissions) publishes(subject string) bool {
	if p.Publish == nil {
		return false
	}
	for _, d := range p.Publish.Deny {
		if subjectMatches(d, subject) {
			return false
		}
	}
	for _, a := range p.Publish.Allow {
		if subjectMatches(a, subject) {
			return true
		}
	}
	return false
}

func (p Permissions) subscribes(subject string) bool {
	if p.Subscribe == nil {
		return false
	}
	for _, a := range p.Subscribe.Allow {
		if subjectMatches(a, subject) {
			return true
		}
	}
	return false
}

func allAreaPermissions(t *testing.T) map[string]Permissions {
	t.Helper()
	out := map[string]Permissions{}
	for _, area := range config.NatsLoginAreas() {
		p, err := AreaPermissions(permInstance, area)
		require.NoError(t, err, area)
		out[area] = p
	}
	return out
}

func TestEveryLoginAreaHasGeneratedPermissionsAndNoOtherDoes(t *testing.T) {
	all := allAreaPermissions(t)
	require.Len(t, all, len(config.NatsLoginAreas()))
	for _, exempt := range []string{"mcp", "ai-inference", "not-an-area"} {
		_, err := AreaPermissions(permInstance, exempt)
		require.Error(t, err, "an area with no login must not be given a grant: %s", exempt)
	}
}

func TestAnInstanceIdThatIsNotOneTokenIsRefused(t *testing.T) {
	for _, bad := range []string{"", "a.b", "a*", "a>", "a b"} {
		_, err := AreaPermissions(bad, "event-management")
		require.Error(t, err, bad)
	}
}

func TestPermissionsAreDeterministicAndSorted(t *testing.T) {
	a, err := ServiceUsers(permInstance)
	require.NoError(t, err)
	b, err := ServiceUsers(permInstance)
	require.NoError(t, err)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	require.Equal(t, string(ja), string(jb), "the same inputs must render the same bytes")
	for area, p := range allAreaPermissions(t) {
		require.True(t, slices.IsSorted(p.Publish.Allow), area)
		require.True(t, slices.IsSorted(p.Subscribe.Allow), area)
	}
}

// The grants name no other instance: two instances on one broker must not reach each other.
func TestNoGrantNamesAnotherInstance(t *testing.T) {
	other, err := AreaPermissions("inst2", "device-management")
	require.NoError(t, err)
	mine, err := AreaPermissions(permInstance, "device-management")
	require.NoError(t, err)
	for _, s := range append(slices.Clone(other.Publish.Allow), other.Subscribe.Allow...) {
		if strings.Contains(s, "inst2") {
			require.False(t, mine.publishes(s) || mine.subscribes(s), "%s leaks across instances", s)
		}
	}
}

// 🔑 Only a stream's declared writers can publish into it. This is the forgery property: a
// subject a platform reader consumes can be written only by an area the catalog names.
func TestOnlyDeclaredWritersCanPublishIntoAStream(t *testing.T) {
	all := allAreaPermissions(t)
	for _, st := range streams.All {
		if st.Shape == streams.ShapeAdvisory {
			continue
		}
		subject := messaging.ConcreteSubjectFor(permInstance, "acme", st.Suffix, "dev1")
		for area, p := range all {
			if streams.Writes(st.Suffix, area) {
				require.True(t, p.publishes(subject), "%s is a declared writer of %s but cannot publish %s", area, st.Suffix, subject)
			} else {
				require.False(t, p.publishes(subject), "%s is NOT a declared writer of %s yet can publish %s", area, st.Suffix, subject)
			}
		}
	}
}

// Nobody publishes where a device does: a subject in the device-events namespace is what makes an
// event the broker's statement of which device it came from.
func TestNoAreaCanPublishIntoTheDeviceEventsNamespace(t *testing.T) {
	// Both shapes the capture stream stores: a device's own telemetry and a gateway's for a child.
	for _, events := range []string{
		messaging.DeviceEventsSubject(permInstance, "acme", "dev1"),
		messaging.GatewayEventsSubject(permInstance, "acme", "gw1", "dev1"),
	} {
		for area, p := range allAreaPermissions(t) {
			require.False(t, p.publishes(events), "%s on %s", area, events)
			require.False(t, p.publishes(messaging.MqttMsgsSubject(events)), "%s on %s", area, events)
		}
		require.False(t, ServicePermissions().publishes(events), events)
	}
}

// 🔑 EVERY SUBJECT THE CAPTURE STREAM STORES IS DENIED TO EVERY SERVICE LOGIN. The capture
// stream's subject list is the declaration; the deny is derived from the same shapes. If a new
// subject is added to the capture stream (DeviceEventsSubjects) and not to the deny, a service
// could forge into it, and this fails. The negative control is the loop's own premise: the list
// has the gateway shape in it, so a deny that only knew the device shape is caught.
func TestEveryCaptureSubjectIsDeniedToEveryServiceLogin(t *testing.T) {
	subjects := messaging.DeviceEventsSubjects("*")
	require.Len(t, subjects, 2, "the capture stream stores a device's own subject and a gateway's")
	deny := ServicePublishDeny()
	for _, subject := range subjects {
		for _, form := range []string{subject, messaging.MqttMsgsSubject(subject)} {
			require.Contains(t, deny, form, "the service deny must cover capture subject %s", form)
		}
	}
}

// The gateway grant is the device's, confined to ITS OWN token, and constant-size.
func TestDeviceGrantCoversOnlyItsOwnGatewayNamespace(t *testing.T) {
	grant := func(device string) jwt.Permissions {
		p, err := DevicePermissions(permInstance, "acme", device)
		require.NoError(t, err)
		return p
	}
	allows := func(list jwt.StringList, subject string) bool {
		for _, g := range list {
			if natsSubjectMatches(g, subject) {
				return true
			}
		}
		return false
	}
	p := grant("gw1")
	for _, child := range []string{"c1", "c-2", "anything-at-all"} {
		for _, leaf := range []string{"events", "responses", "reports"} {
			require.True(t, allows(p.Pub.Allow, messaging.GatewayDeviceSubject(permInstance, "acme", "gw1", child, leaf)), "%s/%s", child, leaf)
		}
		for _, leaf := range []string{"commands", "desired"} {
			require.True(t, allows(p.Sub.Allow, messaging.GatewayDeviceSubject(permInstance, "acme", "gw1", child, leaf)), "%s/%s", child, leaf)
		}
	}
	require.True(t, allows(p.Sub.Allow, messaging.GatewayNoticesSubject(permInstance, "acme", "gw1")))

	// Another gateway's namespace, another tenant's, another instance's: none reachable.
	for _, subject := range []string{
		messaging.GatewayEventsSubject(permInstance, "acme", "gw2", "c1"),
		messaging.GatewayEventsSubject(permInstance, "other", "gw1", "c1"),
		messaging.GatewayEventsSubject("inst2", "acme", "gw1", "c1"),
	} {
		require.False(t, allows(p.Pub.Allow, subject), subject)
	}
	require.False(t, allows(p.Sub.Allow, messaging.GatewayNoticesSubject(permInstance, "acme", "gw2")))
	// A device cannot publish its own notices, which are the platform's word to it.
	require.False(t, allows(p.Pub.Allow, messaging.GatewayNoticesSubject(permInstance, "acme", "gw1")))

	// Constant size: the grant has no children in it.
	require.Equal(t, len(grant("gw1").Pub.Allow), len(grant("a-device-that-is-nobodys-gateway").Pub.Allow))
	require.Equal(t, len(grant("gw1").Sub.Allow), len(grant("another-one").Sub.Allow))
}

// 🔑 R4: a KV put is a publish into the bucket's subjects. Only a bucket's declared writers hold
// that grant, so no area but device-management can make one device's events resolve as another's.
func TestOnlyDeclaredWritersCanWriteABucket(t *testing.T) {
	all := allAreaPermissions(t)
	for _, b := range kv.All {
		if b.Account != kv.App {
			continue
		}
		// Every concrete bucket this declaration can be, whichever area opens it.
		var buckets []string
		for _, owner := range b.Admins() {
			buckets = append(buckets, messaging.KvBucketNames(permInstance, b, owner)...)
		}
		for area, p := range all {
			mine := messaging.KvBucketNames(permInstance, b, area)
			for _, bucket := range buckets {
				writer := kv.Writes(b.Name, area) && slices.Contains(mine, bucket)
				require.Equal(t, writer, p.publishes("$KV."+bucket+".key"), "%s on %s", area, bucket)
			}
		}
	}
	byToken := messaging.CacheBucketName(permInstance, "device-management", kv.BucketDeviceByToken)
	for area, p := range all {
		require.Equal(t, area == "device-management", p.publishes("$KV."+byToken+".k"), area)
	}
}

// 🔑 R3: a durable consumer is named exactly, so one area cannot rewrite, steal from or ack
// another's. The one wildcard-named grant is the ephemeral reader's, on its own stream, and it
// is the residue the stream-administration step removes.
func TestAnAreaCannotTouchAnotherAreasDurable(t *testing.T) {
	all := allAreaPermissions(t)
	for _, st := range streams.All {
		name := messaging.StreamName(permInstance, st.Suffix)
		for _, owner := range streams.ReadersOf(st.Suffix) {
			if !owner.Mode.Consumes() {
				continue
			}
			for _, d := range messaging.DeclaredDurables(permInstance, st.Suffix, owner) {
				for area, p := range all {
					if area == owner.Area {
						continue
					}
					for _, verb := range []string{
						"CONSUMER.CREATE." + name + "." + d,
						"CONSUMER.MSG.NEXT." + name + "." + d,
						"CONSUMER.DELETE." + name + "." + d,
						"CONSUMER.DURABLE.CREATE." + name + "." + d,
						"$JS.ACK." + name + "." + d + ".1.2.3.4.5",
					} {
						subject := verb
						if !strings.HasPrefix(verb, "$JS.") {
							subject = "$JS.API." + verb
						}
						require.False(t, p.publishes(subject), "%s can %s (the durable is %s's)", area, subject, owner.Area)
					}
				}
			}
		}
	}
}

// 🔴 STREAM CREATE AND UPDATE NAME THE STREAM, NOT ITS CONFIGURATION. Whoever may update a stream
// may give it sources, a subject transform or RePublish, and each of those writes into a stream
// past every publish permission; whoever may create one may give it any subjects, which captures
// another login's inbox, another bucket's keys or the broker's own authorization requests and
// reads them back. So NO AREA holds either verb on a catalog stream or bucket: they belong to the
// broker administrator. The exceptions are exactly two, each a named exception to the catalog and
// each pinned here by the full set it may touch: the MQTT gateway's own stores (event-sources) and
// nothing else. A new grant of either verb is a visible edit to this list.
func TestNoAreaCanCreateOrUpdateAStreamExceptTheGatewayStores(t *testing.T) {
	gatewayStores := map[string]bool{}
	for _, s := range messaging.MqttStoreNames() {
		gatewayStores[s] = true
	}
	for area, p := range allAreaPermissions(t) {
		for _, a := range p.Publish.Allow {
			for _, verb := range []string{"$JS.API.STREAM.CREATE.", "$JS.API.STREAM.UPDATE."} {
				target, ok := strings.CutPrefix(a, verb)
				if !ok {
					continue
				}
				require.Equal(t, mqttGatewayArea, area, "%s may %s%s: only the MQTT gateway's owner shapes a stream", area, verb, target)
				require.True(t, gatewayStores[target], "%s may %s%s, which is not one of the gateway's stores", area, verb, target)
			}
		}
	}
}

// The negative control for the test above: it must be able to fail. A generated grant that DID
// hold the verb on a catalog stream is built by hand and the same rule is applied to it.
func TestTheStreamShapingRuleCanFail(t *testing.T) {
	g := newGrant()
	g.api("STREAM.CREATE." + messaging.StreamName(permInstance, streams.ResolvedEvents))
	p := g.permissions(true)
	gatewayStores := map[string]bool{}
	for _, s := range messaging.MqttStoreNames() {
		gatewayStores[s] = true
	}
	var offending []string
	for _, a := range p.Publish.Allow {
		if target, ok := strings.CutPrefix(a, "$JS.API.STREAM.CREATE."); ok && !gatewayStores[target] {
			offending = append(offending, target)
		}
	}
	require.NotEmpty(t, offending, "the rule did not notice a CREATE on a catalog stream")
}

// The broker administrator is the one login that shapes the catalog's streams and buckets, and it
// is held to that: it can create, update and inspect every stream and bucket (and delete the one
// declared for a retention migration), reads its own reply inbox, and holds no publish right on
// any data subject and no consumer or message verb at all beyond the migration's durable.
func TestTheBrokerAdministratorShapesTheCatalogAndNothingElse(t *testing.T) {
	p, err := AdminPermissions(permInstance)
	require.NoError(t, err)
	for _, st := range streams.All {
		name := messaging.StreamName(permInstance, st.Suffix)
		for _, verb := range []string{"STREAM.INFO.", "STREAM.CREATE.", "STREAM.UPDATE."} {
			require.True(t, p.publishes("$JS.API."+verb+name), "the administrator cannot %s%s", verb, name)
		}
		require.Equal(t, st.RecreateOnRetentionChange, p.publishes("$JS.API.STREAM.DELETE."+name), "delete is for the declared migration only: %s", name)
		// No data goes in through this login.
		for _, subject := range messaging.StreamSubjects(permInstance, st.Suffix) {
			require.False(t, p.publishes(strings.ReplaceAll(subject, "*", "x")), "the administrator may publish into %s", subject)
		}
	}
	for _, b := range kv.All {
		if b.Account != kv.App {
			continue
		}
		for _, area := range b.Admins() {
			for _, bucket := range messaging.KvBucketNames(permInstance, b, area) {
				require.True(t, p.publishes("$JS.API.STREAM.CREATE."+messaging.KvStreamName(bucket)), "no create on %s", bucket)
				require.False(t, p.publishes("$KV."+bucket+".key"), "the administrator may write into %s", bucket)
			}
		}
	}
	for _, a := range p.Publish.Allow {
		require.True(t, strings.HasPrefix(a, "$JS.API."), "the administrator may publish to %q, which is not a JetStream API subject", a)
		for _, forbidden := range []string{"STREAM.PURGE", "STREAM.MSG", "STREAM.RESTORE", "STREAM.SNAPSHOT", "CONSUMER.CREATE", "CONSUMER.DELETE", "CONSUMER.MSG", "DIRECT.GET"} {
			require.NotContains(t, a, forbidden)
		}
	}
	require.Equal(t, []string{messaging.InboxPrefix(config.NatsAdminPrincipal) + ".>"}, p.Subscribe.Allow)
	require.False(t, p.AllowResponses)
	require.False(t, p.publishes("$MQTT.msgs.acme.devices.d1.events"), "the device-events deny rides on this login too")
}

// Stream deletion, restore and message deletion are not granted wholesale: they appear only where
// a declaration calls for them.
func TestDestructiveVerbsAreGrantedOnlyWhereDeclared(t *testing.T) {
	for area, p := range allAreaPermissions(t) {
		for _, a := range p.Publish.Allow {
			require.NotContains(t, a, "STREAM.RESTORE", area)
			require.NotContains(t, a, "STREAM.SNAPSHOT", area)
			require.False(t, a == ">" || a == "*" || a == "$JS.API.>" || a == "$JS.>", "%s holds the blanket grant %q", area, a)
			if strings.HasPrefix(a, "$JS.API.STREAM.DELETE.") {
				// The only deletion an area holds is user-management retiring the shared-account
				// copies of the sign-in buckets. The retention migration's delete is the broker
				// administrator's.
				require.True(t, area == "user-management" && strings.Contains(a, "KV_"),
					"%s may delete a stream it has no declared reason to: %s", area, a)
			}
			if strings.HasPrefix(a, "$JS.API.STREAM.PURGE.") {
				require.Equal(t, "user-management", area, "only the tenant purge purges: %s holds %s", area, a)
			}
		}
	}
}

// Replies: an area subscribes to its own inbox prefix and to no other, and the broker-wide
// "_INBOX.>" is nobody's. Nothing else grants a publish into an inbox: replies ride
// allow_responses.
func TestInboxesAreEachAreasOwn(t *testing.T) {
	for area, p := range allAreaPermissions(t) {
		require.True(t, p.AllowResponses, area)
		require.True(t, p.subscribes(messaging.InboxPrefix(area)+".abc.def"), area)
		require.False(t, p.subscribes("_INBOX.abc"), area)
		for other := range allAreaPermissions(t) {
			if other != area {
				require.False(t, p.subscribes(messaging.InboxPrefix(other)+".abc"), "%s reads %s's inbox", area, other)
			}
		}
		for _, a := range p.Publish.Allow {
			require.False(t, strings.HasPrefix(a, "_INBOX"), "%s may publish into an inbox: %s", area, a)
		}
	}
}

func TestOnlyTheCalloutAreaAnswersDeviceAuthorization(t *testing.T) {
	for area, p := range allAreaPermissions(t) {
		require.Equal(t, area == "device-management", p.subscribes(AuthCalloutRequestSubject), area)
	}
}

// Every declared reader's durable is granted to that area, in full.
func TestEveryDeclaredDurableIsGrantedToItsArea(t *testing.T) {
	all := allAreaPermissions(t)
	for _, st := range streams.All {
		name := messaging.StreamName(permInstance, st.Suffix)
		for _, r := range streams.ReadersOf(st.Suffix) {
			p, ok := all[r.Area]
			if !ok {
				continue
			}
			for _, d := range messaging.DeclaredDurables(permInstance, st.Suffix, r) {
				require.True(t, p.publishes("$JS.API.CONSUMER.CREATE."+name+"."+d+".inst1.*.x"), "%s create %s", r.Area, d)
				require.True(t, p.publishes("$JS.API.CONSUMER.INFO."+name+"."+d), "%s info %s", r.Area, d)
				require.True(t, p.publishes("$JS.API.CONSUMER.MSG.NEXT."+name+"."+d), "%s next %s", r.Area, d)
				require.True(t, p.publishes("$JS.ACK."+name+"."+d+".1.2.3.4.5"), "%s ack %s", r.Area, d)
			}
		}
	}
}

func TestEveryDeclaredBucketHasAConcreteName(t *testing.T) {
	for _, b := range kv.All {
		if b.Account == kv.Identity {
			for _, area := range b.Admins() {
				require.Empty(t, messaging.KvBucketNames(permInstance, b, area), "%s lives in another account", b.Name)
			}
			continue
		}
		for _, area := range b.Admins() {
			require.NotEmpty(t, messaging.KvBucketNames(permInstance, b, area), "%s has no concrete name for %s", b.Name, area)
		}
		// An area that is not declared for a bucket gets no name for it, and so no grant.
		for _, area := range config.NatsLoginAreas() {
			if !slices.Contains(b.Admins(), area) {
				require.Empty(t, messaging.KvBucketNames(permInstance, b, area), "%s must not map for %s", b.Name, area)
			}
		}
	}
}

func TestSystemLoginKeepsItsAllowLists(t *testing.T) {
	sys := SysPermissions()
	require.ElementsMatch(t, SysPublishAllow(), sys.Publish.Allow)
	require.ElementsMatch(t, SysSubscribeAllow(), sys.Subscribe.Allow)
	require.False(t, sys.AllowResponses)
}

func TestServiceUsersCoverEveryStaticLogin(t *testing.T) {
	users, err := ServiceUsers(permInstance)
	require.NoError(t, err)
	app := users[AppAccount]
	// 🔴 THE SHARED LOGIN IS NOT A STATIC LOGIN OF THE GENERATED DOCUMENT. A document that carried it
	// by default would put it back on every fresh install and keep it through the retirement.
	require.NotContains(t, app, ServiceUser)
	require.Contains(t, app, AdminUser)
	for _, area := range config.NatsLoginAreas() {
		require.Contains(t, app, LoginUser(area))
	}
	require.Len(t, app, 1+len(config.NatsLoginAreas()))
	require.Contains(t, users[SystemAccount], SysUser)
	// Held out on purpose: the identity login's account is its boundary.
	for _, m := range users {
		require.NotContains(t, m, IdentityUser)
	}
}

// The permissions serialise as the keys nats-server reads. A renamed field would render a login
// with no restriction at all, which is the failure that matters.
func TestPermissionsSerialiseAsTheBrokerReadsThem(t *testing.T) {
	raw, err := json.Marshal(Permissions{
		Publish: &Rules{Allow: []string{"a.>"}, Deny: []string{"b"}}, Subscribe: &Rules{Allow: []string{"c"}}, AllowResponses: true,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"publish":{"allow":["a.>"],"deny":["b"]},"subscribe":{"allow":["c"]},"allow_responses":true}`, string(raw))
	raw, err = json.Marshal(ServicePermissions())
	require.NoError(t, err)
	require.JSONEq(t, `{"publish":{"deny":["*.*.devices.*.events","$MQTT.msgs.*.*.devices.*.events","*.*.gateways.*.devices.*.events","$MQTT.msgs.*.*.gateways.*.devices.*.events"]}}`, string(raw))
}

// The tenant purge deletes the gateway's consumers that name a tenant, whose names the gateway
// chooses, so it holds a wildcard over the message store's consumers. event-sources' fail-closed
// durables are in that store, and deleting one loses a PUBACKed message, so the wildcard is carved
// around exactly those names: purgeable consumers still are, the durables are not.
func TestThePurgeCannotDeleteTheFailClosedDurables(t *testing.T) {
	p, err := AreaPermissions(permInstance, tenantPurgeArea)
	require.NoError(t, err)
	names := messaging.MqttFailClosedConsumerNames(permInstance)
	require.NotEmpty(t, names)
	for _, d := range names {
		require.False(t, p.publishes("$JS.API.CONSUMER.DELETE."+messaging.MqttMessageStore+"."+d),
			"user-management may delete the fail-closed durable %s", d)
	}
	require.True(t, p.publishes("$JS.API.CONSUMER.DELETE."+messaging.MqttMessageStore+".a-gateway-consumer"),
		"the carve-out removed the purge of the gateway's own consumers")
	// event-sources, which owns the durables, still manages them.
	own, err := AreaPermissions(permInstance, mqttGatewayArea)
	require.NoError(t, err)
	require.True(t, own.publishes("$JS.API.CONSUMER.DELETE."+messaging.MqttMessageStore+"."+names[0]))
}

// ownedStreams are the streams an area may hold wildcard consumer verbs on, because it owns them
// and nobody else trusts them: the key-value streams of the buckets it opens (a watch or a key
// listing is an ordered consumer the client library names) and the MQTT gateway's stores (the
// tenant purge deletes the consumers that name a tenant).
func ownedStreams() map[string]bool {
	owned := map[string]bool{
		messaging.MqttSessionStore: true, messaging.MqttMessageStore: true, messaging.MqttPubRelStore: true,
	}
	for _, name := range messaging.IdentityBucketNames(permInstance) {
		owned[messaging.KvStreamName(name)] = true
	}
	for _, b := range kv.All {
		for _, area := range config.NatsLoginAreas() {
			for _, bucket := range messaging.KvBucketNames(permInstance, b, area) {
				owned[messaging.KvStreamName(bucket)] = true
			}
		}
	}
	return owned
}

// wildcardConsumerGrants lists every consumer verb, in p, with a wildcard in the consumer-name
// token on a stream that is not owned.
func wildcardConsumerGrants(p Permissions) []string {
	owned := ownedStreams()
	var out []string
	for _, a := range p.Publish.Allow {
		for _, verb := range []string{"CONSUMER.CREATE.", "CONSUMER.DURABLE.CREATE.", "CONSUMER.INFO.", "CONSUMER.DELETE.", "CONSUMER.MSG.NEXT.", "CONSUMER.LEADER.STEPDOWN."} {
			rest, ok := strings.CutPrefix(a, jsAPI+verb)
			if !ok {
				continue
			}
			stream, consumer, _ := strings.Cut(rest, ".")
			// The consumer is the token after the stream. "<name>.>" is a durable's own name followed
			// by the filter-subject form of a create, which is exact; "*" or ">" in the name's place is not.
			consumer, _, _ = strings.Cut(consumer, ".")
			if !owned[stream] && (consumer == "*" || consumer == ">") {
				out = append(out, a)
			}
		}
		if rest, ok := strings.CutPrefix(a, "$JS.ACK."); ok {
			stream, consumer, _ := strings.Cut(rest, ".")
			consumer, _, _ = strings.Cut(consumer, ".")
			if !owned[stream] && (consumer == "*" || consumer == ">") {
				out = append(out, a)
			}
		}
	}
	return out
}

// 🔴 NO AREA NAMES A CONSUMER WITH A WILDCARD ON A MESSAGE STREAM. A grant of "*" in the consumer
// name token lets its holder create-or-update, fetch from, acknowledge and delete any consumer on
// the stream, including another area's durable: rewriting its filter, or stealing and acknowledging
// its messages. It used to be needed by the one ephemeral reader (event-processing's replay), whose
// consumer the client library named; that reader reads by sequence now.
func TestNoAreaHoldsAWildcardConsumerNameOnAMessageStream(t *testing.T) {
	for area, p := range allAreaPermissions(t) {
		require.Empty(t, wildcardConsumerGrants(p), "%s holds a wildcard consumer name on a message stream", area)
	}
}

// The negative control: the grant the replay reader used to hold is caught by the same rule, and
// event-processing's real one is not it.
func TestTheWildcardConsumerRuleCanFail(t *testing.T) {
	name := messaging.StreamName(permInstance, streams.ResolvedEvents)
	g := newGrant()
	g.api("CONSUMER.CREATE."+name+".>", "CONSUMER.INFO."+name+".*", "CONSUMER.MSG.NEXT."+name+".*")
	g.publish("$JS.ACK." + name + ".*.>")
	require.Len(t, wildcardConsumerGrants(g.permissions(true)), 4)

	ep, err := AreaPermissions(permInstance, "event-processing")
	require.NoError(t, err)
	require.Empty(t, wildcardConsumerGrants(ep))
	require.True(t, ep.publishes("$JS.API.DIRECT.GET."+name), "event-processing reads resolved-events by sequence")
	require.True(t, ep.publishes("$JS.API.STREAM.MSG.GET."+name))
	// It still manages the two durables it declares.
	require.True(t, ep.publishes("$JS.API.CONSUMER.MSG.NEXT."+name+"."+messaging.AckFloorDurableName(permInstance, "event-processing", streams.ResolvedEvents)))
}

// The shared login is written by exactly one function, for exactly one broker configuration (the
// upgrade that introduces the per-area logins), and adds that login and nothing else.
func TestOnlyTheTransitionalDocumentCarriesTheSharedLogin(t *testing.T) {
	plain, err := ServiceUsersFor(permInstance, config.NatsLoginAreas())
	require.NoError(t, err)
	with, err := ServiceUsersWithSharedLogin(permInstance, config.NatsLoginAreas())
	require.NoError(t, err)
	require.NotContains(t, plain[AppAccount], ServiceUser)
	require.Equal(t, ServicePermissions(), with[AppAccount][ServiceUser])
	delete(with[AppAccount], ServiceUser)
	require.Equal(t, plain, with, "the transitional document differs from the plain one by the shared login alone")
	for account, byUser := range plain {
		for u := range byUser {
			require.NotEqual(t, ServiceUser, u, "account %s", account)
		}
	}
}

// The operator's diagnostics list every stream and its consumers over the administrator's login,
// and that grant is a listing and nothing more.
func TestTheBrokerAdministratorCanEnumerateButNotRead(t *testing.T) {
	p, err := AdminPermissions(permInstance)
	require.NoError(t, err)
	for _, s := range []string{
		"$JS.API.STREAM.LIST", "$JS.API.STREAM.NAMES",
		"$JS.API.CONSUMER.LIST." + messaging.StreamName(permInstance, streams.All[0].Suffix),
		"$JS.API.CONSUMER.NAMES." + messaging.MqttSessionStore,
	} {
		require.True(t, p.publishes(s), s)
	}
	for _, s := range []string{
		"$JS.API.CONSUMER.MSG.NEXT.s.c", "$JS.API.CONSUMER.CREATE.s.c", "$JS.API.CONSUMER.DELETE.s.c",
		"$JS.API.STREAM.MSG.GET.s", "$JS.API.DIRECT.GET.s", "$JS.API.STREAM.PURGE.s",
	} {
		require.False(t, p.publishes(s), s)
	}
}
