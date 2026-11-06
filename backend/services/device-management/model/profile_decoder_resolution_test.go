// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The resolution half of payload decoders: the decoder a published version declares is compiled
// into the cached ProfileResolution, in a key namespace an older build never reads, evicted from
// both namespaces, filled once per replica however many events ask at once, and a decoder this
// build cannot run is a typed failure that touches nothing but integration uplinks.

const resolutionDecoder = `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[
  {"name":"t","outputs":[{"key":"temp","expr":"double(i16be(frame, 0)) / 10.0"}]}],
  "vectors":[{"name":"v","input":{"frameHex":"00D7"},"expect":{"temp":21.5}}]}`

// decodeFrame runs a resolution's decoder over a two-byte frame and returns the temp reading.
func decodeTemp(t *testing.T, res ProfileResolution) string {
	t.Helper()
	plan := res.Decoder().Plan()
	require.NotNil(t, plan, "the resolution has no usable decoder")
	out, derr := plan.Decode(payloaddecode.Input{Frame: []byte{0x00, 0xD7}})
	require.Nil(t, derr)
	require.Len(t, out.Readings, 1)
	return out.Readings[0].Value
}

// decoderRig is the cache rig with a decoder on the draft, published as version 2.
func decoderRig(t *testing.T) *profileResolutionRig {
	t.Helper()
	r := newProfileResolutionRig(t)
	_, err := r.api.SetDeviceProfileDecoder(r.ctx, "p", []byte(resolutionDecoder))
	require.NoError(t, err)
	_, err = r.capi.PublishDeviceProfile(r.ctx, "p", nil, nil, "t")
	require.NoError(t, err)
	return r
}

// replica is a second CachedApi over the same database and the same shared bucket, with a process
// memory of its own: another pod of the same build.
func (r *profileResolutionRig) replica() *CachedApi {
	return NewCachedApi(r.api, &Caches{ProfileResolutionByType: r.kv.NewCache()})
}

func TestAResolutionCompilesItsVersionsDecoder(t *testing.T) {
	r := decoderRig(t)
	res := r.read(t)
	assert.True(t, res.HasDecoder())
	dec := res.Decoder()
	assert.True(t, dec.Declared())
	assert.Nil(t, dec.Unusable())
	assert.Equal(t, "FRAME", dec.Input())
	assert.Equal(t, 1, dec.MaxReadings())
	assert.NotZero(t, dec.EstimatedCost())
	assert.Equal(t, "21.5", decodeTemp(t, res))
	assert.Equal(t, "p@2", res.Scope().ProfileVersionToken)

	// A version without one declares none, and says so rather than answering a zero plan.
	none := newProfileResolutionRig(t).read(t)
	assert.False(t, none.HasDecoder())
	assert.False(t, none.Decoder().Declared())
	assert.Nil(t, none.Decoder().Plan())
	assert.Nil(t, none.Decoder().Unusable())
}

// The cache stores the document and every replica compiles its own program: a decoded entry
// decodes the same as a built one, and an entry without a decoder encodes as it always did.
func TestTheCacheWireCarriesTheDecoderDocumentNotAProgram(t *testing.T) {
	r := decoderRig(t)
	built := r.read(t)
	raw, err := json.Marshal(built)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	assert.Contains(t, fields, "Decoder")
	assert.JSONEq(t, resolutionDecoder, string(fields["Decoder"]))

	before := decodePlanBuilds.Load()
	decoded, err := decodeProfileResolution(raw)
	require.NoError(t, err)
	assert.Equal(t, before+1, decodePlanBuilds.Load(), "a decoded entry compiles its own decoder")
	assert.Equal(t, "21.5", decodeTemp(t, *decoded))
	assert.NotSame(t, built.Decoder().Plan(), decoded.Decoder().Plan(), "a program is never carried between entries")

	plain, err := json.Marshal(newProfileResolutionRig(t).read(t))
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "Decoder", "an entry without a decoder is encoded exactly as before")
}

// ROLLING UPGRADE, new entry read by an old build: its decode is not strict, so the key it does
// not know is ignored and the metrics and scope still read. (It never sees the entry: see the key
// test below. This pins the second line of defence.)
func TestAnOlderBuildReadsAPastTheDecoderKey(t *testing.T) {
	r := decoderRig(t)
	raw, err := json.Marshal(r.read(t))
	require.NoError(t, err)
	var old struct { // profileResolutionWire as the build before the decoder declared it
		Scope   ProfileScope
		Metrics []resolvedMetricWire
	}
	require.NoError(t, json.Unmarshal(raw, &old))
	assert.Equal(t, "p@2", old.Scope.ProfileVersionToken)
	require.Len(t, old.Metrics, 1)
	assert.Equal(t, "temp", old.Metrics[0].MetricKey)
}

