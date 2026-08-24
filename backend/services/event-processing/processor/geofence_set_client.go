// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// fenceSetClient reads frozen fence sets out of device-management — the archive this service's
// live containment projection is a cache of.
//
// It is how a version the view does NOT hold gets resolved: an authoring preview replaying last
// week, a startup reconcile after a restart, the five-minute sweep, a tenant's first fence rule.
// Every one of those callers is off the single-writer loop and may block. Nothing here is
// reachable from the loop, which is why the hot path's answer to a miss stays a loud counted
// error rather than a network round trip taken while every tenant's event processing waits
// behind it.
//
// 🔴 IT NO LONGER PAGES, AND WHAT REPLACED THE PAGING IS NOT A BIGGER PAGE. The read used to
// walk a version's fences a page at a time and HALVE its page size whenever the response came
// back too large, because a page counted in fences is a guess about bytes and nothing bounded
// what one fence weighed. Now the version's shape and the version's content are two different
// reads: a manifest, whose size depends only on the fence count, and geometry documents fetched
// by content address in batches with a known per-document ceiling. The consequences are worth
// stating because they are the reason for the change rather than a side effect of it — only the
// geometry a version does not share with the previous one is transferred at all, a refused batch
// is SPLIT rather than restarting the whole walk, and there is no longer any fence set whose
// size prevents it from being read.
type fenceSetClient struct {
	client  *svcclient.Client
	url     string
	cache   *geofence.GeometryCache
	metrics *fenceGeometryMetrics

	// transport, when non-nil, carries one query INSTEAD of the service-token HTTP call. It is
	// the substitutable half of the fenceSetExec seam documented below.
	//
	// 🔴 IT IS A FIELD RATHER THAN A METHOD SO THE SEAM IS ACTUALLY A SEAM. Everything worth
	// testing about this client sits BETWEEN the query strings and the decoded result — the
	// manifest read, the chunking arithmetic, the split-on-refusal, the content verification,
	// the per-fence error fence — and none of it is reachable if the only way to reach
	// device-management is a hard-coded method call. With this, a test drives all of it against
	// device-management's REAL parsed schema and REAL resolvers in-process, faking the HTTP hop
	// and nothing else, so a field-name drift between the two services fails a test instead of
	// silently decoding to an empty fence set in production.
	//
	// Nil in every production path: NewFenceSetClient never sets it, so main's client resolves
	// through c.client exactly as before.
	transport fenceSetExec
}

// NewFenceSetClient builds the fence-set fetch seam over a service-token client and
// device-management's GraphQL URL (wired in main). It returns the two runtime interfaces it
// satisfies so the concrete adapter stays package-private: the version-addressed source the
// preview/replay path resolves through, and the current-set source the startup reconcile and
// the periodic sweep seed from.
//
// The cache is owned here rather than by a caller because every road into this service's
// containment projection passes through these two methods, and a cache that only some of them
// consulted would be a cache that mostly missed.
func NewFenceSetClient(ms *core.Microservice, client *svcclient.Client, url string,
	cache *geofence.GeometryCache) (runtime.FenceSetSource, runtime.CurrentFenceSetSource, runtime.FenceManifestResolver) {
	if cache == nil {
		cache = geofence.NewGeometryCache(geofence.DefaultMaxCachedVertices)
	}
	c := &fenceSetClient{client: client, url: url, cache: cache}
	if ms != nil {
		c.metrics = newFenceGeometryMetrics(ms)
	}
	return c, c, c
}

// fenceSetExec runs one query for a tenant and decodes its "data" object into out.
//
// It is the ONE thing this client needs from a transport, which is what lets the manifest read,
// the batched geometry read, the chunk splitting and the content verification be the same code
// in the production HTTP client and in a test that drives device-management's real schema
// in-process — rather than a stub that skips the step under test.
//
// An implementation MUST report a response it could not carry as svcclient.ErrResponseTooLarge
// (errors.Is-comparable). That is the signal a geometry batch splits on; any other error is
// terminal for the read.
type fenceSetExec func(ctx context.Context, tenant, query string, vars map[string]any, out any) error

// exec runs one query through whatever transport this client was built over — the substituted
// one when there is one, and otherwise the production path: a service-token GraphQL call over
// HTTP.
func (c *fenceSetClient) exec(ctx context.Context, tenant, query string, vars map[string]any, out any) error {
	if c.transport != nil {
		return c.transport(ctx, tenant, query, vars, out)
	}
	return c.client.Query(ctx, c.url, tenant, query, vars, out)
}

// FenceSetAt resolves the frozen fence set of one version — the fences that were live when an
// event stamped with that version was resolved.
//
// Version 0 is the KNOWN-EMPTY set (the tenant had never created a fence), answered without a
// round trip: it is knowledge, not absence, and asking the archive about it would be asking
// after a version that was never minted.
func (c *fenceSetClient) FenceSetAt(ctx context.Context, tenant string, version int32) (*geofence.FenceSet, error) {
	if version <= 0 {
		return geofence.EmptyFenceSet(0), nil
	}
	manifest, err := c.fetchManifest(ctx, tenant, version)
	if err != nil {
		return nil, err
	}
	return c.assemble(ctx, tenant, manifest)
}

// CurrentFenceSet resolves the tenant's CURRENT frozen fence set — the version a location event
// resolved right now would be stamped with, and the fences that version froze.
//
// The manifest read answers both halves from one row, so a fence edit landing mid-read cannot
// yield fences filed under a version that is not the one reported. That guarantee used to take
// care — the first page came from one door and every later page from another, addressed by the
// version the first had reported — and it is now a property of there being one read.
func (c *fenceSetClient) CurrentFenceSet(ctx context.Context, tenant string) (*geofence.FenceSet, error) {
	manifest, err := c.fetchManifest(ctx, tenant, 0)
	if err != nil {
		return nil, err
	}
	return c.assemble(ctx, tenant, manifest)
}

// ResolveManifest assembles a fence set from a manifest the caller already holds — the
// fence-set fact's road into the projection.
//
// It is the same assembly the archive reads use, deliberately: the fact road and the archive
// road must agree about what an unresolvable fence becomes, and two implementations of that
// rule would be two chances to make an unresolved fence silently absent on one of them.
func (c *fenceSetClient) ResolveManifest(ctx context.Context, tenant string,
	manifest *dmmodel.GeoFenceSetManifest) (*geofence.FenceSet, error) {
	return c.assemble(ctx, tenant, manifest)
}
