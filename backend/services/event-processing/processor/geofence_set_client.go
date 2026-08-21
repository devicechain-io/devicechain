// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// fenceSetPageSize is the page size the frozen-set read OPENS with. It is a starting point,
// not a bound — the bound is discovered by the walk below, and it is in bytes.
//
// 🔴 PAGING IS FORCED BY ARITHMETIC, NOT CHOSEN FOR TIDINESS, so this number is about round
// trips and nothing else. A fence set at device-management's documented limits is larger than
// svcclient.MaxResponseBytes at every coordinate precision anyone would use: 100 fences × 512
// positions is ~980 KB of stored GeoJSON at five decimal places (~1.1 m, already below useful
// geofencing precision) and 1,105,765 bytes once the schema serializes it, against a 1,048,576
// -byte cap. There is no per-fence bound that makes the whole set fit in one response while
// MaxGeoFenceVertices stays at 512 — one tight enough would be below a legitimate fence.
//
// 🔴 AND A PAGE SIZE COUNTED IN FENCES CANNOT DEFEND A CAP COUNTED IN BYTES. MaxGeoFenceVertices
// bounds positions, not size; only MaxGeoFenceGeometryBytes says anything about what a response
// weighs, and even under it 25 fences at the byte ceiling is ~800 KB — inside the cap, but not
// by much. Against that, an ordinary fence set is nowhere near it: a fence at the vertex ceiling
// at nine decimal places stores at ~13.9 KB and gains about 9% in GraphQL string escaping, so 25
// of those is ~380 KB and the whole ceiling set is four round trips, taken on a startup
// reconcile or a fence edit, never on the DETECT loop.
//
// So 25 makes the COMMON case cheap, and fenceWalk is what makes the pathological case work: a
// page the peer refuses for being too large halves this and tries again, down to one fence.
//
// All of this is expected to be temporary. The root cause is that a coordinate is JSON text of
// unbounded length; storing coordinates as int32 degrees × 10^7 puts the whole ceiling set at
// ~411 KB, inside one response, and this walk could be deleted rather than tuned. That change is
// scheduled for v0.13.0 — see MaxGeoFenceGeometryBytes.
const fenceSetPageSize = 25

// maxFenceSetResponses bounds the total GraphQL responses one fence-set read may spend, across
// every page and every halved retry, so a peer that reports a totalRecords its pages never
// reach cannot spin this goroutine forever.
//
// It is a RUNAWAY STOP, not a second fence-count limit, so it is sized well above anything
// reachable: the worst legitimate walk is MaxGeoFencesPerTenant fences at a page size of one,
// i.e. 100 responses, plus the few spent on attempts refused on the way down from 25 (a
// refused attempt aborts at the page that was refused, so it costs pages, not whole walks).
// 512 leaves that room several times over, and it ERRORS rather than truncating when it runs
// out — a short fence set is indistinguishable downstream from a small one.
const maxFenceSetResponses = 512

// geoFenceSetSnapshotQuery reads the FROZEN fence set of one fence-set version from
// device-management (ADR-078). It carries NO tenant argument: the tenant travels as the
// service-token client's tenant header, and device-management's rows are tenant-scoped, so
// the set of fence sets this query can reach is decided by the header and not by anything
// this service can put in the request. Asking for another tenant's fence set is therefore
// not an expressible request rather than a refused one.
const geoFenceSetSnapshotQuery = `query($version: Int!, $pagination: PaginationInput!) {
  geoFenceSetSnapshot(version: $version) {
    version
    fences(pagination: $pagination) {
      results { token geometry }
      pagination { pageStart pageEnd totalRecords }
    }
  }
}`

// currentGeoFenceSetQuery reads the tenant's CURRENT frozen fence set — version and fences
// together from one row, so a fence edit landing mid-read cannot yield a set filed under a
// version that is not its own. Same tenancy story as above.
//
// Only its FIRST page is read through this query; see fetchCurrentSnapshot for why the rest
// are read by version instead.
const currentGeoFenceSetQuery = `query($pagination: PaginationInput!) {
  currentGeoFenceSet {
    version
    fences(pagination: $pagination) {
      results { token geometry }
      pagination { pageStart pageEnd totalRecords }
    }
  }
}`

// fenceSetClient is event-processing's seam onto device-management's frozen fence-set
// archive, over the ADR-044 service-token client (least-privilege device:read — the same
// authority the fence CRUD reads take, because this is the same material).
//
// 🔴 IT IS THE FETCH PATH, NEVER THE EVALUATION PATH. The live fan-out reads the loop-owned
// FenceSetView, which is pure memory and is fed by the fence-set fact stream; this client is
// how a version the view does NOT hold gets resolved — an authoring preview replaying last
// week, a startup reconcile after a restart, a fact that arrived without its fences because
// the set outgrew the broker's per-message ceiling. Every one of those callers is off the
// single-writer loop and may block. Nothing here is reachable from the loop, which is why the
// hot path's answer to a miss stays a loud counted error rather than a network round trip
// taken while every tenant's event processing waits behind it.
type fenceSetClient struct {
	client *svcclient.Client
	url    string
}