// oldWriterEntry is what a build before the decoder writes for a version that HAS one: the scope
// and the metrics, no decoder.
func oldWriterEntry(t *testing.T, r *profileResolutionRig) json.RawMessage {
	t.Helper()
	res := r.read(t)
	require.True(t, res.HasDecoder())
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	delete(fields, "Decoder")
	old, err := json.Marshal(fields)
	require.NoError(t, err)
	return old
}

// A21. ROLLING UPGRADE, old entry read by a new build: an old pod filled the bucket under the key
// it has always used with a resolution that lacks the decoder the snapshot has. A new pod must
// not take it for the version's truth, or every integration uplink of the type would be
// flattened instead of decoded for as long as it lived.
func TestANewBuildNeverReadsAnOldBuildsEntry(t *testing.T) {
	r := decoderRig(t)
	stale := oldWriterEntry(t, r)
	require.NoError(t, r.capi.Caches().ProfileResolutionByType.Delete(r.ctx, profileResolutionByTypeKey(partialUpdateTenant, r.typeId)))
	require.NoError(t, r.capi.Caches().ProfileResolutionByType.Set(r.ctx,
		legacyProfileResolutionByTypeKey(partialUpdateTenant, r.typeId), stale))

	// The old entry is really there, and really decoder-free: the control the assertion needs.
	got, answered, _ := readSharedCache(r.ctx, r.capi.Caches().ProfileResolutionByType,
		legacyProfileResolutionByTypeKey(partialUpdateTenant, r.typeId), decodeProfileResolution, false)
	require.True(t, answered)
	require.False(t, got.HasDecoder())

	// A fresh pod (empty process memory) over the same bucket.
	res, err := r.replica().ProfileResolutionByDeviceType(r.ctx, r.typeId)
	require.NoError(t, err)
	assert.True(t, res.HasDecoder(), "the new build took the old build's decoder-free entry for the version")
	assert.Equal(t, "21.5", decodeTemp(t, res))
}

// And the other direction: the new build writes only under its own namespace, which an old
// build never reads, and never under the key the old build serves.
func TestANewBuildWritesOnlyUnderItsOwnKey(t *testing.T) {
	r := decoderRig(t)
	r.read(t)
	c := r.capi.Caches().ProfileResolutionByType
	_, v2, _ := readSharedCache(r.ctx, c, profileResolutionByTypeKey(partialUpdateTenant, r.typeId), decodeProfileResolution, false)
	_, legacy, _ := readSharedCache(r.ctx, c, legacyProfileResolutionByTypeKey(partialUpdateTenant, r.typeId), decodeProfileResolution, false)
	assert.True(t, v2, "the new build filled its own key")
	assert.False(t, legacy, "the new build wrote the key an old build serves from")
}

// The tenant leads every key: a tenant's erasure scans keys by their leading tenant, and a
// cross-replica eviction is filed under it. A key that led with the namespace would be filed
// under a tenant called "v2" and survive both.
func TestTheNamespacedKeyStillLeadsWithTheTenant(t *testing.T) {
	for _, tenant := range []string{"acme", "acme-2", "tenant-0001"} {
		key := profileResolutionByTypeKey(tenant, 7)
		assert.True(t, strings.HasPrefix(key, tenant+"|"), key)
		assert.Equal(t, tenant, strings.SplitN(key, "|", 2)[0])
	}
	assert.NotEqual(t, profileResolutionByTypeKey("acme", 7), legacyProfileResolutionByTypeKey("acme", 7))
	assert.NotEqual(t, profileResolutionByTypeKey("acme", 7), profileResolutionByTypeKey("acme-2", 7))
}

