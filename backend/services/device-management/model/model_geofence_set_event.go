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
	"github.com/devicechain-io/dc-microservice/governance"
)

// MaxGeoFenceSetManifestBytes reports the largest a marshalled fence-set manifest can be, for
// a tenant holding the maximum number of fences with every bounded field at its worst case.
//
// 🔴 IT BUILDS THE WORST CASE AND MEASURES IT RATHER THAN RETURNING A NUMBER, and that is the
// difference between a claim and a check. This value is the justification for deleting an
// entire apparatus of broker-ceiling machinery — the pointer fact, the headroom subtraction,
// the live max_payload clamp — so it must be re-derived from the constants it depends on
// (governance.MaxGeoFenceCeiling, core.MaxTokenLen, the 64-character hash) every time it is
// asked. Written as a literal it would keep answering yesterday's number on the day somebody
// raised the fence cap, and the machinery that would have caught the resulting oversized fact
// is gone. That is not hypothetical: the figure this sentence used to quote was 21,577, from
// when the loop ran to a hard-coded 100 fences, and it survived unchanged into a tree where the
// real answer was forty times larger.
//
// 🔴 IT LOOPS THE PLATFORM MAXIMUM, NOT THE PLATFORM DEFAULT, AND FOR ONE MINT THOSE ARE 40x
// APART. The fence count is a TIER setting: the default is 100 and the largest any tier may
// grant is governance.MaxGeoFenceCeiling. The single consumer of this number is a startup
// warning about a broker per-message ceiling configured too small for a full manifest — a
// question about what the INSTANCE must survive, not about what one unconfigured tenant gets
// — so looping the default would measure a ~21.6 KB worst case for a deployment whose real
// worst case is ~860 KB, and would keep the warning silent on exactly the deployments that
// need it. Nothing ties the two constants together at compile time; this comment and
// TestManifestFitsOneBrokerMessage (processor/geofence_set_publisher_test.go) are what hold it
// — its derived floor is the leg that fails if this ever loops the default again.
//
// 🔴 THAT NAME WAS FICTIONAL UNTIL IT WAS CHECKED. This comment cited a
// TestTheManifestWorstCaseIsMeasuredAtThePlatformMaximum that has never existed in this tree —
// written in the same change that deleted an identical fiction from core/geo's e7_test.go, and
// caught only by grepping every name a comment cites. A back-pointer nobody follows is worse
// than none: it advertises protection that is not there.
//
// The token is spelled with a character the grammar admits and JSON never escapes, which is
// the worst case rather than a convenient one: core.ValidateToken permits only letters,
// digits, hyphen and underscore, so no token can cost more encoded than it does raw.
func MaxGeoFenceSetManifestBytes() int {
	token := strings.Repeat("A", core.MaxTokenLen)
	hash := strings.Repeat("f", 64)
	fences := make([]GeoFenceManifestEntry, 0, governance.MaxGeoFenceCeiling)
	for i := 0; i < governance.MaxGeoFenceCeiling; i++ {
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
