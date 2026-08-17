// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package transport is the single declaration of the transport names the platform MINTS
// into a device's projected `source`, and the one reduction that reads a transport back out
// of one.
//
// A projected source is `transport` or `transport:qualifier`. It had three unrelated
// origins and no shared definition: a bare compile-time constant in lwm2m-ingest, a runtime
// concatenation in sparkplug-ingest, and — for a plain broker or HTTP gateway — the
// operator's own EventSource id, which is not a transport name at all. Nothing tied them
// together, and nothing could: event-sources, lwm2m-ingest and sparkplug-ingest do not all
// import each other, and command-delivery, which is the only thing that CLASSIFIES a source,
// imports none of them. So this package is a LEAF, imports nothing, and sits in core where
// every one of them already depends on it — adding it creates no new module edge.
//
// The cost of having no shared definition is on record. command-delivery's undeliverable
// deny list held the bare word "sparkplug" and compared it against the WHOLE source, which
// no producer writes: sparkplug-ingest stamps "sparkplug:"+hostId. The lookup missed every
// real device, the Undeliverable verdict could not fire at all, and every test passed
// against a hand-written value nothing emits.
//
// # 🔴 WHY THERE IS NO MQTT AND NO HTTP HERE
//
// Their absence is the most important thing in this file, and it is not an oversight. A
// plain MQTT or HTTP event source projects the OPERATOR-CHOSEN EventSource.Id — whose
// shipped defaults are "mqtt1" and "http1" — so the literal "mqtt" is a value NO PRODUCER
// EMITS. Adding it here would manufacture exactly the fiction that caused the sparkplug
// defect: a plausible constant, greppable and confidence-inspiring, that never appears in
// any real row. The set below is what the platform mints, and it is deliberately not named
// All, because it is not all the ways a device can reach the platform.
//
// # 🔴 TWO MATCHING DISCIPLINES, AND THEY ARE NOT INTERCHANGEABLE
//
//   - CLASSIFY BY TRANSPORT with Of. Answering "is this device on Sparkplug?" must cut the
//     qualifier off, because the qualifier is runtime input and the whole value is therefore
//     not a closed vocabulary that could be enumerated anywhere.
//   - MATCH A WHOLE SOURCE with plain equality, qualifier included. The asserted-presence
//     reconcilers do this (`WHERE presence_source = ? AND source = ?`) because they are
//     re-filing the rows ONE named emitter wrote, not every row from a family of them. Two
//     Sparkplug hosts are two emitters and must not reconcile each other's rows.
//
// Reaching for Of in the second case would make one host's reconciliation sweep the other's
// devices; reaching for equality in the first is the defect described above.
package transport

import "strings"

// Name is a transport as it appears in the first segment of a projected `source`.
type Name string

const (
	// LwM2M is stamped BARE and unqualified — there is no lwm2m:{serverId} form, and no
	// config field can produce one. A qualified example was once offered in a doc comment
	// and was minted by nothing; it is the single artifact that would talk a reader into
	// matching a prefix that never arrives.
	LwM2M Name = "lwm2m"

	// Sparkplug is ALWAYS qualified, as "sparkplug:"+hostId — see Qualify. The bare word
	// appears in no row a producer wrote.
	Sparkplug Name = "sparkplug"
)

// Minted is every transport name the platform itself writes into a source.
//
// 🔴 IT IS NOT CALLED All ON PURPOSE. It is not the set of transports devices arrive on —
// MQTT and HTTP are missing because they mint no name (see the package doc). It is the set
// of names an operator-chosen identifier must not be mistaken for, which is what makes it
// the right set for a reservation and the wrong set for a menu.
var Minted = []Name{LwM2M, Sparkplug}

// Of reduces a projected source to the transport it names, dropping any runtime qualifier:
// "sparkplug:plant-a" -> "sparkplug", "lwm2m" -> "lwm2m", "mqtt1" -> "mqtt1".
//
// 🔴 IT IS NOT A PREFIX MATCH, AND THAT IS THE POINT. Matching by prefix would catch a
// source merely NAMED like a transport — "sparkplugin", or an operator's gateway source
// called "sparkplug-test" — and since a plain gateway's source IS an operator-chosen id,
// those collisions are reachable input rather than hypotheticals. Cutting at the first ":"
// matches the one form producers mint and nothing adjacent to it.
//
// Note the result is not necessarily a MINTED name: for a plain gateway it is the operator's
// id, or its first segment. Callers deciding membership must test with IsMinted or against
// their own set rather than assuming this returns something from Minted.
func Of(source string) Name {
	transport, _, _ := strings.Cut(source, ":")
	return Name(transport)
}

// Qualify builds the qualified form a producer stamps. It exists so the separator is written
// once: a caller concatenating its own ":" is how the two sides of a match drift apart.
//
// ⚠️ It does not sanitize the qualifier, and a qualifier containing ":" yields a source with
// two of them. That is reachable today — sparkplug-ingest's hostId validation rejects the
// MQTT topic metacharacters and NUL but permits a colon — and it is harmless to Of, which
// cuts at the FIRST one and still answers "sparkplug". It is recorded here so nobody builds
// a parser on the assumption that a source has at most one colon.
func Qualify(name Name, qualifier string) string {
	return string(name) + ":" + qualifier
}

// IsMinted reports whether a name is one the platform itself writes.
//
// The intended use is a RESERVATION: an operator-chosen identifier that reduces to a minted
// name would be classified as that transport by every caller of Of, whatever the operator
// meant by it.
//
// 🔴 THE COST OF THAT CONFUSION INVERTS WITH THE CONSUMING LIST'S POLARITY, which is why
// this reservation is enforced at config load rather than left to each consumer. Against a
// DENY list the collision is self-inflicted and self-limiting — the operator named their own
// source "sparkplug" and their own commands stop. Against an ALLOW list it fails the other
// way: the collision makes a foreign transport's device look like a member, and the caller
// ACTS on a device the list was written to exclude.
func IsMinted(name Name) bool {
	for _, m := range Minted {
		if name == m {
			return true
		}
	}
	return false
}