// Every trigger drops the entry in BOTH namespaces. An old replica serves the entry under the old
// key until told otherwise, and a publish on a new one must reach it; the new key is the one this
// build serves.
func TestEveryTriggerDropsBothKeys(t *testing.T) {
	triggers := map[string]func(t *testing.T, r *profileResolutionRig){
		"publish": func(t *testing.T, r *profileResolutionRig) { r.editUnitAndPublish(t, "K") },
		"rollback": func(t *testing.T, r *profileResolutionRig) {
			_, err := r.capi.RollbackDeviceProfile(r.ctx, "p", 1)
			require.NoError(t, err)
		},
		"device type re-profiled": func(t *testing.T, r *profileResolutionRig) {
			_, err := r.capi.UpdateDeviceType(r.ctx, "dt", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.ClearedString()})
			require.NoError(t, err)
		},
		"fence-set version minted": func(t *testing.T, r *profileResolutionRig) {
			_, err := r.api.CreateGeoFence(r.ctx, &GeoFenceCreateRequest{Token: "yard", Geometry: yardGeometry})
			require.NoError(t, err)
		},
	}
	for name, trigger := range triggers {
		t.Run(name, func(t *testing.T) {
			r := decoderRig(t)
			c := r.capi.Caches().ProfileResolutionByType
			r.read(t) // the new key
			require.NoError(t, c.Set(r.ctx, legacyProfileResolutionByTypeKey(partialUpdateTenant, r.typeId), oldWriterEntry(t, r)))
			require.Equal(t, 2, r.kv.Len(), "the control: both namespaces hold an entry")

			trigger(t, r)
			_, v2, _ := readSharedCache(r.ctx, c, profileResolutionByTypeKey(partialUpdateTenant, r.typeId), decodeProfileResolution, false)
			_, legacy, _ := readSharedCache(r.ctx, c, legacyProfileResolutionByTypeKey(partialUpdateTenant, r.typeId), decodeProfileResolution, false)
			assert.False(t, v2, "the new key survived the trigger")
			assert.False(t, legacy, "the old key survived the trigger: an old replica would keep serving it")
			assert.Equal(t, 0, r.kv.Len())
		})
	}
}

// withSnapshotDecoder rewrites the stored snapshot of version v of profile p to hold decoder, as a
// release that knows a newer library, or an old pod's publish, would have left it.
func (r *profileResolutionRig) withSnapshotDecoder(t *testing.T, version int32, decoder string) {
	t.Helper()
	var row DeviceProfileVersion
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("version = ?", version).First(&row).Error)
	var snap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(row.Snapshot, &snap))
	snap["decoder"] = json.RawMessage(decoder)
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Where("id = ?", row.ID).
		Update("snapshot", raw).Error)
	r.capi.dropProfileResolution(r.ctx, partialUpdateTenant, r.typeId)
}

// A24. A snapshot written by a newer library, read by this build, fails only where a decode is
// attempted. The resolution itself is whole: its scope, its metrics and its validation are what a
// Location event or a plain measurement of the same device type reads, and a resolution that
// failed to build would turn all of them into a retried ApiCallFailed.
func TestADecoderThisBuildCannotRunDoesNotFailTheResolution(t *testing.T) {
	cases := map[string]struct {
		decoder string
		reason  payloaddecode.Reason
	}{
		"a newer environment version": {
			decoder: `{"kind":"CEL","envVersion":2,"input":"FRAME","futureKey":true,"sections":[{"name":"t","outputs":[{"key":"temp","expr":"lpp.decode2(frame)"}]}]}`,
			reason:  payloaddecode.ReasonEnvUnsupported,
		},
		"a document that does not compile": {
			decoder: `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"t","outputs":[{"key":"temp","expr":"nope("}]}]}`,
			reason:  payloaddecode.ReasonCompileFailed,
		},
		"an undeclared output": {
			decoder: `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"t","outputs":[{"key":"undeclared","expr":"1.0"}]}]}`,
			reason:  payloaddecode.ReasonCompileFailed,
		},
		"a kind this build refuses": {
			decoder: `{"kind":"WASM","envVersion":1,"input":"FRAME","sections":[]}`,
			reason:  payloaddecode.ReasonCompileFailed,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newProfileResolutionRig(t)
			r.withSnapshotDecoder(t, 1, tc.decoder)

			res, err := r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId)
			require.NoError(t, err, "a decoder this build cannot run failed the resolution of the whole device type")
			assert.Equal(t, "p@1", res.Scope().ProfileVersionToken)
			assert.Equal(t, 1, res.MetricCount())
			assert.NoError(t, res.ValidateMeasurement("temp", "21.5"))
			assert.Error(t, res.ValidateMeasurement("temp", "not-a-number"), "the metrics still validate")

			assert.True(t, res.HasDecoder())
			dec := res.Decoder()
			assert.True(t, dec.Declared())
			assert.Nil(t, dec.Plan())
			bad := dec.Unusable()
			require.NotNil(t, bad)
			assert.Equal(t, tc.reason, bad.Reason)
			if tc.reason == payloaddecode.ReasonEnvUnsupported {
				assert.True(t, errors.Is(bad, payloaddecode.ErrEnvUnsupported))
			}
			if strings.Contains(tc.decoder, `"input":"FRAME"`) {
				assert.Equal(t, "FRAME", dec.Input(), "an unusable decoder still says what it would read")
			}

			// It is the entry that other events of the type read: cached, not rebuilt per event.
			again, err := r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId)
			require.NoError(t, err)
			assert.Equal(t, bad.Reason, again.Decoder().Unusable().Reason)
		})
	}
}

