// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// MaxGeoFenceSetManifestBytes reports the largest a marshalled fence-set manifest can be, for
// a tenant holding the maximum number of fences with every bounded field at its worst case.
//
// 🔴 IT BUILDS THE WORST CASE AND MEASURES IT RATHER THAN RETURNING A NUMBER, and that is the
// difference between a claim and a check. This value is the justification for deleting an
// entire apparatus of broker-ceiling machinery — the pointer fact, the headroom subtraction,
// the live max_payload clamp — so it must be re-derived from the constants it depends on
// (MaxGeoFencesPerTenant, core.MaxTokenLen, the 64-character hash) every time it is asked.
// Written as a literal it would keep answering 21,577 on the day somebody raised the fence
// cap, and the machinery that would have caught the resulting oversized fact is gone.
//
// The token is spelled with a character the grammar admits and JSON never escapes, which is
// the worst case rather than a convenient one: core.ValidateToken permits only letters,
// digits, hyphen and underscore, so no token can cost more encoded than it does raw.
func MaxGeoFenceSetManifestBytes() int {
	token := strings.Repeat("A", core.MaxTokenLen)
	hash := strings.Repeat("f", 64)
	fences := make([]GeoFenceManifestEntry, 0, MaxGeoFencesPerTenant)
	for i := 0; i < MaxGeoFencesPerTenant; i++ {
		fences = append(fences, GeoFenceManifestEntry{Token: token, Hash: hash})
	}
	// The longest rendering time.Time has: nine significant fractional digits, and a
	// numeric zone offset rather than the single "Z" a UTC instant collapses to.
	worstTime := time.Date(2006, 1, 2, 15, 4, 5, 999999999, time.FixedZone("worst", 5*3600+30*60))
	encoded, err := MarshalGeoFenceSetManifest(&GeoFenceSetManifest{
		Version:  math.MinInt32, // the widest int32 rendering, sign included
		Fences:   fences,
		MintedAt: worstTime,
	})
	if err != nil {
		// Unreachable: every field is a string, an int32 or a time, all of which marshal.
		// Returning 0 here would read as "no bound", so refuse loudly instead.
		panic("model: a fence-set manifest of fixed shape failed to marshal: " + err.Error())
	}
	return len(encoded)
}

// MarshalGeoFenceSetManifest encodes a fence-set manifest for the wire (the producer side).
//
// JSON, not protobuf, and for a different reason than before. The fact this replaces carried
// opaque geometry documents that were ALREADY JSON, so re-encoding them into a proto bytes
// field would have bought nothing and added a second encoding of the same bytes to keep
// honest. A manifest carries no documents at all — it is a version, a timestamp and a list of
// two short strings — so the argument from opacity is gone, and what remains is that every
// other control-plane fact on these streams is JSON and a lone exception would earn nothing.
func MarshalGeoFenceSetManifest(manifest *GeoFenceSetManifest) ([]byte, error) {
	return json.Marshal(manifest)
}

// UnmarshalGeoFenceSetManifest decodes a fence-set manifest (the consumer side,
// event-processing). A missing fence list normalizes to a non-nil empty slice, so the version
// minted by deleting a tenant's LAST fence decodes to a known-EMPTY set rather than a nil one
// — the distinction the whole projection turns on, since a tenant with no fences answers
// "outside" correctly while a tenant whose set failed to arrive must answer an error.
//
// 🔴 THERE IS NO LONGER A FLAG TO CONSULT BEFORE READING THE FENCE LIST, AND THAT IS A
// DELETION, NOT AN OVERSIGHT. The fact this replaces carried FencesOmitted precisely because
// it had two forms — the whole set, and a pointer standing in for a set too large to send —
// which after this normalization had IDENTICAL fence lists. A manifest has one form. It
// cannot outgrow a message (see GeoFenceSetManifest for the arithmetic), so there is no
// second form for a flag to distinguish, and an empty list here means exactly what it says.
func UnmarshalGeoFenceSetManifest(encoded []byte) (*GeoFenceSetManifest, error) {
	manifest := &GeoFenceSetManifest{}
	if err := json.Unmarshal(encoded, manifest); err != nil {
		return nil, err
	}
	if manifest.Fences == nil {
		manifest.Fences = []GeoFenceManifestEntry{}
	}
	return manifest, nil
}

