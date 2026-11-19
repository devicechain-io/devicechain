// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
)

const (
	// MaxGeoFenceGeometryHashesPerRequest bounds how many geometry documents one
	// GeoFenceGeometryDocuments call may ask for. Over the limit is an ERROR, never a
	// partial answer: a caller that asked for 40 addresses and silently got 24 cannot tell
	// that from a tenant holding only 24 of them, and this door's whole contract is that
	// absence means absence.
	//
	// 🔴 IT IS ARITHMETIC, NOT A ROUND NUMBER, AND THE ARITHMETIC IS THE ONLY REASON A
	// COUNT CAN DEFEND A BYTE CAP AT ALL. A limit counted in documents is a guess about
	// bytes unless each document has a known byte ceiling — which is exactly what
	// MaxGeoFenceGeometryBytes provides, applied to the jsonb-rendered form the archive
	// actually returns. 24 x 32 KiB is 786,432 bytes against svcclient.MaxResponseBytes
	// (1 MiB), leaving room for the hashes, the field names and the GraphQL envelope's
	// string escaping. TestGeometryBatchFitsOneResponse measures a worst-case response
	// rather than trusting that estimate, because two numbers in this tree disagree about
	// what escaping costs and a comment cannot settle it.
	//
	// It does not have to defend against documents already stored ABOVE that ceiling —
	// rows predating the bound exist — so a client is still expected to split its request
	// on a too-large response. Splitting is trivial here in a way it was not for the paged
	// read this replaces: a request names a SET OF ADDRESSES, so half of it is still a
	// well-formed request, where a (pageNumber, pageSize) offset could not be re-expressed
	// at a different size and forced the whole walk to restart.
	MaxGeoFenceGeometryHashesPerRequest = 24
)

// GeoFenceSetManifest names WHICH fences a fence-set version holds and the CONTENT ADDRESS
// of each one's geometry. It carries no geometry itself, which is the entire point: its
// size is a function of the fence COUNT and of nothing an author can write.
//
// 🔴 THE SIZE PROPERTY IS WHY AN ENTIRE APPARATUS OF BROKER-CEILING MACHINERY IS GONE, so it
// is MEASURED rather than asserted. A token is bounded by core.MaxTokenLen under a grammar
// containing no character JSON has to escape, and a hash is exactly 64 hex characters, so an
// entry has a fixed worst case and a whole manifest has one too — roughly a forty-eighth of the
// default 1 MiB per-message ceiling at the DEFAULT fence ceiling of 100, and about 82% of it at
// governance.MaxGeoFenceCeiling, the most any tier may grant. That second figure is the one the
// startup warning is measured against, because it is what the instance must survive.
//
// 🔑 NO BYTE COUNT IS QUOTED HERE ON PURPOSE. MaxGeoFenceSetManifestBytes() builds the worst
// case and measures it, and that function is the authority; an earlier draft of this comment
// named a figure derived by hand and was wrong by six bytes within a day of being written. A
// number in prose cannot be re-derived when a constant moves, which is exactly when it matters.
//
// It is ONE type serving two seams — the fence-set fact and the geoFenceSetManifest /
// currentGeoFenceSetManifest doors — deliberately, and the reasoning is the opposite of the
// one that keeps GeoFenceSetSnapshot and storedGeoFenceSetSnapshot apart. Those two are
// separate because they describe the same fence set at DIFFERENT levels of resolution, so a
// caller could marshal the wrong one and produce a document that looks correct until the
// archive it should have referenced is needed. A manifest is the same information on both
// seams; splitting it would create exactly the drift the split is supposed to prevent.
type GeoFenceSetManifest struct {
	// Version is the fence-set version this manifest describes. It is what a resolved
	// location event is stamped with, and the key a consumer files the set under.
	Version int32 `json:"version"`
	// Fences names each fence and the content address of its geometry, ordered by token
	// (the mint path orders them, so a manifest is a function of the fence set alone).
	Fences []GeoFenceManifestEntry `json:"fences"`
	// MintedAt is when the change that minted this version committed. It is carried for
	// operator diagnosis — how old is the set this engine is holding? — and containment
	// never reads it.
	MintedAt time.Time `json:"mintedAt"`
}

// GeoFenceManifestEntry is one fence inside a manifest: the token a rule names it by, and
// the content address under which its geometry is stored.
//
// 🔴 AN ENTRY WHOSE HASH THE HOLDER CANNOT RESOLVE MUST BECOME A FENCE CARRYING AN ERROR,
// NEVER A FENCE THAT IS ABSENT. Absence is already meaningful — a rule naming a fence the
// set does not contain is a distinct, reported condition — so a body that failed to arrive,
// dropped silently, reads downstream as "this fence does not exist here" and containment
// answers "outside" for a device that is inside. That is the one failure mode splitting the
// geometry out of the fact introduces, and it is the consumer's job to close.
type GeoFenceManifestEntry struct {
	Token string `json:"token"`
	Hash  string `json:"hash"`
}

// GeoFenceGeometryHash is the content address of a canonical geometry document.
//
// 🔴 HASH WHAT THE DATABASE HANDS BACK, NEVER WHAT THE AUTHOR WROTE. jsonb is not a byte
// store: it parses each number and reprints it with numeric_out, so the request text and
// the stored text are different lengths and the difference is unbounded (see
// MaxGeoFenceGeometryBytes for the measurements). Canonicalising before storing is what
// makes the stored form stable and predictable, but it does not make it EQUAL to the
// request text — separator spacing alone differs. The mint path hashes the geometry it
// just read out of geo_fences, and stores the blob from those same bytes, so an address
// and the document it names can never disagree. Hashing authored text instead would
// compute an address nothing is stored at, and the miss would look like a lost fence.
func GeoFenceGeometryHash(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

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
// written in the same change that deleted an identical fiction from core/geo's E7 encoder tests
// (that encoder and its tests have since been removed entirely), and
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
		panic("contract: a fence-set manifest of fixed shape failed to marshal: " + err.Error())
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