// A15. The same decoder text in two tenants compiles against each tenant's OWN metrics: a program
// compiled for one is not valid for the other, and the one that is shared is the one that leaks.
func TestTwoTenantsWithTheSameDecoderTextNeverShareAProgram(t *testing.T) {
	mk := func(dataType string) ProfileResolution {
		defs := []*MetricDefinition{{MetricKey: "temp", DataType: dataType}}
		defs[0].ID = 1
		return NewProfileResolutionWithDecoder(ProfileScope{ProfileVersionToken: "p@1"}, defs, json.RawMessage(resolutionDecoder))
	}
	acme := mk("DOUBLE")
	globex := mk("INT")
	assert.Equal(t, "21.5", decodeTemp(t, acme))

	// Globex's metric is an INT: 21.5 is not storable in it. A program shared from acme's
	// resolution would store it.
	require.NotNil(t, globex.Decoder().Plan())
	_, derr := globex.Decoder().Plan().Decode(payloaddecode.Input{Frame: []byte{0x00, 0xD7}})
	require.NotNil(t, derr, "globex's decoder ran with acme's metrics")
	assert.Equal(t, payloaddecode.ReasonInvalidOutput, derr.Reason)
	assert.NotSame(t, acme.Decoder().Plan(), globex.Decoder().Plan())

	// And a tenant that never declared the metric cannot borrow a compiled program for it.
	nobody := NewProfileResolutionWithDecoder(ProfileScope{ProfileVersionToken: "p@1"}, nil, json.RawMessage(resolutionDecoder))
	require.NotNil(t, nobody.Decoder().Unusable(), "a tenant with no declared metric was handed another tenant's program")
	assert.Equal(t, payloaddecode.ReasonCompileFailed, nobody.Decoder().Unusable().Reason)
	assert.Nil(t, nobody.Decoder().Plan())
}

// hold makes every compile wait until `want` compiles have started or `patience` has passed, so
// a test can see how many fills a burst of events started.
func hold(want int64, patience time.Duration) (release func()) {
	started := make(chan struct{})
	var once sync.Once
	var count int64
	var mu sync.Mutex
	decodePlanBuildHook = func() {
		mu.Lock()
		count++
		if count >= want {
			once.Do(func() { close(started) })
		}
		mu.Unlock()
		select {
		case <-started:
		case <-time.After(patience):
		}
	}
	return func() { decodePlanBuildHook = nil }
}

func burst(t *testing.T, n int, read func() (ProfileResolution, error)) []ProfileResolution {
	t.Helper()
	out := make([]ProfileResolution, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i], errs[i] = read()
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	return out
}

// The stampede after a publish: every event in flight misses at once, and each would compile the
// decoder (tens of milliseconds for a large one). The fill is one per key per replica.
func TestAColdBurstCompilesTheDecoderOnce(t *testing.T) {
	const n = 24
	r := decoderRig(t)
	r.capi.dropProfileResolution(r.ctx, partialUpdateTenant, r.typeId)
	release := hold(n, 400*time.Millisecond)
	defer release()
	before := decodePlanBuilds.Load()

	got := burst(t, n, func() (ProfileResolution, error) { return r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId) })
	assert.Equal(t, int64(1), decodePlanBuilds.Load()-before, "a burst of %d events compiled the decoder more than once", n)
	for _, res := range got {
		assert.Equal(t, "21.5", decodeTemp(t, res))
	}
}

// The same, when the entry is in the shared bucket (another replica filled it) and this replica's
// memory is empty: decoding the entry is the compile.
func TestAColdBurstOverABucketHitDecodesTheEntryOnce(t *testing.T) {
	const n = 24
	r := decoderRig(t)
	r.read(t) // this replica fills the bucket
	other := r.replica()
	before, gets := decodePlanBuilds.Load(), r.kv.Gets
	release := hold(n, 400*time.Millisecond)
	defer release()

	got := burst(t, n, func() (ProfileResolution, error) { return other.ProfileResolutionByDeviceType(r.ctx, r.typeId) })
	assert.Equal(t, int64(1), decodePlanBuilds.Load()-before, "a burst of %d events over a bucket hit decoded the entry more than once", n)
	assert.Equal(t, 1, r.kv.Gets-gets, "a burst of %d events over a bucket hit read the bucket once each", n)
	for _, res := range got {
		assert.Equal(t, "21.5", decodeTemp(t, res))
	}
}

