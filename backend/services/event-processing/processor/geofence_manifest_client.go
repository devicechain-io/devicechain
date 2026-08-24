// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// geometryChunkSize is how many content addresses one geoFenceGeometry request asks for.
//
// 🔴 IT IS ARITHMETIC AGAINST A KNOWN PER-DOCUMENT CEILING, WHICH IS THE ONLY THING THAT LETS
// A COUNT DEFEND A BYTE CAP. The paged read this replaces could not do that: its page size was
// counted in FENCES while svcclient.MaxResponseBytes is counted in bytes, and nothing bounded
// what one fence weighed — so the size was a guess, wrong in both directions, and the walk
// needed a halving retry to survive being wrong. device-management's MaxGeoFenceGeometryBytes
// bounds a single stored document at 32 KiB, so 24 documents is at most 786,432 bytes against
// a 1 MiB cap, leaving room for the addresses, the field names and the response envelope.
//
// It is deliberately the SAME number as device-management's own per-request limit rather than
// a smaller one hedged against it. Two numbers that must agree are better as one number named
// in both places than as a value and a margin nobody re-derives: if they ever drift, the peer
// REFUSES the request and this errors loudly, which is the failure direction to want.
const geometryChunkSize = dmmodel.MaxGeoFenceGeometryHashesPerRequest

// maxGeometryRequests bounds the total GraphQL requests one geometry fetch may spend, across
// every chunk and every split retry, so a peer that keeps refusing cannot spin this goroutine
// forever.
//
// It is a RUNAWAY STOP, not a second size limit, so it is sized well above anything reachable:
// the worst legitimate fetch is MaxGeoFencesPerTenant distinct documents at a chunk size of
// one, i.e. 100 requests, plus the few spent on chunks refused on the way down. 512 leaves
// that room several times over, and it ERRORS rather than returning what it has.
const maxGeometryRequests = 512

// geoFenceSetManifestQuery reads one version's manifest. It carries NO tenant argument: the
// tenant travels as the service-token client's tenant header, and device-management's rows are
// tenant-scoped, so which manifests this query can reach is decided by the header and not by
// anything expressible in the document.
const geoFenceSetManifestQuery = `query($version: Int!) {
  geoFenceSetManifest(version: $version) {
    version
    mintedAt
    fences { token hash }
  }
}`

// currentGeoFenceSetManifestQuery reads the tenant's CURRENT manifest — version and fences from
// one row, so a fence edit landing mid-read cannot yield fences filed under a version that is
// not the one reported.
const currentGeoFenceSetManifestQuery = `query {
  currentGeoFenceSetManifest {
    version
    mintedAt
    fences { token hash }
  }
}`

// geoFenceGeometryQuery resolves content addresses into the documents stored under them.
const geoFenceGeometryQuery = `query($hashes: [String!]!) {
  geoFenceGeometry(hashes: $hashes) { hash geometry }
}`

// manifestPayload is the decoded wire shape of a manifest. It is a NAMED type rather than an
// anonymous struct at each call site so a test can decode a response produced by
// device-management's REAL schema through exactly the mapping this client uses — a field-name
// drift between the two services then fails a test instead of silently decoding to an empty
// fence set at runtime.
type manifestPayload struct {
	Version int32   `json:"version"`
	Fences  []entry `json:"fences"`
}

type entry struct {
	Token string `json:"token"`
	Hash  string `json:"hash"`
}

type geoFenceSetManifestResponse struct {
	GeoFenceSetManifest manifestPayload `json:"geoFenceSetManifest"`
}

type currentGeoFenceSetManifestResponse struct {
	CurrentGeoFenceSetManifest manifestPayload `json:"currentGeoFenceSetManifest"`
}

// geometryPayload is one resolved document. Geometry stays a STRING here and becomes
// json.RawMessage only when it is handed on: it is an opaque self-describing document that
// only the geofence compiler reads, and it is also the exact byte sequence its content address
// is the hash of, so decoding and re-encoding it would both duplicate that compiler and break
// every verification at once.
type geometryPayload struct {
	Hash     string `json:"hash"`
	Geometry string `json:"geometry"`
}

type geoFenceGeometryResponse struct {
	GeoFenceGeometry []geometryPayload `json:"geoFenceGeometry"`
}