// NewFenceSetClient builds the fence-set fetch seam over a service-token client and
// device-management's GraphQL URL (wired in main). It returns the two runtime interfaces it
// satisfies so the concrete adapter stays package-private: the version-addressed source the
// preview/replay path resolves through, and the current-set source the startup reconcile
// seeds from.
func NewFenceSetClient(client *svcclient.Client, url string) (runtime.FenceSetSource, runtime.CurrentFenceSetSource) {
	c := &fenceSetClient{client: client, url: url}
	return c, c
}

// fenceSetExec runs one fence-set query for a tenant and decodes its "data" object into out.
// It is the ONE thing the paging walk below needs from a transport, which is what lets that
// walk be shared by the production HTTP client and by a test that drives device-management's
// real schema in-process: the paging, the halving, the stitching and the completeness check
// are then the same code in both, rather than a stub that skips the step under test.
//
// An implementation MUST report a response it could not carry as svcclient.ErrResponseTooLarge
// (errors.Is-comparable). That is the signal the walk halves on; any other error is terminal.
type fenceSetExec func(ctx context.Context, tenant, query string, vars map[string]any, out any) error

// exec is the production transport: a service-token GraphQL call over HTTP.
func (c *fenceSetClient) exec(ctx context.Context, tenant, query string, vars map[string]any, out any) error {
	return c.client.Query(ctx, c.url, tenant, query, vars, out)
}

// geoFenceSetSnapshotResponse / currentGeoFenceSetResponse are the decoded "data" objects of
// the two queries above. They are NAMED types rather than anonymous structs at each call site
// so a test can decode a response produced by device-management's REAL schema through exactly
// the mapping the client uses — a field-name drift between the two services then fails a test
// instead of silently decoding to an empty fence set at runtime.
type geoFenceSetSnapshotResponse struct {
	GeoFenceSetSnapshot snapshotPayload `json:"geoFenceSetSnapshot"`
}

type currentGeoFenceSetResponse struct {
	CurrentGeoFenceSet snapshotPayload `json:"currentGeoFenceSet"`
}

// snapshotPayload is the wire shape of one PAGE of a frozen fence set.
type snapshotPayload struct {
	Version int32     `json:"version"`
	Fences  fencePage `json:"fences"`
}

// fencePage is one page of fences plus the pagination record that says how much of the set it
// covers. Geometry stays a raw string: it is an opaque self-describing document that only the
// geofence compiler reads, so decoding it here would be a second parser to keep in step with
// that one.
type fencePage struct {
	Results []struct {
		Token    string `json:"token"`
		Geometry string `json:"geometry"`
	} `json:"results"`
	Pagination pageInfo `json:"pagination"`
}

// pageInfo mirrors the schema's SearchResultsPagination, whose fields are NULLABLE Ints.
//
// 🔴 THE POINTERS ARE THE POINT. A missing totalRecords decoded into a plain int32 would be
// zero, the paging loop would conclude it had read the whole set after one page, and a
// hundred fences would resolve to twenty-five — a truncated fence set is indistinguishable
// downstream from a tenant who really has that many fences, so it would evaluate quietly and
// wrongly forever. Nil is therefore an ERROR here, not a default.
type pageInfo struct {
	PageStart    *int32 `json:"pageStart"`
	PageEnd      *int32 `json:"pageEnd"`
	TotalRecords *int32 `json:"totalRecords"`
}

// total returns the reported total record count, or an error when the peer did not report one.
func (p pageInfo) total() (int32, error) {
	if p.TotalRecords == nil {
		return 0, fmt.Errorf("geofence set page carried no totalRecords; cannot tell a complete set from a truncated one")
	}
	return *p.TotalRecords, nil
}

// appendTo accumulates one page's fences onto acc, in the order the archive froze them.
func (f fencePage) appendTo(acc []geofence.SnapshotFence) []geofence.SnapshotFence {
	for _, r := range f.Results {
		acc = append(acc, geofence.SnapshotFence{Token: r.Token, Geometry: json.RawMessage(r.Geometry)})
	}
	return acc
}

// pageVars builds the pagination input for one page.
func pageVars(pageNumber, pageSize int32) map[string]any {
	return map[string]any{"pageNumber": pageNumber, "pageSize": pageSize}
}