// A fill that began before a publish is the superseded version. An event that arrives after the
// publish must not be handed it by joining the fill in flight.
func TestAFillBeforeAPublishIsNotHandedToAnEventAfterIt(t *testing.T) {
	r := decoderRig(t)
	r.capi.dropProfileResolution(r.ctx, partialUpdateTenant, r.typeId)

	entered := make(chan struct{})
	proceed := make(chan struct{})
	var claimed atomic.Bool
	decodePlanBuildHook = func() {
		if claimed.CompareAndSwap(false, true) { // only the first compile is held
			close(entered)
			<-proceed
		}
	}
	defer func() { decodePlanBuildHook = nil }()

	var stale ProfileResolution
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		stale, err = r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId)
		assert.NoError(t, err)
	}()
	<-entered // the fill has read version 2 and is compiling

	r.editUnitAndPublish(t, "K") // version 3, and the eviction
	// Asked in its own goroutine: an event that joined the held fill would wait for it, and that
	// must fail the test, not hang it.
	type answer struct {
		res ProfileResolution
		err error
	}
	asked := make(chan answer, 1)
	go func() {
		res, err := r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId)
		asked <- answer{res, err}
	}()
	var fresh ProfileResolution
	select {
	case a := <-asked:
		require.NoError(t, a.err)
		fresh = a.res
	case <-time.After(5 * time.Second):
		close(proceed)
		wg.Wait()
		t.Fatal("an event after the publish waited on the fill that began before it")
	}
	assert.Equal(t, "p@3", fresh.Scope().ProfileVersionToken, "an event after the publish was handed the fill that began before it")

	close(proceed)
	wg.Wait()
	assert.Equal(t, "p@2", stale.Scope().ProfileVersionToken, "the control: the fill in flight did read version 2")

	// The superseded fill must not have been stored over the new one.
	again, err := r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId)
	require.NoError(t, err)
	assert.Equal(t, "p@3", again.Scope().ProfileVersionToken)
}

// A caller cancelled while it led a fill does not fail the callers that joined it.
func TestACancelledLeaderDoesNotFailTheCallersThatJoinedIt(t *testing.T) {
	r := decoderRig(t)
	r.capi.dropProfileResolution(r.ctx, partialUpdateTenant, r.typeId)
	cancelled, cancel := context.WithCancel(r.ctx)
	cancel()
	_, err := r.capi.ProfileResolutionByDeviceType(cancelled, r.typeId)
	_ = err // whatever the leader's own outcome, the next caller succeeds
	res, err := r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId)
	require.NoError(t, err)
	assert.True(t, res.HasDecoder())
}

// The tenant is part of every key and of the context a fill runs in: a read in one tenant's
// context cannot be answered by another tenant's entry for the same device type id.
func TestResolutionKeysNeverCrossTenants(t *testing.T) {
	r := decoderRig(t)
	r.read(t)
	other := core.WithTenant(context.Background(), "globex")
	c := r.capi.Caches().ProfileResolutionByType
	_, answered, _ := readSharedCache(other, c, profileResolutionByTypeKey("globex", r.typeId), decodeProfileResolution, false)
	assert.False(t, answered, fmt.Sprintf("globex found acme's entry for type %d", r.typeId))
}

// A maximal decoder stays in the replica's memory once decoded: it is charged what it holds, and
// the cache's byte bound is large enough to hold it. An entry that could not be attached would be
// decoded, which means compiled, again on every event of the type.
func TestAMaximalDecoderIsKeptDecodedByTheProductionCache(t *testing.T) {
	doc, defs := maximalDecoder(t)
	built := NewProfileResolutionWithDecoder(ProfileScope{ProfileVersionToken: "p@1"}, defs, doc)
	require.NotNil(t, built.Decoder().Plan())
	c := newMemoryCache(profileResolutionCacheOptions(32, 25)...)
	require.NoError(t, c.Set(context.Background(), "acme|v2|1", built))

	before := decodePlanBuilds.Load()
	for i := 0; i < 5; i++ {
		_, found, err := messaging.GetShared(context.Background(), c, "acme|v2|1", decodeProfileResolution)
		require.NoError(t, err)
		require.True(t, found)
	}
	assert.Equal(t, int64(1), decodePlanBuilds.Load()-before, "five reads compiled the decoder more than once: the decoded entry was not kept")
}