// errArchiveSkew reports that the peer does not serve the manifest doors — it is running a
// build from before manifest delivery.
//
// 🔴 IT IS A DISTINCT ERROR BECAUSE ITS SYMPTOM IS INDISTINGUISHABLE FROM AN UNREACHABLE
// ARCHIVE AND ITS CAUSE AND CURE ARE NOTHING ALIKE. device-management and event-processing are
// independent Deployments that neither wait for nor order against each other, so a rolling
// upgrade briefly runs one of each, and a rollback of device-management alone runs them that
// way indefinitely. An operator seeing "cannot read the fence archive" will go and look at the
// network; an operator seeing "the peer does not serve this query" knows to look at what is
// deployed. The condition repairs itself the moment the peer rolls forward, and nothing here
// should try to work around it — falling back to the retired doors would keep a dead code path
// alive forever to serve a window measured in minutes.
var errArchiveSkew = errors.New("device-management does not serve the geofence manifest doors")

// isArchiveSkew reports whether a query error is the peer not knowing the query.
//
// It matches on the GraphQL validation vocabulary rather than on a status code, because the
// peer answers 200 with an errors array: an unknown field is a document that failed
// validation, not a transport fault. The match is deliberately narrow — a field name we sent
// has to appear alongside the complaint — so an unrelated validation error is NOT reported as
// version skew, which would send an operator looking at deployments over a typo.
func isArchiveSkew(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	if !strings.Contains(text, "Cannot query field") && !strings.Contains(text, "no such field") {
		return false
	}
	for _, field := range []string{"geoFenceSetManifest", "currentGeoFenceSetManifest", "geoFenceGeometry"} {
		if strings.Contains(text, field) {
			return true
		}
	}
	return false
}

// fetchManifest reads one version's manifest, or the current one when version is non-positive.
func (c *fenceSetClient) fetchManifest(ctx context.Context, tenant string, version int32) (*dmmodel.GeoFenceSetManifest, error) {
	var fences []entry
	var got int32
	if version > 0 {
		var out geoFenceSetManifestResponse
		if err := c.exec(ctx, tenant, geoFenceSetManifestQuery, map[string]any{"version": version}, &out); err != nil {
			return nil, classifyArchiveError(err)
		}
		fences, got = out.GeoFenceSetManifest.Fences, out.GeoFenceSetManifest.Version
	} else {
		var out currentGeoFenceSetManifestResponse
		if err := c.exec(ctx, tenant, currentGeoFenceSetManifestQuery, nil, &out); err != nil {
			return nil, classifyArchiveError(err)
		}
		fences, got = out.CurrentGeoFenceSetManifest.Fences, out.CurrentGeoFenceSetManifest.Version
	}
	// A version-addressed read that answers about a DIFFERENT version is a fault, not a
	// surprise to absorb: the whole point of addressing by version is that the answer is
	// deterministic, and filing another version's fences under the number asked for is the
	// failure the stamp exists to prevent.
	if version > 0 && got != version {
		return nil, fmt.Errorf("asked device-management for fence-set version %d and it answered about version %d",
			version, got)
	}
	manifest := &dmmodel.GeoFenceSetManifest{
		Version: got,
		Fences:  make([]dmmodel.GeoFenceManifestEntry, 0, len(fences)),
	}
	for _, f := range fences {
		if f.Token == "" || f.Hash == "" {
			return nil, fmt.Errorf("fence-set version %d carries a manifest entry with an empty token or address (%q -> %q)",
				got, f.Token, f.Hash)
		}
		manifest.Fences = append(manifest.Fences, dmmodel.GeoFenceManifestEntry{Token: f.Token, Hash: f.Hash})
	}
	return manifest, nil
}

// classifyArchiveError turns a peer error into errArchiveSkew when that is what it is, and
// otherwise passes it through unchanged.
func classifyArchiveError(err error) error {
	if isArchiveSkew(err) {
		return fmt.Errorf("%w: %v", errArchiveSkew, err)
	}
	return err
}

// geometryFetch is one batched geometry read: a transport, the tenant it runs for, and the
// request budget shared across every chunk and every split it makes.
type geometryFetch struct {
	client *fenceSetClient
	tenant string
	spent  int
}

