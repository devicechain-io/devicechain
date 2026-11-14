// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package backupset is the one inventory of the durable state an instance keeps OUTSIDE its
// databases, and of what a restore owes each piece of it.
//
// An instance's recoverable state is more than two databases and a root key. Message streams, key
// value buckets and the object store all hold data, and the disaster-recovery design decides, item
// by item, whether a restore must bring it back (must-back-up), can rebuild it from the databases
// (rebuildable), or does not need it (transient). That decision used to live in prose. Here it is
// the declarations themselves: core/streams and core/kv carry a class on every entry, and this
// package adds the three kinds of item that are not in those registries and presents the union.
//
// The tests are the watcher. A stream or bucket declared without a class fails them; a new creation
// path that bypasses the registries fails them; and the table in the published disaster-recovery
// page is generated from Items, so the page cannot say something the code does not.
package backupset

import (
	"fmt"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/durability"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
)

// Kind says where an item lives.
type Kind string

const (
	// KindStream is a platform message stream declared in core/streams.
	KindStream Kind = "stream"
	// KindBucket is a key value bucket declared in core/kv.
	KindBucket Kind = "bucket"
	// KindGatewayStream is a stream the MQTT gateway creates for itself.
	KindGatewayStream Kind = "gateway stream"
	// KindObjectStore is the instance object store (a volume or an S3 bucket).
	KindObjectStore Kind = "object store"
)

// ObjectStoreName is the object store's name in the inventory.
const ObjectStoreName = "object-store"

// Item is one piece of durable state outside the databases.
type Item struct {
	Kind  Kind
	Name  string
	Class durability.Class
	// BackupClass is the checkpoint policy, separate from the published durability contract.
	BackupClass durability.BackupClass
	// Why is the reason for the class, in words an operator can act on.
	Why string
	// Procedure is what an operator does to back the item up. It is set for exactly the
	// must-back-up items: a class that promises a backup has to say how.
	Procedure string
}

// gateway lists the streams nats-server creates for the MQTT gateway. They are named by the server
// (core/messaging mqtt_store.go), so they are listed here rather than declared by a registry.
// TestGatewayNamesMatchTheTransport holds these to the transport's constants.
// $MQTT_msgs is checkpoint-required even though its published durability class is
// transient: fail-closed ownership can leave it holding the only accepted copy.
// Snapshot it with consumers and ownership/from metadata. The other gateway
// streams may be included together in an optional broker-continuity profile.
var gateway = []Item{
	{KindGatewayStream, "$MQTT_msgs", durability.Transient, durability.BackupCheckpoint, "a second copy of every QoS 1 and 2 message, held only until the platform stream has it", ""},
	{KindGatewayStream, "$MQTT_out", durability.Transient, durability.BackupEphemeral, "outgoing QoS 2 release records, in flight", ""},
	{KindGatewayStream, "$MQTT_qos2in", durability.Transient, durability.BackupEphemeral, "inbound QoS 2 handshake state, in flight", ""},
	{KindGatewayStream, "$MQTT_sess", durability.Rebuildable, durability.BackupRebuildable, "persistent MQTT sessions and their subscriptions; a device that subscribes on every connect recovers by itself, one that relies on the broker having remembered its subscriptions must be made to subscribe again", ""},
	{KindGatewayStream, "$MQTT_rmsgs", durability.Rebuildable, durability.BackupRebuildable, "retained messages; the platform publishes none itself, so each one is whatever a device or an integration retained and must publish again", ""},
}

// objectStore is the object store's entry.
var objectStore = Item{
	Kind:        KindObjectStore,
	Name:        ObjectStoreName,
	Class:       durability.MustBackUp,
	BackupClass: durability.BackupRequired,
	Why:         "update artifact content and tenant branding images; the database holds only each object's size and digest",
	Procedure: "Snapshot the volume, or turn on versioning and replication for the bucket, and take that copy after " +
		"the database base backup it belongs with.",
}

// streamProcedures names what an operator does for each must-back-up stream.
var streamProcedures = map[string]string{
	streams.DeviceEventsCapture:   "Freeze producers and consumers, then export with dcctl backup and verify with dcctl backup inspect --verify. Coordinate the database recovery cutoff separately; restore --plan is read-only and does not apply a restore.",
	streams.ConnectorDispatchDead: "Export with dcctl backup while producers and consumers are frozen, then verify with dcctl backup inspect --verify. Records absent from the backup cannot be recovered once the stream is lost; restore --plan does not apply a restore.",
}

// Items returns every item, streams first, then buckets, the gateway's streams and the object
// store, each group in declaration order.
func Items() []Item {
	var out []Item
	for _, s := range streams.All {
		it := Item{Kind: KindStream, Name: s.Suffix, Class: s.Durability, BackupClass: s.BackupClass, Why: s.DurabilityWhy}
		if it.Class == durability.MustBackUp {
			it.Procedure = streamProcedures[s.Suffix]
		}
		out = append(out, it)
	}
	for _, b := range kv.All {
		out = append(out, Item{Kind: KindBucket, Name: b.Name, Class: b.Durability, BackupClass: b.BackupClass, Why: b.DurabilityWhy})
	}
	out = append(out, gateway...)
	out = append(out, objectStore)
	return out
}

// ValidateBackupPolicy rejects incomplete inventory before backup tooling selects
// resources. It never treats an unknown policy as permission to omit an item.
// It is opt-in metadata validation; service startup behavior is unchanged.
func ValidateBackupPolicy() error {
	return validateBackupPolicy(Items())
}

func validateBackupPolicy(items []Item) error {
	for _, it := range items {
		if !it.BackupClass.Valid() {
			return fmt.Errorf("%s %q has missing or unknown backup classification %q", it.Kind, it.Name, it.BackupClass)
		}
	}
	return nil
}

// MustBackUp returns the items a restore cannot rebuild, sorted by kind and name.
func MustBackUp() []Item {
	var out []Item
	for _, it := range Items() {
		if it.Class == durability.MustBackUp {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Markers delimit the generated table in the disaster-recovery page.
const (
	TableStart = "<!-- durability:start -->"
	TableEnd   = "<!-- durability:end -->"
)

// Table renders Items as the markdown table the disaster-recovery page carries, between the
// markers. The cells hold identifiers and reasons only (no prose that would need translating), so
// the same block appears in every language of the page.
func Table() string {
	var b strings.Builder
	b.WriteString(TableStart + "\n")
	b.WriteString("| Item | Kind | Class | What a restore owes it |\n|---|---|---|---|\n")
	for _, it := range Items() {
		why := it.Why
		if it.Procedure != "" {
			why += ". " + it.Procedure
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", it.Name, it.Kind, it.Class, why)
	}
	b.WriteString(TableEnd + "\n")
	return b.String()
}
