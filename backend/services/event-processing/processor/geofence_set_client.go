// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// fenceSetPageSize is how many fences one page of the frozen-set read asks for.
//
// 🔴 IT IS SIZED AGAINST THE CLIENT'S READ CAP, NOT AGAINST ROUND-TRIP COUNT.
// svcclient.maxResponseBytes refuses a response over 1 MiB, and one fence at the authoring
// ceiling (device-management's MaxGeoFenceVertices = 512 positions) is roughly 10 KB of
// coordinate text before the GraphQL response escapes it into a JSON string, which is where
// the worst case lands nearer 20 KB. 25 of those is about half the cap — headroom enough
// that a tenant sitting exactly on the documented limits is not one geometry document away
// from the wall again. At the fence-count ceiling (MaxGeoFencesPerTenant = 100) that is
// four round trips, taken on a startup reconcile or a fence edit, never on the DETECT loop.
const fenceSetPageSize = 25

// maxFenceSetPages bounds the paging loop, so a peer that reports a totalRecords its pages
// never reach cannot spin this goroutine forever. It is deliberately far above the reachable
// page count (MaxGeoFencesPerTenant / fenceSetPageSize = 4): it is a runaway stop, not a
// second fence-count limit, and sizing it near the real bound would turn a raised authoring
// ceiling into a silently truncated fence set.
const maxFenceSetPages = 64

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
// Only its FIRST page is read through this query; see CurrentFenceSet for why the rest are
// read by version instead.
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
// It is the ONE thing the paging loops below need from a transport, which is what lets those
// loops be shared by the production HTTP client and by a test that drives device-management's
// real schema in-process: the paging, the stitching and the completeness check are then the
// same code in both, rather than a stub that skips the step under test.
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
func pageVars(page int) map[string]any {
	return map[string]any{"pageNumber": page, "pageSize": fenceSetPageSize}
}

// fetchRemainingPages reads pages 2..N of ONE fence-set version and appends them to acc,
// stopping when the accumulated count reaches total.
//
// It reads by VERSION even when the first page came from currentGeoFenceSet, and that is the
// whole reason the two entry points below are not one function. A version's snapshot is frozen
// at mint and nothing rewrites it, so pages taken from it minutes apart belong to the same set
// by construction; "current" is a moving target, and a fence edit between two of its pages
// would stitch half of version 7 onto half of version 8 and file the result under one number.
//
// A page that returns nothing while the total says otherwise is an ERROR rather than a short
// set: the caller turns a returned error into ErrNoFenceSet, which containment reports as
// unknowable and the runtime counts. Returning what was read so far would present a truncated
// fence set as the tenant's fence set, which nothing downstream can detect.
func fetchRemainingPages(ctx context.Context, exec fenceSetExec, tenant string, version int32,
	acc []geofence.SnapshotFence, total int32) ([]geofence.SnapshotFence, error) {
	for page := 2; int32(len(acc)) < total; page++ {
		if page > maxFenceSetPages {
			return nil, fmt.Errorf("geofence set version %d did not complete within %d pages (%d of %d fences read)",
				version, maxFenceSetPages, len(acc), total)
		}
		var out geoFenceSetSnapshotResponse
		vars := map[string]any{"version": version, "pagination": pageVars(page)}
		if err := exec(ctx, tenant, geoFenceSetSnapshotQuery, vars, &out); err != nil {
			return nil, err
		}
		if len(out.GeoFenceSetSnapshot.Fences.Results) == 0 {
			return nil, fmt.Errorf("geofence set version %d reported %d fences but page %d was empty after %d",
				version, total, page, len(acc))
		}
		acc = out.GeoFenceSetSnapshot.Fences.appendTo(acc)
	}
	return acc, nil
}

// fetchSnapshotAt reads the WHOLE frozen fence set of one version, paging until complete.
func fetchSnapshotAt(ctx context.Context, exec fenceSetExec, tenant string, version int32) (*geofence.FenceSet, error) {
	var out geoFenceSetSnapshotResponse
	vars := map[string]any{"version": version, "pagination": pageVars(1)}
	if err := exec(ctx, tenant, geoFenceSetSnapshotQuery, vars, &out); err != nil {
		return nil, err
	}
	total, err := out.GeoFenceSetSnapshot.Fences.Pagination.total()
	if err != nil {
		return nil, err
	}
	fences := out.GeoFenceSetSnapshot.Fences.appendTo(make([]geofence.SnapshotFence, 0, total))
	// Paged by the version ASKED FOR, not by the one the payload reports. They agree, but
	// only the requested one is the key the archive was addressed by, so continuing on it
	// cannot follow a payload's own version field somewhere else.
	fences, err = fetchRemainingPages(ctx, exec, tenant, version, fences, total)
	if err != nil {
		return nil, err
	}
	// A fence whose geometry cannot be compiled is retained WITH its error by NewFenceSet
	// (never dropped), so one malformed fence cannot disable containment for every other
	// fence the tenant owns.
	return geofence.NewFenceSet(out.GeoFenceSetSnapshot.Version, fences), nil
}

// fetchCurrentSnapshot reads the WHOLE frozen fence set of the tenant's CURRENT version: page
// one from currentGeoFenceSet (which reads the version and its fences from one row, so the two
// cannot disagree), then the rest addressed by the version that page reported.
func fetchCurrentSnapshot(ctx context.Context, exec fenceSetExec, tenant string) (*geofence.FenceSet, error) {
	var out currentGeoFenceSetResponse
	vars := map[string]any{"pagination": pageVars(1)}
	if err := exec(ctx, tenant, currentGeoFenceSetQuery, vars, &out); err != nil {
		return nil, err
	}
	total, err := out.CurrentGeoFenceSet.Fences.Pagination.total()
	if err != nil {
		return nil, err
	}
	fences := out.CurrentGeoFenceSet.Fences.appendTo(make([]geofence.SnapshotFence, 0, total))
	fences, err = fetchRemainingPages(ctx, exec, tenant, out.CurrentGeoFenceSet.Version, fences, total)
	if err != nil {
		return nil, err
	}
	return geofence.NewFenceSet(out.CurrentGeoFenceSet.Version, fences), nil
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
// which is exactly what its events' version-0 stamp means, and which pages to completion in
// one round trip because its total is zero.
func (c *fenceSetClient) CurrentFenceSet(ctx context.Context, tenant string) (*geofence.FenceSet, error) {
	return fetchCurrentSnapshot(ctx, c.exec, tenant)
}

// compile-time assertions that the client satisfies both runtime contracts.
var (
	_ runtime.FenceSetSource        = (*fenceSetClient)(nil)
	_ runtime.CurrentFenceSetSource = (*fenceSetClient)(nil)
)