// GeoFenceSetPublisher publishes fence-set manifests (ADR-078). Like the detection-rules,
// roster and device-attribute publishers it is best-effort and side-band to the fence write:
// a marshal/publish failure is logged by the implementation, never surfaced to the caller — a
// NATS hiccup must not fail or roll back the authoring action, which is the source of truth.
//
// Emission is at-most-once, and the recovery story differs from the other fact publishers in
// a way worth stating: the consumer keeps no durable projection of its own, because this
// service already IS the durable archive of every version's snapshot and of every geometry
// document any version names. A fact that never reaches the stream is recovered by the
// consumer's startup reconcile reading those back, not by a replay of the stream.
// Implementations must be safe for concurrent use.
//
// 🔴 AN IMPLEMENTATION MUST NEVER TRUNCATE A MANIFEST, and unlike its predecessor it has no
// legitimate reason to want to: the fact carries no geometry, so no fence set can make one
// too large for the transport. If a publish is refused anyway — an operator can configure a
// per-message ceiling below any useful value, and values.schema.json states no minimum — the
// implementation counts and logs the refusal and sends nothing. A short fence list is
// indistinguishable downstream from a tenant who really has that many fences.
type GeoFenceSetPublisher interface {
	PublishGeoFenceSetManifest(ctx context.Context, manifest *GeoFenceSetManifest)
}

// GeoFenceSetMintedEvent is the RETIRED fence-set fact: the whole frozen set, carried inline.
//
// 🔴 NOTHING PRODUCES ONE ANY MORE. It is kept for exactly one change — event-processing still
// decodes it, and the two services are separate modules resolved through the workspace, so
// deleting the type here would red a build in a service this change does not touch. The
// consumer's cutover deletes both this type and the subjects it travelled on; do not build
// anything new against it, and do not "fix" a caller by reaching for it.
//
// What replaced it, and why, is on GeoFenceSetManifest: the fence set's size is a product of
// the fence count and what each fence CONTAINS, so a tenant at the documented authoring
// ceiling marshalled past the broker's per-message limit — which is what FencesOmitted below
// existed to survive. A manifest names geometry instead of carrying it, so the whole failure
// class, and every piece of machinery built to handle it, is gone rather than tuned.
type GeoFenceSetMintedEvent struct {
	Version  int32                 `json:"version"`
	Fences   []GeoFenceSnapshotRef `json:"fences"`
	MintedAt time.Time             `json:"mintedAt"`
	// FencesOmitted reported that this fact carried NO fences because the marshalled set did
	// not fit the broker's per-message ceiling — a POINTER fact, whose version the consumer
	// resolved through the frozen archive instead of reading Fences.
	//
	// It was a FIELD rather than an empty fence list because an empty list is already
	// meaningful: a tenant that never created a fence, and a tenant that deleted its last
	// one, both legitimately have none, and containment answers "outside" for them
	// correctly. Encoding "go and fetch this yourself" the same way would have turned a
	// hundred fences into a silently empty set — a healthy-looking rule that never fires.
	FencesOmitted bool `json:"fencesOmitted,omitempty"`
}

// MarshalGeoFenceSetMintedEvent encodes a retired whole-set fact. Retained only until the
// consumer's cutover; see GeoFenceSetMintedEvent.
func MarshalGeoFenceSetMintedEvent(event *GeoFenceSetMintedEvent) ([]byte, error) {
	return json.Marshal(event)
}

// UnmarshalGeoFenceSetMintedEvent decodes a retired whole-set fact, normalizing a missing
// fence list to a non-nil empty slice. Retained only until the consumer's cutover; see
// GeoFenceSetMintedEvent.
func UnmarshalGeoFenceSetMintedEvent(encoded []byte) (*GeoFenceSetMintedEvent, error) {
	event := &GeoFenceSetMintedEvent{}
	if err := json.Unmarshal(encoded, event); err != nil {
		return nil, err
	}
	if event.Fences == nil {
		event.Fences = []GeoFenceSnapshotRef{}
	}
	return event, nil
}