func newMemoryCache(opts ...messaging.CacheOption) *messaging.Cache {
	return msgtest.NewMemoryKV().NewCache(opts...)
}

// A replica's memory for decoders is bounded: each decoded entry is charged what it holds, so a
// cache that has seen more maximal decoders than its bound allows has let go of the oldest, and
// reading one again compiles it again. Charged by its encoding alone, all of them would be kept.
func TestAReplicaHoldsAtMostItsBoundOfDecoders(t *testing.T) {
	doc, defs := maximalDecoder(t)
	built := NewProfileResolutionWithDecoder(ProfileScope{ProfileVersionToken: "p@1"}, defs, doc)
	c := newMemoryCache(profileResolutionCacheOptions(32, 25)...)
	ctx := context.Background()
	const n = 12 // twice what the bound holds at about 6 MB each
	for i := 0; i < n; i++ {
		require.NoError(t, c.Set(ctx, fmt.Sprintf("acme|v2|%d", i), built))
		_, _, err := messaging.GetShared(ctx, c, fmt.Sprintf("acme|v2|%d", i), decodeProfileResolution)
		require.NoError(t, err)
	}
	before := decodePlanBuilds.Load()
	for i := 0; i < n; i++ {
		_, _, err := messaging.GetShared(ctx, c, fmt.Sprintf("acme|v2|%d", i), decodeProfileResolution)
		require.NoError(t, err)
	}
	assert.Greater(t, decodePlanBuilds.Load()-before, int64(0),
		"%d maximal decoders are all held decoded: the cache charged them by their encoding, not by the programs they hold", n)
}

// The wave AFTER the fill: a database fill stores the encoded entry, not the decoded value, so the
// first reads from memory each find bytes and decode them, which compiles. They share one decode.
func TestAWaveOfMemoryHitsAfterAFillDecodesTheEntryOnce(t *testing.T) {
	const n = 24
	r := decoderRig(t)
	r.capi.dropProfileResolution(r.ctx, partialUpdateTenant, r.typeId)
	r.read(t) // the database fill: one compile, and the entry is in memory as bytes
	before := decodePlanBuilds.Load()
	release := hold(n, 400*time.Millisecond)
	defer release()

	got := burst(t, n, func() (ProfileResolution, error) { return r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId) })
	assert.Equal(t, int64(1), decodePlanBuilds.Load()-before, "a wave of %d memory hits after a fill decoded the entry more than once", n)
	for _, res := range got {
		assert.Equal(t, "21.5", decodeTemp(t, res))
	}
}

// A caller that saw a miss and reached the load after the fill that answered it had finished must
// find that fill, not read the database again. Shown by moving the database underneath: a late
// caller that loaded again would see the new value, one that found the fill sees the old.
func TestALateMissAfterTheFillDoesNotLoadAgain(t *testing.T) {
	r := decoderRig(t)
	r.capi.dropProfileResolution(r.ctx, partialUpdateTenant, r.typeId)
	key := profileResolutionByTypeKey(partialUpdateTenant, r.typeId)
	filled := r.read(t)
	require.Equal(t, "Cel", unitOf(t, filled))

	var row DeviceProfileVersion
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("version = ?", 2).First(&row).Error)
	changed := strings.Replace(string(row.Snapshot), `"Cel"`, `"ZZ"`, -1)
	require.NotEqual(t, string(row.Snapshot), changed, "the control: the snapshot holds the unit")
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Where("id = ?", row.ID).
		Update("snapshot", []byte(changed)).Error)

	late, err := r.capi.loadProfileResolution(r.ctx, key, r.typeId)
	require.NoError(t, err)
	assert.Equal(t, "Cel", unitOf(t, late), "a late miss read the database again instead of finding the fill")
}

