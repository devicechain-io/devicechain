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
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
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
// It is a RUNAWAY STOP, not a second size limit, so it is sized well above anything reachable.
//
// 🔴 IT IS DERIVED FROM THE PLATFORM MAXIMUM FENCE COUNT, AND IT USED TO BE A LITERAL 512
// JUSTIFIED BY A FENCE CEILING OF 100. That ceiling is now a tier setting, so the worst
// LEGITIMATE fetch is one distinct document per fence at governance.MaxGeoFenceCeiling
// fences — 4,000 of them, which would have blown straight through a 512-request stop. A
// runaway stop that fires on legitimate work is not a stop; it is an outage for whichever
// tenant an operator packaged generously, surfacing as a geometry read that errors.
//
// 🔴 THE MULTIPLIER IS 3 BECAUSE THERE ARE TWO CONSUMERS, AND SIZING IT FOR ONE LEFT 1.9%
// HEADROOM. Written out, with N = governance.MaxGeoFenceCeiling and c = geometryChunkSize:
//
//   - the SPLIT. A chunk refused and subdivided to leaves costs 2c-1 requests, so the whole
//     set costs ceil(N/c) * (2c-1), which is strictly below 2N and approaches it from below.
//     At today's constants that is 7,849 — under a 2N budget of 8,000 by 151 requests.
//   - the LATE FETCH. rebuild's cache-fill callback re-fetches, one address at a time and
//     through THIS SAME budget, any address its plan held as cached that was evicted before
//     Get asked for it — deliberately, so a tenant whose working set exceeds the cache bound
//     does not manufacture unresolvable fences forever. In the pathological case that is up
//     to N more requests, and 2N cannot pay for it.
//
// So 2N is the bound for the split ALONE, which is what made the margin look like slack when
// it was really an unpriced second consumer. 3N covers both: the exact worst case is
// 3N - ceil(N/c), so the budget clears it by ceil(N/c) — 167 requests today, 1.3%.
//
// 🔴 THAT MARGIN IS THIN ON PURPOSE, AND WIDENING IT WOULD COST THE PROPERTY THAT MAKES IT
// SAFE. Because the budget sits just above an EXACT bound, any change to geometryChunkSize, or
// a third consumer of the budget, fails the test rather than quietly eating the cushion — which
// is precisely how the 512 this replaced survived a 40x change in the fence ceiling. A budget
// several times the worst case would absorb such a change in silence, and would also let a peer
// making expensive progress spin for correspondingly longer before the stop fires.
//
// The relation is asserted rather than described: see
// TestTheRequestBudgetSitsAboveTheLargestLegitimateFetch, which models both consumers and
// carries a counterweight so an arbitrarily large budget fails too — a stop far above anything
// reachable lets a peer making expensive progress spin for a long time before it fires.
//
// It ERRORS rather than returning what it has: a short geometry set is indistinguishable
// downstream from a tenant who really has that many fences.
const maxGeometryRequests = governance.MaxGeoFenceCeiling * 3

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
			return nil, c.classifyArchiveError(err)
		}
		fences, got = out.GeoFenceSetManifest.Fences, out.GeoFenceSetManifest.Version
	} else {
		var out currentGeoFenceSetManifestResponse
		if err := c.exec(ctx, tenant, currentGeoFenceSetManifestQuery, nil, &out); err != nil {
			return nil, c.classifyArchiveError(err)
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

// classifyArchiveError turns a peer error into errArchiveSkew when that is what it is, COUNTS
// it, and otherwise passes it through unchanged.
//
// 🔴 THE COUNTING HAPPENS WHERE THE ERROR IS RAISED, AND THE FIRST VERSION OF THIS DID IT
// WHERE THE ERROR WAS HANDLED — WHICH MADE THE COUNTER UNREACHABLE ON THE ROAD IT EXISTS FOR.
// Skew was recorded in assemble, but a manifest-door failure is classified here and returned
// straight to FenceSetAt/CurrentFenceSet, which never reach assemble. Those two are precisely
// the reconcile and startup roads — the ones that ask a MANIFEST door first — so the case the
// metric was built for, a device-management rolled back behind this service, was reported in
// the log and counted nowhere. An alert had already been written against it.
//
// Recording at the raise site is what makes that structurally impossible to reintroduce: there
// is one place an unknown-field error becomes errArchiveSkew, and it is this one.
func (c *fenceSetClient) classifyArchiveError(err error) error {
	if isArchiveSkew(err) {
		c.metrics.recordFenceArchiveSkew()
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
			return g.client.classifyArchiveError(err)
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
			// Counted here rather than where this error is handled, for the reason
			// classifyArchiveError records: a counter incremented at the handling site is
			// only as reachable as that site, and this one must fire wherever a body is
			// verified.
			g.client.metrics.recordFenceGeometryHashMismatch()
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

// assemble turns a manifest into an evaluable fence set, resolving each entry's geometry
// through the compiled-geometry cache and fetching in one batch whatever the cache does not
// already hold.
//
// 🔴 THE FAILURE TAXONOMY IS THE WHOLE DESIGN, AND THE TWO HALVES MUST NOT BE COLLAPSED.
//
//   - A failure that is ABOUT THE READ — transport, budget, version skew, or a document that
//     did not match its content address — returns an error and installs NOTHING. The version
//     simply does not arrive, which is what a failed archive read has always meant here, and
//     the reconcile sweep retries it. None of those causes is per-fence: if the archive is
//     unreachable for one body it is unreachable for all of them, and pretending otherwise
//     would spend a retention slot on a set that is uniformly useless.
//   - A body the archive does not HOLD is per-fence, and that fence is installed CARRYING ITS
//     ERROR. This is the case that only exists because geometry stopped travelling with the
//     fact, and it is the one that must never be expressed as a missing fence: an absent fence
//     reads downstream as "no such fence", which is a rule naming something that does not
//     exist — indistinguishable from a healthy rule that never fires. An errored fence reports
//     that it could not be evaluated and lands on the eval-error counter. Same hole, opposite
//     visibility, and only one of them is discoverable.
//
// The cache is consulted twice on purpose: once advisorily, to size the batch to what is
// actually missing, and then again per fence through Get, which is the only door that verifies,
// single-flights and refreshes recency. Held may be wrong by the time Get runs in BOTH
// directions, and neither may be allowed to become an answer: an entry that arrived after the
// plan costs a document fetched and not needed, and an entry EVICTED after the plan is refetched
// on its own rather than reported as absent. The second is the one that bites — see the fetch
// closure below.
func (c *fenceSetClient) assemble(ctx context.Context, tenant string, manifest *dmmodel.GeoFenceSetManifest) (*geofence.FenceSet, error) {
	if len(manifest.Fences) == 0 {
		return geofence.EmptyFenceSet(manifest.Version), nil
	}

	unique := make([]string, 0, len(manifest.Fences))
	seen := make(map[string]struct{}, len(manifest.Fences))
	for _, entry := range manifest.Fences {
		if _, dup := seen[entry.Hash]; dup {
			continue
		}
		seen[entry.Hash] = struct{}{}
		unique = append(unique, entry.Hash)
	}

	held := c.cache.Held(tenant, unique)
	missing := make([]string, 0, len(unique))
	for _, hash := range unique {
		if !held[hash] {
			missing = append(missing, hash)
		}
	}
	before := c.cache.Stats()

	fetch := &geometryFetch{client: c, tenant: tenant}
	documents, err := fetch.documents(ctx, missing)
	if err != nil {
		// Not counted here: both classes that have their own counter — version skew and a
		// hash mismatch — are recorded at the point they are RAISED, so they are counted on
		// every road rather than only on the ones that funnel through this function.
		return nil, err
	}

	fences := make([]*geofence.Fence, 0, len(manifest.Fences))
	unresolved := 0
	for _, entry := range manifest.Fences {
		compiled, err := c.cache.Get(ctx, tenant, entry.Hash, func(ctx context.Context) ([]byte, error) {
			if document, ok := documents[entry.Hash]; ok {
				return document, nil
			}
			// 🔴 REACHING HERE MEANS THE PLAN WAS STALE, NOT THAT THE ARCHIVE IS MISSING A
			// BODY, AND CONFLATING THE TWO INSTALLS A FENCE THAT REPORTS A LIE. Held is
			// advisory: it said this address was cached, so it was left out of the batch, and
			// by the time Get asked it had been evicted. That is not hypothetical — this
			// function's OWN admits can evict entries it planned as held, so a tenant whose
			// working set exceeds the cache bound would otherwise manufacture unresolvable
			// fences on every rebuild, and the reconcile sweep would hit the same wall forever.
			//
			// Fetching the one address through the SAME geometryFetch keeps the request budget
			// shared, so even a pathological run terminates. Absence is reported only after
			// asking the archive directly for it.
			late, err := fetch.documents(ctx, []string{entry.Hash})
			if err != nil {
				return nil, err
			}
			if document, ok := late[entry.Hash]; ok {
				return document, nil
			}
			// The archive answered and did not hold this address. Returning an error is what
			// keeps the cache free of negatives — nothing is retained for a document that
			// never arrived.
			return nil, fmt.Errorf("device-management holds no geometry under %s", entry.Hash)
		})
		if err != nil {
			unresolved++
			fences = append(fences, geofence.NewErrorFence(entry.Token, fmt.Errorf(
				"geofence %q names geometry %s, which could not be resolved: %w",
				entry.Token, entry.Hash, err)))
			continue
		}
		fences = append(fences, geofence.NewCompiledFence(entry.Token, compiled))
	}

	after := c.cache.Stats()
	c.metrics.recordFenceGeometryCache(
		int(after.Hits-before.Hits), int(after.Misses-before.Misses),
		int(after.Evictions-before.Evictions), int64(after.Vertices))
	c.metrics.recordFenceGeometryUnresolved(unresolved)
	if unresolved > 0 {
		log.Warn().Str("tenant", tenant).Int32("version", manifest.Version).
			Int("unresolved", unresolved).Int("fences", len(manifest.Fences)).
			Msg("Installing a geofence set whose geometry could not be fully resolved. The affected fences report an evaluation error rather than answering, so a rule naming one is counted rather than silently reading as outside. The next reconcile sweep retries them.")
	}
	return geofence.NewFenceSetFromFences(manifest.Version, fences), nil
}