// fenceWalk is one fence-set read: a transport, the tenant it runs for, and the response
// budget shared across every attempt it makes.
//
// 🔴 IT RETRIES AT A SMALLER PAGE SIZE, AND THAT IS WHAT MAKES THE READ TOTAL. A page size
// counted in fences is a guess about bytes, and svcclient.MaxResponseBytes is what the guess
// is aimed at. Any fixed guess can be wrong: a coordinate is a JSON number of unbounded
// length and a position may carry ordinates beyond [lon, lat], so the same 100 × 512 fence set
// is ~670 KB at two decimal places and ~2.5 MB at twenty. Rather than assume a number, the
// walk asks and halves when the answer is "too large" — down to one fence, which
// device-management's MaxGeoFenceGeometryBytes guarantees fits with room to spare. Below one
// there is nothing left to halve, which is exactly why that authoring bound has to exist: a
// reader cannot page its way under a cap that one row already exceeds.
//
// The halving is also what makes this correct for fences ALREADY STORED. MaxGeoFenceGeometryBytes
// binds new writes; rows written before it can be any size, and those sets have to stay readable.
type fenceWalk struct {
	exec   fenceSetExec
	tenant string
	// spent counts responses across ALL attempts, so the halving retries cannot escape the
	// runaway bound by starting over.
	spent int
}

// query runs one GraphQL request against the walk's budget.
func (w *fenceWalk) query(ctx context.Context, query string, vars map[string]any, out any) error {
	if w.spent >= maxFenceSetResponses {
		return fmt.Errorf("geofence set read for tenant %q did not complete within %d responses",
			w.tenant, maxFenceSetResponses)
	}
	w.spent++
	return w.exec(ctx, w.tenant, query, vars, out)
}

// snapshotPage reads one page of one VERSION.
func (w *fenceWalk) snapshotPage(ctx context.Context, version, pageNumber, pageSize int32) (snapshotPayload, error) {
	var out geoFenceSetSnapshotResponse
	vars := map[string]any{"version": version, "pagination": pageVars(pageNumber, pageSize)}
	if err := w.query(ctx, geoFenceSetSnapshotQuery, vars, &out); err != nil {
		return snapshotPayload{}, err
	}
	return out.GeoFenceSetSnapshot, nil
}

// versionPagesFrom walks ONE fence-set version at ONE page size, appending to acc from
// pageNumber onward, until the accumulated count reaches total.
//
// It reads by VERSION rather than by "current", and that is the reason the two entry points
// below are not one function. A version's snapshot is frozen at mint and nothing rewrites it,
// so pages taken from it minutes apart belong to the same set by construction; "current" is a
// moving target, and a fence edit between two of its pages would stitch half of version 7 onto
// half of version 8 and file the result under one number.
//
// A page that returns nothing while the total says otherwise is an ERROR rather than a short
// set: the caller turns a returned error into ErrNoFenceSet, which containment reports as
// unknowable and the runtime counts. Returning what was read so far would present a truncated
// fence set as the tenant's fence set, which nothing downstream can detect.
func (w *fenceWalk) versionPagesFrom(ctx context.Context, version, pageSize int32,
	acc []geofence.SnapshotFence, pageNumber, total int32) ([]geofence.SnapshotFence, error) {
	for ; int32(len(acc)) < total; pageNumber++ {
		page, err := w.snapshotPage(ctx, version, pageNumber, pageSize)
		if err != nil {
			return nil, err
		}
		if len(page.Fences.Results) == 0 {
			return nil, fmt.Errorf("geofence set version %d reported %d fences but page %d was empty after %d",
				version, total, pageNumber, len(acc))
		}
		acc = page.Fences.appendTo(acc)
	}
	return acc, nil
}

// halve returns the next page size to try, or 0 when there is nothing left to halve.
func halve(size int32) int32 {
	if size <= 1 {
		return 0
	}
	next := size / 2
	if next < 1 {
		next = 1
	}
	return next
}

// wholeVersion reads a whole fence-set version, halving the page size and starting the walk
// again whenever the peer refuses a response for being too large.
//
// It RESTARTS rather than continuing at the smaller size, and the reason is arithmetic, not
// caution: the wire addresses a page by (pageNumber, pageSize), so an offset reached at one
// size is not generally expressible at another — 25 fences in hand is not a whole number of
// 12-fence pages. Restarting is exact, and the version being IMMUTABLE is what makes starting
// over yield the same set rather than a different one. It costs nothing in any case that is
// not already pathological, because the first attempt succeeds for every fence set a real
// editor produces.
func (w *fenceWalk) wholeVersion(ctx context.Context, version int32) ([]geofence.SnapshotFence, error) {
	for size := int32(fenceSetPageSize); size > 0; size = halve(size) {
		fences, err := w.versionAtPageSize(ctx, version, size)
		if err == nil {
			return fences, nil
		}
		if !errors.Is(err, svcclient.ErrResponseTooLarge) || halve(size) == 0 {
			return nil, err
		}
	}
	// Unreachable: the loop returns on the size-1 attempt either way.
	return nil, fmt.Errorf("geofence set version %d could not be read at any page size", version)
}

