// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// geoFenceSetSnapshotQuery reads the FROZEN fence set of one fence-set version from
// device-management (ADR-078). It carries NO tenant argument: the tenant travels as the
// service-token client's tenant header, and device-management's rows are tenant-scoped, so
// the set of fence sets this query can reach is decided by the header and not by anything
// this service can put in the request. Asking for another tenant's fence set is therefore
// not an expressible request rather than a refused one.
const geoFenceSetSnapshotQuery = `query($version: Int!) {
  geoFenceSetSnapshot(version: $version) { version fences { token geometry } }
}`

// currentGeoFenceSetQuery reads the tenant's CURRENT frozen fence set — version and fences
// together from one row, so a fence edit landing mid-read cannot yield a set filed under a
// version that is not its own. Same tenancy story as above.
const currentGeoFenceSetQuery = `query {
  currentGeoFenceSet { version fences { token geometry } }
}`

// fenceSetClient is event-processing's seam onto device-management's frozen fence-set
// archive, over the ADR-044 service-token client (least-privilege device:read — the same
// authority the fence CRUD reads take, because this is the same material).
//
// 🔴 IT IS THE FETCH PATH, NEVER THE EVALUATION PATH. The live fan-out reads the loop-owned
// FenceSetView, which is pure memory and is fed by the fence-set fact stream; this client is
// how a version the view does NOT hold gets resolved — an authoring preview replaying last
// week, a startup reconcile after a restart. Every one of those callers is off the
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

// snapshotPayload is the wire shape of a frozen fence set. Geometry stays a raw string: it is
// an opaque self-describing document that only the geofence compiler reads, so decoding it
// here would be a second parser to keep in step with that one.
type snapshotPayload struct {
	Version int32 `json:"version"`
	Fences  []struct {
		Token    string `json:"token"`
		Geometry string `json:"geometry"`
	} `json:"fences"`
}

// compile turns a decoded wire snapshot into an evaluable fence set. A fence whose geometry
// cannot be compiled is retained WITH its error by NewFenceSet (never dropped), so one
// malformed fence cannot disable containment for every other fence the tenant owns.
func (s snapshotPayload) compile() *geofence.FenceSet {
	fences := make([]geofence.SnapshotFence, 0, len(s.Fences))
	for _, f := range s.Fences {
		fences = append(fences, geofence.SnapshotFence{Token: f.Token, Geometry: json.RawMessage(f.Geometry)})
	}
	return geofence.NewFenceSet(s.Version, fences)
}

// FenceSetAt resolves the frozen fence set of one (tenant, version).
//
// An error is returned rather than an empty set for every failure — an unknown version, a
// transport failure, a refused authority. That direction is load-bearing: the caller
// (LoadingFenceSets) turns a nil into ErrNoFenceSet, which the predicate reports as
// "unknowable" and the runtime counts as an eval error. Returning an empty set on failure
// would present "the fence set could not be read" as "the tenant has no fences", which reads
// downstream as a quiet, healthy, never-firing rule.
func (c *fenceSetClient) FenceSetAt(ctx context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	var out geoFenceSetSnapshotResponse
	vars := map[string]any{"version": version}
	if err := c.client.Query(ctx, c.url, tenant, geoFenceSetSnapshotQuery, vars, &out); err != nil {
		return nil, err
	}
	return out.GeoFenceSetSnapshot.compile(), nil
}

// CurrentFenceSet resolves the tenant's current frozen fence set, for the startup reconcile.
// A tenant that has never had a fence answers version 0 with no fences — the known-empty set,
// which is exactly what its events' version-0 stamp means.
func (c *fenceSetClient) CurrentFenceSet(ctx context.Context, tenant string) (*geofence.FenceSet, error) {
	var out currentGeoFenceSetResponse
	if err := c.client.Query(ctx, c.url, tenant, currentGeoFenceSetQuery, nil, &out); err != nil {
		return nil, err
	}
	return out.CurrentGeoFenceSet.compile(), nil
}

// compile-time assertions that the client satisfies both runtime contracts.
var (
	_ runtime.FenceSetSource        = (*fenceSetClient)(nil)
	_ runtime.CurrentFenceSetSource = (*fenceSetClient)(nil)
)