// documents resolves the given addresses into their stored documents.
//
// 🔴 EVERY RETURNED DOCUMENT IS VERIFIED AGAINST THE ADDRESS IT WAS ASKED FOR, AND A MISMATCH
// IS FATAL TO THE FETCH RATHER THAN TO ONE FENCE. The address is the SHA-256 of the document,
// so re-deriving it is an INDEPENDENT oracle for a class of failure that has no other
// detector: a peer serving the wrong row, or anything on the path re-encoding the JSON, would
// otherwise install geometry that compiles cleanly and answers containment confidently and
// wrongly. It is fatal to the whole fetch because none of those causes is per-document — if
// one body came back wrong, the reason applies to all of them — and because a verified body
// is about to enter a cache that outlives every version, where no reconcile sweep would ever
// revisit it.
//
// An address the archive does not hold is simply ABSENT from the result. That is not an error
// here: the caller turns a missing body into a fence carrying its error, which is what keeps
// an unresolvable fence reporting rather than silently vanishing.
func (g *geometryFetch) documents(ctx context.Context, hashes []string) (map[string]json.RawMessage, error) {
	found := make(map[string]json.RawMessage, len(hashes))
	for start := 0; start < len(hashes); start += geometryChunkSize {
		end := start + geometryChunkSize
		if end > len(hashes) {
			end = len(hashes)
		}
		if err := g.chunk(ctx, hashes[start:end], found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

// chunk resolves one chunk, splitting it and retrying when the peer's response is too large.
//
// 🔑 SPLITTING IS TRIVIAL HERE BECAUSE THE REQUEST NAMES ITS ITEMS. The paged read this
// replaces addressed its work as (pageNumber, pageSize), which cannot be re-expressed at a
// different size — so a refusal forced it to restart the entire walk from page one at a halved
// size. A chunk is a SET OF ADDRESSES, so half of it is still a well-formed request for
// exactly the items that have not been answered, and nothing already fetched is thrown away.
//
// The split still has to exist: MaxGeoFenceGeometryBytes bounds documents written since it was
// introduced, and rows predating it can be larger. It terminates at a single address, whose
// refusal is a genuine error — one document that will not fit one response cannot be fetched
// by any subdivision, and saying so beats looping.
func (g *geometryFetch) chunk(ctx context.Context, hashes []string, into map[string]json.RawMessage) error {
	if len(hashes) == 0 {
		return nil
	}
	if g.spent >= maxGeometryRequests {
		return fmt.Errorf("geofence geometry read for tenant %q exceeded its budget of %d requests",
			g.tenant, maxGeometryRequests)
	}
	g.spent++

	var out geoFenceGeometryResponse
	err := g.client.exec(ctx, g.tenant, geoFenceGeometryQuery, map[string]any{"hashes": hashes}, &out)
	if err != nil {
		if !errors.Is(err, svcclient.ErrResponseTooLarge) {
			return classifyArchiveError(err)
		}
		if len(hashes) == 1 {
			return fmt.Errorf("geofence geometry %s is too large for one response on its own: %w",
				hashes[0], err)
		}
		mid := len(hashes) / 2
		if err := g.chunk(ctx, hashes[:mid], into); err != nil {
			return err
		}
		return g.chunk(ctx, hashes[mid:], into)
	}

	asked := make(map[string]struct{}, len(hashes))
	for _, h := range hashes {
		asked[h] = struct{}{}
	}
	for _, doc := range out.GeoFenceGeometry {
		if _, wanted := asked[doc.Hash]; !wanted {
			return fmt.Errorf("device-management answered with geometry %s, which was not asked for",
				doc.Hash)
		}
		raw := json.RawMessage(doc.Geometry)
		if got := dmmodel.GeoFenceGeometryHash([]byte(doc.Geometry)); got != doc.Hash {
			return fmt.Errorf("%w: asked for geometry %s and the document served under it hashes to %s",
				errGeometryHashMismatch, doc.Hash, got)
		}
		into[doc.Hash] = raw
	}
	return nil
}

// errGeometryHashMismatch reports a served document that did not hash to the address it was
// requested under. It is its own error so the caller can COUNT it separately: it is never
// transient and never benign, unlike every other way a fetch can fail.
var errGeometryHashMismatch = errors.New("geofence geometry does not match its content address")