// versionAtPageSize is one attempt: the whole version, from page one, at a fixed page size.
func (w *fenceWalk) versionAtPageSize(ctx context.Context, version, pageSize int32) ([]geofence.SnapshotFence, error) {
	first, err := w.snapshotPage(ctx, version, 1, pageSize)
	if err != nil {
		return nil, err
	}
	total, err := first.Fences.Pagination.total()
	if err != nil {
		return nil, err
	}
	fences := first.Fences.appendTo(make([]geofence.SnapshotFence, 0, total))
	return w.versionPagesFrom(ctx, version, pageSize, fences, 2, total)
}

// fetchSnapshotAt reads the WHOLE frozen fence set of one version.
func fetchSnapshotAt(ctx context.Context, exec fenceSetExec, tenant string, version int32) (*geofence.FenceSet, error) {
	w := &fenceWalk{exec: exec, tenant: tenant}
	fences, err := w.wholeVersion(ctx, version)
	if err != nil {
		return nil, err
	}
	// A fence whose geometry cannot be compiled is retained WITH its error by NewFenceSet
	// (never dropped), so one malformed fence cannot disable containment for every other fence
	// the tenant owns.
	//
	// Filed under the version ASKED FOR, not the one the payload reports. They agree, but only
	// the requested one is the key the archive was addressed by.
	return geofence.NewFenceSet(version, fences), nil
}

// fetchCurrentSnapshot reads the WHOLE frozen fence set of the tenant's CURRENT version: the
// first page from currentGeoFenceSet (which reads the version and its fences from one row, so
// the two cannot disagree), then — only if there is more — the rest addressed by the version
// that page reported.
//
// The first page is retried at a halved size like any other, and that is safe precisely
// because it is PAGE ONE: page one is page one at every page size, so a retry cannot land on a
// different offset. Everything after it is version-addressed, so no later page and no later
// retry can be answered from a fence set the first page did not come from.
func fetchCurrentSnapshot(ctx context.Context, exec fenceSetExec, tenant string) (*geofence.FenceSet, error) {
	w := &fenceWalk{exec: exec, tenant: tenant}

	var first snapshotPayload
	var total int32
	for size := int32(fenceSetPageSize); ; size = halve(size) {
		var out currentGeoFenceSetResponse
		err := w.query(ctx, currentGeoFenceSetQuery, map[string]any{"pagination": pageVars(1, size)}, &out)
		if err == nil {
			first = out.CurrentGeoFenceSet
			total, err = first.Fences.Pagination.total()
			if err == nil {
				break
			}
		}
		if !errors.Is(err, svcclient.ErrResponseTooLarge) || halve(size) == 0 {
			return nil, err
		}
	}

	// The common case: the whole set arrived in one response, and no second question is asked.
	if int32(len(first.Fences.Results)) >= total {
		return geofence.NewFenceSet(first.Version, first.Fences.appendTo(nil)), nil
	}

	// More to read. Hand the rest to the version-addressed walk, which re-reads page one at the
	// cost of one response. Paying it buys the guarantee: every page of the set, including any
	// read after a halving retry, is answered from the ONE version the first page named.
	fences, err := w.wholeVersion(ctx, first.Version)
	if err != nil {
		return nil, err
	}
	return geofence.NewFenceSet(first.Version, fences), nil
}

// FenceSetAt resolves the frozen fence set of one (tenant, version).
//
// An error is returned rather than an empty set for every failure — an unknown version, a
// transport failure, a refused authority, a set that would not page to completion. That
// direction is load-bearing: the caller (LoadingFenceSets) turns a nil into ErrNoFenceSet,
// which the predicate reports as "unknowable" and the runtime counts as an eval error.
// Returning an empty set on failure would present "the fence set could not be read" as "the
// tenant has no fences", which reads downstream as a quiet, healthy, never-firing rule.
func (c *fenceSetClient) FenceSetAt(ctx context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	return fetchSnapshotAt(ctx, c.exec, tenant, version)
}

// CurrentFenceSet resolves the tenant's current frozen fence set, for the startup reconcile.
// A tenant that has never had a fence answers version 0 with no fences — the known-empty set,
// which is exactly what its events' version-0 stamp means, and which completes in one round
// trip because its total is zero.
func (c *fenceSetClient) CurrentFenceSet(ctx context.Context, tenant string) (*geofence.FenceSet, error) {
	return fetchCurrentSnapshot(ctx, c.exec, tenant)
}

// compile-time assertions that the client satisfies both runtime contracts.
var (
	_ runtime.FenceSetSource        = (*fenceSetClient)(nil)
	_ runtime.CurrentFenceSetSource = (*fenceSetClient)(nil)
)