// A leader whose deadline passes while it holds the fill does not fail the callers that joined it:
// the database read runs under the leader's context, so they all receive its error, and each
// asks again under its own.
func TestAnExpiredLeaderDoesNotFailItsJoiners(t *testing.T) {
	r := decoderRig(t)
	r.capi.dropProfileResolution(r.ctx, partialUpdateTenant, r.typeId)
	leaderCtx, cancel := context.WithTimeout(r.ctx, 150*time.Millisecond)
	defer cancel()

	entered := make(chan struct{})
	var claimed atomic.Bool
	profileResolutionLoadHook = func() {
		if claimed.CompareAndSwap(false, true) { // only the leader is held, until its deadline has passed
			close(entered)
			<-leaderCtx.Done()
		}
	}
	defer func() { profileResolutionLoadHook = nil }()

	var wg sync.WaitGroup
	var leaderErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, leaderErr = r.capi.ProfileResolutionByDeviceType(leaderCtx, r.typeId)
	}()
	<-entered
	joined := make(chan error, 1)
	go func() {
		res, err := r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId) // joins the held fill
		if err == nil && !res.HasDecoder() {
			err = errors.New("joiner answered without the decoder")
		}
		joined <- err
	}()
	select {
	case err := <-joined:
		assert.NoError(t, err, "a joiner failed with its leader's deadline")
	case <-time.After(10 * time.Second):
		t.Fatal("the joiner never answered")
	}
	wg.Wait()
	assert.Error(t, leaderErr, "the control: the leader, whose deadline passed, did fail")
}

// F1. A decode in flight for the bytes of version 2 must not be handed to a reader of version 3's
// bytes, even though a fill does not move the cache generation and both read the same one: the
// decoded value is attached to the entry the reader holds, so the replica would serve the
// superseded decoder and metrics until the entry expired.
func TestADecodeInFlightIsNotSharedAcrossDifferentBytes(t *testing.T) {
	r := decoderRig(t) // version 2, with a decoder
	key := profileResolutionByTypeKey(partialUpdateTenant, r.typeId)
	old := r.read(t)
	raw2, err := json.Marshal(old)
	require.NoError(t, err)
	r.editUnitAndPublish(t, "K") // version 3
	raw3, err := json.Marshal(r.read(t))
	require.NoError(t, err)
	require.NotEqual(t, raw2, raw3)
	decode := r.capi.decodeOnce(key)

	entered := make(chan struct{})
	proceed := make(chan struct{})
	var claimed atomic.Bool
	decodePlanBuildHook = func() {
		if claimed.CompareAndSwap(false, true) { // only the decode of version 2 is held
			close(entered)
			<-proceed
		}
	}
	defer func() { decodePlanBuildHook = nil }()

	var stale *ProfileResolution
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		stale, err = decode(raw2)
		assert.NoError(t, err)
	}()
	<-entered

	type answer struct {
		res *ProfileResolution
		err error
	}
	asked := make(chan answer, 1)
	go func() { res, err := decode(raw3); asked <- answer{res, err} }()
	select {
	case a := <-asked:
		require.NoError(t, a.err)
		assert.Equal(t, "p@3", a.res.Scope().ProfileVersionToken, "the decode of version 3's bytes was handed version 2's result")
	case <-time.After(5 * time.Second):
		close(proceed)
		wg.Wait()
		t.Fatal("the decode of version 3's bytes waited on the decode of version 2's")
	}
	close(proceed)
	wg.Wait()
	assert.Equal(t, "p@2", stale.Scope().ProfileVersionToken)
}

// F2. One tenant with more maximal decoders than its share evicts its own, never another
// tenant's: the quiet tenant's decoded entry is still held, with no recompile, after the noisy
// tenant has stored far more than the whole cache could keep.
func TestANoisyTenantEvictsItsOwnDecodersNotAnothersOwn(t *testing.T) {
	doc, defs := maximalDecoder(t)
	built := NewProfileResolutionWithDecoder(ProfileScope{ProfileVersionToken: "p@1"}, defs, doc)
	c := newMemoryCache(profileResolutionCacheOptions(32, 25)...)
	ctx := context.Background()
	read := func(key string) {
		_, found, err := messaging.GetShared(ctx, c, key, decodeProfileResolution)
		require.NoError(t, err)
		require.True(t, found)
	}
	require.NoError(t, c.Set(ctx, "quiet|v2|1", built))
	read("quiet|v2|1") // the oldest entry in the cache
	for i := 0; i < 8; i++ {
		k := fmt.Sprintf("noisy|v2|%d", i)
		require.NoError(t, c.Set(ctx, k, built))
		read(k)
	}
	before := decodePlanBuilds.Load()
	read("quiet|v2|1")
	assert.Equal(t, int64(0), decodePlanBuilds.Load()-before, "another tenant's decoders evicted the quiet tenant's")

	// The control: the noisy tenant could not keep them all, so reading its oldest compiles again.
	read("noisy|v2|0")
	assert.Equal(t, int64(1), decodePlanBuilds.Load()-before, "the noisy tenant kept more than its share")
}

func TestCompileCounterHasNoTenantLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "x"}, []string{"result"})
	reg.MustRegister(vec)
	decoderCompileMetric.Store(vec)
	defer decoderCompileMetric.Store(nil)
	NewProfileResolutionWithDecoder(ProfileScope{}, []*MetricDefinition{{MetricKey: "temp", DataType: "DOUBLE"}}, json.RawMessage(resolutionDecoder))
	NewProfileResolutionWithDecoder(ProfileScope{}, nil, json.RawMessage(`{"kind":"CEL","envVersion":2,"input":"FRAME"}`))
	assert.Equal(t, 1.0, testutil.ToFloat64(vec.WithLabelValues("ok")))
	assert.Equal(t, 1.0, testutil.ToFloat64(vec.WithLabelValues("env_unsupported")))
}

// A published version whose stored decoder is over the document cap (a row jsonb re-rendered
// larger than the save gate allowed) is an invalid plan with a compile reason, never an
// "unsupported environment" and never a scan of the oversized document.
func TestAnOverCapStoredDecoderIsACompileFailureNotAnEnvMismatch(t *testing.T) {
	raw := json.RawMessage(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","ignore":true}],"table":{"x":"` +
		strings.Repeat("a", payloaddecode.MaxDocumentBytes) + `"}}`)
	p := buildDecodePlan(ProfileScope{}, nil, raw)
	require.NotNil(t, p)
	require.NotNil(t, p.err)
	assert.Equal(t, payloaddecode.ReasonCompileFailed, p.err.Reason)
	assert.Nil(t, p.plan)
}

// Two maximal decoders of one tenant, read in turn, compile twice in all when the tenant's share
// holds both, which is what sizing the share for the decoders a tenant keeps in use means. The
// control is the same reads against a share that holds only one: they compile on almost every
// read, so the count above is the share's doing and not an artifact of the fixture.
func TestTwoMaximalDecodersOfOneTenantAreCompiledOnceEachWhenTheShareHoldsBoth(t *testing.T) {
	doc, defs := maximalDecoder(t)
	built := NewProfileResolutionWithDecoder(ProfileScope{ProfileVersionToken: "p@1"}, defs, doc)
	ctx := context.Background()
	const rounds = 10
	run := func(miB, share int) int64 {
		c := newMemoryCache(profileResolutionCacheOptions(miB, share)...)
		for _, k := range []string{"acme|v2|1", "acme|v2|2"} {
			require.NoError(t, c.Set(ctx, k, built))
		}
		before := decodePlanBuilds.Load()
		for i := 0; i < rounds; i++ {
			for _, k := range []string{"acme|v2|1", "acme|v2|2"} {
				_, found, err := messaging.GetShared(ctx, c, k, decodeProfileResolution)
				require.NoError(t, err)
				require.True(t, found)
			}
		}
		return decodePlanBuilds.Load() - before
	}
	assert.Equal(t, int64(2), run(32, 50), "a share that holds both decoders compiled more than once each")
	assert.Greater(t, run(32, 25), int64(2), "the control: a share that holds one decoder should have thrashed")
}

// One tenant's decoders do not push another tenant's out of the replica's memory, even when the
// replica as a whole is full: the tenant holding the most gives up an entry, not the one that holds
// a decoder of its own and is within its share.
func TestATenantWithinItsShareKeepsItsDecoderWhileALargerTenantFillsTheReplica(t *testing.T) {
	doc, defs := maximalDecoder(t)
	built := NewProfileResolutionWithDecoder(ProfileScope{ProfileVersionToken: "p@1"}, defs, doc)
	ctx := context.Background()
	// 32 MiB with a 40% share (12.8 MiB): two maximal decoders per tenant, five in all.
	c := newMemoryCache(profileResolutionCacheOptions(32, 40)...)
	read := func(k string) {
		require.NoError(t, c.Set(ctx, k, built))
		_, found, err := messaging.GetShared(ctx, c, k, decodeProfileResolution)
		require.NoError(t, err)
		require.True(t, found)
	}
	read("quiet|v2|1") // the oldest entry of the replica
	for _, k := range []string{"busy1|v2|1", "busy1|v2|2", "busy2|v2|1", "busy2|v2|2", "busy3|v2|1"} {
		read(k)
	}
	before := decodePlanBuilds.Load()
	_, found, err := messaging.GetShared(ctx, c, "quiet|v2|1", decodeProfileResolution)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(0), decodePlanBuilds.Load()-before, "the quiet tenant's decoder was let go for other tenants' stores")
}
