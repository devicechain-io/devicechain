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
					if streams.ReadsAs(st.Suffix, area, streams.ReadEphemeral) {
						continue // the named residue
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
// past every publish permission. That is the residue the stream-administration step removes; until
// then the grant must at least stop at the streams and buckets the catalog says the area
// administers, plus the MQTT gateway's stores for event-sources (the named exception). An area
// that gained an update on a stream it only has to read would be a forgery path nothing else here
// sees, because the publish properties above all hold.
func TestStreamAdministrationIsGrantedOnlyWhereDeclared(t *testing.T) {
	for area, p := range allAreaPermissions(t) {
		mayAdminister := map[string]bool{}
		for _, st := range streams.All {
			if streams.Administers(st.Suffix, area) {
				mayAdminister[messaging.StreamName(permInstance, st.Suffix)] = true
			}
		}
		for _, b := range kv.All {
			for _, bucket := range messaging.KvBucketNames(permInstance, b, area) {
				mayAdminister[messaging.KvStreamName(bucket)] = true
			}
		}
		if area == mqttGatewayArea {
			for _, s := range messaging.MqttStoreNames() {
				mayAdminister[s] = true
			}
		}
		for _, a := range p.Publish.Allow {
			for _, verb := range []string{"$JS.API.STREAM.CREATE.", "$JS.API.STREAM.UPDATE."} {
				target, ok := strings.CutPrefix(a, verb)
				if !ok {
					continue
				}
				require.True(t, mayAdminister[target], "%s may %s%s, a stream it does not administer", area, verb, target)
			}
		}
	}
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
				found := false
				for _, st := range streams.All {
					if st.RecreateOnRetentionChange && a == "$JS.API.STREAM.DELETE."+messaging.StreamName(permInstance, st.Suffix) {
						found = streams.Administers(st.Suffix, area)
					}
				}
				// user-management retires the shared-account copies of the sign-in buckets.
				if area == "user-management" && strings.Contains(a, "KV_") {
					found = true
				}
				require.True(t, found, "%s may delete a stream it has no declared reason to: %s", area, a)
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
	require.Contains(t, app, ServiceUser)
	require.Contains(t, app, AdminUser)
	for _, area := range config.NatsLoginAreas() {
		require.Contains(t, app, LoginUser(area))
	}
	require.Len(t, app, 2+len(config.NatsLoginAreas()))
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
