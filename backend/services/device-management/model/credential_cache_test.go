// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"container/list"
	"context"
	"crypto/sha256"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ccClock is a frozen clock a test moves by hand, so the TTL cannot explain a pass.
type ccClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *ccClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *ccClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// ccFixture is credFixture's device "dev" (MQTT_BASIC cred-1 / s3cret as c-1, ACCESS_TOKEN
// tok-1 as c-2) and a second device "dev2" of the same type, behind a CachedApi holding a
// credential cache on a frozen clock, with the evictor wired as the service wires it.
type ccFixture struct {
	api   *Api
	capi  *CachedApi
	cache *CredentialCache
	ctx   context.Context
	stmts *stmtCounter
	clock *ccClock
	t0    time.Time
	devId uint
	dev2  uint
}

// credentialCacheTables is everything the writes under test touch: the credential and its
// device, and every table DeleteDevice's cascade and ReplaceDevice's journal write.
func credentialCacheTables() []any {
	return append(append([]any{}, deviceProfileTables...), &DeviceCredential{}, &DeviceReplacement{},
		&EntityRelationship{}, &EntityAttribute{}, &Alarm{}, &EntityGroupMembership{})
}

func newCredentialCacheFixture(t *testing.T) ccFixture {
	t.Helper()
	api := newPartialUpdateApi(t, credentialCacheTables()...)
	stmts := countQueries(t, api)
	f := seedCredentialFixture(t, api, stmts)
	dev2, err := api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "dev2", DeviceTypeToken: "dt"})
	require.NoError(t, err)
	t0 := time.Unix(1_800_000_000, 0)
	clock := &ccClock{t: t0}
	cache := NewCredentialCache(4096, 4<<20, withCredentialCacheClock(clock.now))
	// Every cache is there, as in the service: a device delete evicts the others too.
	capi := NewCachedApi(api, &Caches{
		DeviceByToken:           msgtest.NewMemoryKV().NewCache(),
		RelationshipsBySource:   msgtest.NewMemoryKV().NewCache(),
		ProfileResolutionByType: msgtest.NewMemoryKV().NewCache(),
		MembershipsByEntity:     msgtest.NewMemoryKV().NewCache(),
		ScopedGroupsExist:       msgtest.NewMemoryKV().NewCache(),
		Credentials:             cache,
	})
	api.CacheEvictor = capi
	stmts.reset()
	return ccFixture{api: api, capi: capi, cache: cache, ctx: f.ctx, stmts: stmts, clock: clock, t0: t0,
		devId: f.devId, dev2: dev2.ID}
}

// credentialReads is how many statements read device_credentials since the last reset.
func (f ccFixture) credentialReads() int {
	n := 0
	for _, s := range f.stmts.taken() {
		if strings.Contains(s, "device_credentials") {
			n++
		}
	}
	return n
}

// check authenticates p and returns the device, the error and the credential reads it cost.
func (f ccFixture) check(p *PresentedCredential) (*Device, error, int) {
	f.stmts.reset()
	d, err := f.capi.AuthenticateDevice(f.ctx, p, f.t0)
	return d, err, f.credentialReads()
}

func (f ccFixture) mustAuthenticate(t *testing.T, p *PresentedCredential, wantReads int, what string) *Device {
	t.Helper()
	d, err, reads := f.check(p)
	require.NoError(t, err, what)
	require.NotNil(t, d, what)
	require.Equal(t, wantReads, reads, "%s: credential reads", what)
	return d
}

// A second check within the TTL reads nothing, and a hit does not extend the entry: one
// filled at t0 is read again at exactly t0+5s even after a hit at t0+4s.
func TestARepeatedAuthenticationWithinTheTTLRunsNoStatement(t *testing.T) {
	f := newCredentialCacheFixture(t)
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "first check")
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "second check at t0")
	f.clock.set(f.t0.Add(4 * time.Second))
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "check at t0+4s")
	f.clock.set(f.t0.Add(CredentialCacheTTL))
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "check at exactly t0+5s")
}

// Nothing that failed is kept: an unknown id, a wrong secret and a misconfigured credential
// each cost a read every time, and a right secret after a wrong one is read once.
func TestAFailedAuthenticationIsNeverCached(t *testing.T) {
	f := newCredentialCacheFixture(t)
	for i := 1; i <= 2; i++ {
		_, err, reads := f.check(basic("nobody", "x"))
		require.ErrorIs(t, err, ErrCredentialNotResolved, "unknown id, check %d", i)
		require.Equal(t, 1, reads, "unknown id, check %d", i)
	}
	_, err, reads := f.check(basic("cred-1", "wrong"))
	require.ErrorIs(t, err, ErrCredentialSecretMismatch)
	require.Equal(t, 1, reads)
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "right secret after a wrong one")

	// A credential that needs a secret and stores none.
	_, err = f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "c-3", DeviceToken: "dev", CredentialType: string(CredentialMqttBasic),
		CredentialId: "no-secret", Enabled: true,
	})
	require.NoError(t, err)
	for i := 1; i <= 2; i++ {
		_, err, reads := f.check(basic("no-secret", "anything"))
		require.ErrorIs(t, err, ErrCredentialMisconfigured, "misconfigured, check %d", i)
		require.Equal(t, 1, reads, "misconfigured, check %d", i)
	}
}

// A hit compares the secret exactly as a database row is compared: a wrong, empty or
// absent one is refused from memory, and the right one still authenticates.
func TestACachedCredentialStillComparesTheSecret(t *testing.T) {
	f := newCredentialCacheFixture(t)
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "fill")
	for name, p := range map[string]*PresentedCredential{
		"wrong": basic("cred-1", "wrong"),
		"empty": basic("cred-1", ""),
		"nil":   {CredentialType: string(CredentialMqttBasic), CredentialId: "cred-1"},
	} {
		_, err, reads := f.check(p)
		require.ErrorIs(t, err, ErrCredentialSecretMismatch, "%s secret", name)
		require.Zero(t, reads, "%s secret was not answered from memory", name)
	}
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "right secret after the refusals")
}

// An expiry takes effect at its time on a hit, whatever the cache's own TTL says.
func TestACachedCredentialExpiresAtItsExpiry(t *testing.T) {
	f := newCredentialCacheFixture(t)
	expires := f.t0.Add(time.Second).UTC().Format(time.RFC3339)
	_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "c-4", DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
		CredentialId: "tok-exp", Enabled: true, ExpiresAt: &expires,
	})
	require.NoError(t, err)
	f.mustAuthenticate(t, accessToken("tok-exp"), 1, "fill before expiry")
	f.stmts.reset()
	_, err = f.capi.AuthenticateDevice(f.ctx, accessToken("tok-exp"), f.t0.Add(2*time.Second))
	require.ErrorIs(t, err, ErrCredentialExpired)
	require.Zero(t, f.credentialReads(), "the expired credential was not answered from memory")
}

// 🔑 EVERY CHANGE TO A CREDENTIAL OR ITS DEVICE IS SEEN ON THE NEXT CHECK, with the clock
// frozen so the TTL cannot explain it. Each row fills the cache, makes one change through
// the plain Api (as the GraphQL mutations do), and checks again.
func TestEveryChangeToACredentialOrItsDeviceIsSeenOnTheNextCheck(t *testing.T) {
	type row struct {
		name   string
		change func(t *testing.T, f ccFixture)
		check  func(t *testing.T, f ccFixture)
	}
	refused := func(want error) func(t *testing.T, f ccFixture) {
		return func(t *testing.T, f ccFixture) {
			_, err, reads := f.check(basic("cred-1", "s3cret"))
			require.ErrorIs(t, err, want)
			require.Equal(t, 1, reads, "the refusal came from the database")
		}
	}
	update := func(req *DeviceCredentialUpdateRequest) func(t *testing.T, f ccFixture) {
		return func(t *testing.T, f ccFixture) {
			_, err := f.api.UpdateDeviceCredential(f.ctx, "c-1", req)
			require.NoError(t, err)
		}
	}
	rows := []row{
		{"disable", update(&DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}),
			refused(ErrCredentialNotResolved)},
		{"expiry in the past", update(&DeviceCredentialUpdateRequest{
			ExpiresAt: dcgraphql.OptionalStringOf(f0Past())}), refused(ErrCredentialExpired)},
		{"new secret", update(&DeviceCredentialUpdateRequest{CredentialValue: dcgraphql.OptionalStringOf("n3w")}),
			func(t *testing.T, f ccFixture) {
				_, err, _ := f.check(basic("cred-1", "s3cret"))
				require.ErrorIs(t, err, ErrCredentialSecretMismatch, "the old secret")
				f.mustAuthenticate(t, basic("cred-1", "n3w"), 1, "the new secret")
			}},
		{"credential id changed", update(&DeviceCredentialUpdateRequest{CredentialId: dcgraphql.OptionalStringOf("cred-9")}),
			refused(ErrCredentialNotResolved)},
		{"re-pointed to dev2", update(&DeviceCredentialUpdateRequest{DeviceToken: dcgraphql.OptionalStringOf("dev2")}),
			func(t *testing.T, f ccFixture) {
				d := f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "after the re-point")
				require.Equal(t, "dev2", d.Token)
			}},
		{"credential deleted", func(t *testing.T, f ccFixture) {
			deleted, err := f.api.DeleteDeviceCredential(f.ctx, "c-1")
			require.NoError(t, err)
			require.True(t, deleted)
		}, refused(ErrCredentialNotResolved)},
		{"device deleted", func(t *testing.T, f ccFixture) {
			deleted, err := f.api.DeleteDevice(f.ctx, "dev")
			require.NoError(t, err)
			require.True(t, deleted)
		}, refused(ErrCredentialNotResolved)},
		{"device replaced", func(t *testing.T, f ccFixture) {
			_, err := f.api.ReplaceDevice(f.ctx, &DeviceReplaceRequest{DeviceToken: "dev"}, "test", f.t0)
			require.NoError(t, err)
		}, refused(ErrCredentialNotResolved)},
		{"device external id changed", func(t *testing.T, f ccFixture) {
			_, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{ExternalId: dcgraphql.OptionalStringOf("ext-2")})
			require.NoError(t, err)
		}, func(t *testing.T, f ccFixture) {
			d := f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "after the device edit")
			require.True(t, d.ExternalId.Valid)
			require.Equal(t, "ext-2", d.ExternalId.String)
		}},
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.name
	}
	require.Equal(t, []string{"disable", "expiry in the past", "new secret", "credential id changed",
		"re-pointed to dev2", "credential deleted", "device deleted", "device replaced",
		"device external id changed"}, names, "the table is the list of writes that must evict")

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			f := newCredentialCacheFixture(t)
			f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "fill")
			f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "held")
			r.change(t, f)
			r.check(t, f)
		})
	}

	// The control: with no evictor wired, the same disable is NOT seen, which shows the
	// rows above can see a missing eviction.
	t.Run("control: no evictor", func(t *testing.T) {
		f := newCredentialCacheFixture(t)
		f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "fill")
		f.api.CacheEvictor = nil
		update(&DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})(t, f)
		f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "a disable nothing evicted is still answered from memory")
	})
}

func f0Past() string { return time.Unix(1_500_000_000, 0).UTC().Format(time.RFC3339) }

// The same credential id under two tenants is two credentials: filling one tenant's never
// answers the other's.
func TestTheCacheIsTenantScoped(t *testing.T) {
	f := newCredentialCacheFixture(t)
	globex := core.WithTenant(context.Background(), "globex")
	_, err := f.api.CreateDeviceType(globex, &DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	_, err = f.api.CreateDevice(globex, &DeviceCreateRequest{Token: "gdev", DeviceTypeToken: "dt"})
	require.NoError(t, err)
	_, err = f.api.CreateDeviceCredential(globex, &DeviceCredentialCreateRequest{
		Token: "g-1", DeviceToken: "gdev", CredentialType: string(CredentialAccessToken), CredentialId: "tok-1", Enabled: true,
	})
	require.NoError(t, err)

	require.Equal(t, "dev", f.mustAuthenticate(t, accessToken("tok-1"), 1, "acme fill").Token)
	f.stmts.reset()
	d, err := f.capi.AuthenticateDevice(globex, accessToken("tok-1"), f.t0)
	require.NoError(t, err)
	require.Equal(t, "gdev", d.Token)
	require.Equal(t, 1, f.credentialReads(), "globex's check was answered from acme's entry")
}

// The same id under two types is two credentials.
func TestTheSameIdUnderTwoTypesIsTwoEntries(t *testing.T) {
	f := newCredentialCacheFixture(t)
	_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "c-x", DeviceToken: "dev2", CredentialType: string(CredentialX509Certificate), CredentialId: "tok-1", Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "dev", f.mustAuthenticate(t, accessToken("tok-1"), 1, "token").Token)
	cert := &PresentedCredential{CredentialType: string(CredentialX509Certificate), CredentialId: "tok-1"}
	require.Equal(t, "dev2", f.mustAuthenticate(t, cert, 1, "certificate").Token)
	require.Equal(t, "dev", f.mustAuthenticate(t, accessToken("tok-1"), 0, "token again").Token)
	require.Equal(t, "dev2", f.mustAuthenticate(t, cert, 0, "certificate again").Token)
}

// An access token's id IS its secret, so no held string equals the presented id, and every
// key is a hash.
func TestTheCacheHoldsNoCredentialIdInTheClear(t *testing.T) {
	f := newCredentialCacheFixture(t)
	f.mustAuthenticate(t, accessToken("tok-1"), 1, "fill")
	require.Equal(t, 1, len(f.cache.byKey))
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.String:
			require.NotEqual(t, "tok-1", v.String(), "%s holds the credential id", path)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i), path+"."+v.Type().Field(i).Name)
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		}
	}
	for k, el := range f.cache.byKey {
		require.Equal(t, sha256.Size, len(k))
		walk(reflect.ValueOf(el.Value), "entry")
	}
}

// blockCredentialRead holds the next device_credentials SELECT until release is closed, and
// closes reading once it is held.
func blockCredentialRead(t *testing.T, f ccFixture) (reading, release chan struct{}) {
	t.Helper()
	reading, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	require.NoError(t, f.api.RDB.Database.Callback().Query().Before("gorm:query").Register(
		"test:block-credential-read", func(tx *gorm.DB) {
			if tx.Statement.Table != "device_credentials" {
				return
			}
			once.Do(func() {
				close(reading)
				<-release
			})
		}))
	return reading, release
}

// A read that was in flight when the credential changed does not put what it read back:
// the next check reads the database again.
func TestAnEvictionDuringALookupIsNotUndone(t *testing.T) {
	f := newCredentialCacheFixture(t)
	reading, release := blockCredentialRead(t, f)
	done := make(chan error, 1)
	go func() {
		_, err := f.capi.AuthenticateDevice(f.ctx, basic("cred-1", "s3cret"), f.t0)
		done <- err
	}()
	<-reading
	f.cache.EvictDevices("acme", []uint{f.devId})
	close(release)
	require.NoError(t, <-done)
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "the check after the in-flight read")
}

// The TTL counts from when the read STARTED: a read that took 2 s keeps its entry to
// start + 5 s, not to the end of the read + 5 s, so a replica that missed an eviction holds
// the old credential no longer than 5 s after the change.
func TestAnEntryExpiresFiveSecondsAfterItsReadBegan(t *testing.T) {
	f := newCredentialCacheFixture(t)
	reading, release := blockCredentialRead(t, f)
	done := make(chan error, 1)
	go func() {
		_, err := f.capi.AuthenticateDevice(f.ctx, basic("cred-1", "s3cret"), f.t0)
		done <- err
	}()
	<-reading
	f.clock.set(f.t0.Add(2 * time.Second))
	close(release)
	require.NoError(t, <-done)
	f.clock.set(f.t0.Add(4900 * time.Millisecond))
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "a hit inside the read's 5 s")
	f.clock.set(f.t0.Add(CredentialCacheTTL))
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "5 s after the read began")
}

// A row read by the password connect's finder carries no type, and evaluateCredential would
// skip the secret compare on it. fill refuses it, as returned and even if it claimed to be
// enabled, so no wrong password can be answered from it.
func TestAConnectRowIsNeverCached(t *testing.T) {
	f := newCredentialCacheFixture(t)
	key := credentialCacheKey("acme", string(CredentialMqttBasic), "cred-1")
	for _, forceEnabled := range []bool{false, true} {
		row, err := f.api.deviceCredentialForConnect(f.ctx, string(CredentialMqttBasic), "cred-1")
		require.NoError(t, err)
		require.Empty(t, row.CredentialType, "the connect finder's row now carries a type; this test no longer proves anything")
		row.Enabled = row.Enabled || forceEnabled
		gen, at := f.cache.readStarted()
		f.cache.fill(key, string(CredentialMqttBasic), row, gen, at)
		entries, _ := f.cache.len()
		require.Zero(t, entries, "a connect row was held (enabled forced: %v)", forceEnabled)
	}
	_, err, reads := f.check(basic("cred-1", "wrong"))
	require.ErrorIs(t, err, ErrCredentialSecretMismatch)
	require.Equal(t, 1, reads)
}

// fill refuses a row that is not enabled, or that carries no device. The per-event finder
// matches enabled rows with their device only, so through it neither can arrive today: this
// pins the refusal itself, so a finder that one day returns either cannot fill the cache. The
// enabled row with its device is the control, held by the same call.
func TestFillRefusesADisabledOrDevicelessRow(t *testing.T) {
	c := NewCredentialCache(16, 1<<20)
	disabled := synthCred("acme", 1, 0)
	disabled.Enabled = false
	fillSynth(c, "acme", "disabled", disabled)
	entries, _ := c.len()
	require.Zero(t, entries, "a disabled row was held")

	deviceless := synthCred("acme", 2, 0)
	deviceless.Device = nil
	fillSynth(c, "acme", "deviceless", deviceless)
	entries, _ = c.len()
	require.Zero(t, entries, "a row with no device was held")

	fillSynth(c, "acme", "enabled", synthCred("acme", 3, 0))
	entries, _ = c.len()
	require.Equal(t, 1, entries, "the control: an enabled row with its device is held")
}

// What a hit returns is the caller's own: changing it does not change the next hit.
func TestAHitHandsBackADeviceTheCallerCannotChangeInTheCache(t *testing.T) {
	f := newCredentialCacheFixture(t)
	_, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Metadata: dcgraphql.OptionalStringOf(`{"a":1}`)})
	require.NoError(t, err)
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "fill")
	d := f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "hit")
	require.NotNil(t, d.Metadata)
	d.Token = "tampered"
	(*d.Metadata)[2] = 'Z'
	again := f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "next hit")
	require.Equal(t, "dev", again.Token)
	require.Equal(t, `{"a":1}`, string(*again.Metadata))
}

// synthCred is a row as the per-event finder returns it, for exercising the cache directly.
func synthCred(tenant string, deviceId uint, metadataBytes int) *DeviceCredential {
	d := &Device{}
	d.ID, d.TenantId, d.Token = deviceId, tenant, "d"+strconv.Itoa(int(deviceId))
	if metadataBytes > 0 {
		m := datatypes.JSON(make([]byte, metadataBytes))
		d.Metadata = &m
	}
	c := &DeviceCredential{DeviceId: deviceId, Device: d, CredentialType: string(CredentialAccessToken), Enabled: true}
	c.TenantId = tenant
	return c
}

func fillSynth(c *CredentialCache, tenant, id string, cred *DeviceCredential) credentialKey {
	key := credentialCacheKey(tenant, string(CredentialAccessToken), id)
	gen, at := c.readStarted()
	c.fill(key, string(CredentialAccessToken), cred, gen, at)
	return key
}

// requireIndexed holds the device index to the entries: one map key per device held, one
// slice element per entry, each naming an entry filed under that device.
func requireIndexed(t *testing.T, c *CredentialCache, what string) {
	t.Helper()
	devices := map[deviceKey]bool{}
	for _, el := range c.byKey {
		devices[el.Value.(*credentialEntry).dev] = true
	}
	require.Equal(t, len(devices), len(c.byDevice), "%s: devices indexed", what)
	n := 0
	for dev, keys := range c.byDevice {
		n += len(keys)
		for _, k := range keys {
			el, ok := c.byKey[k]
			require.True(t, ok, "%s: the index names an entry that is gone", what)
			require.Equal(t, dev, el.Value.(*credentialEntry).dev, "%s: an entry indexed under the wrong device", what)
		}
	}
	require.Equal(t, len(c.byKey), n, "%s: index size", what)
	require.Equal(t, c.order.Len(), len(c.byKey), "%s: list and map", what)
	sum := 0
	for el := c.order.Front(); el != nil; el = el.Next() {
		sum += credentialEntrySize(el.Value.(*credentialEntry))
	}
	require.Equal(t, sum, c.bytes, "%s: bytes counted", what)
}

func TestCredentialCacheBounds(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	clock := &ccClock{t: t0}

	t.Run("the least recently USED goes first", func(t *testing.T) {
		c := NewCredentialCache(2, 1<<20, withCredentialCacheClock(clock.now))
		a := fillSynth(c, "acme", "a", synthCred("acme", 1, 0))
		b := fillSynth(c, "acme", "b", synthCred("acme", 2, 0))
		_, ok := c.lookup("acme", a)
		require.True(t, ok)
		cc := fillSynth(c, "acme", "c", synthCred("acme", 3, 0))
		_, ok = c.lookup("acme", b)
		require.False(t, ok, "b, the least recently used, was kept")
		for _, k := range []credentialKey{a, cc} {
			_, ok = c.lookup("acme", k)
			require.True(t, ok)
		}
		requireIndexed(t, c, "after a capacity eviction")
	})

	t.Run("an entry too big for the cache is not held, and takes the one it replaced", func(t *testing.T) {
		c := NewCredentialCache(10, 4096, withCredentialCacheClock(clock.now))
		a := fillSynth(c, "acme", "a", synthCred("acme", 1, 0))
		_, ok := c.lookup("acme", a)
		require.True(t, ok)
		fillSynth(c, "acme", "a", synthCred("acme", 1, 8192))
		_, ok = c.lookup("acme", a)
		require.False(t, ok, "the older copy was left in place of the oversized one")
		entries, bytes := c.len()
		require.Zero(t, entries)
		require.Zero(t, bytes)
		requireIndexed(t, c, "after an oversize refusal")
	})

	t.Run("the byte bound binds", func(t *testing.T) {
		c := NewCredentialCache(100, 3*credentialEntryOverhead+3000, withCredentialCacheClock(clock.now))
		for i := 1; i <= 4; i++ {
			fillSynth(c, "acme", strconv.Itoa(i), synthCred("acme", uint(i), 1000))
		}
		entries, bytes := c.len()
		require.Less(t, entries, 4)
		require.LessOrEqual(t, bytes, 3*credentialEntryOverhead+3000)
		requireIndexed(t, c, "after the byte bound evicted")
	})

	t.Run("a re-fill under another device leaves nothing indexed under the first", func(t *testing.T) {
		c := NewCredentialCache(10, 1<<20, withCredentialCacheClock(clock.now))
		fillSynth(c, "acme", "a", synthCred("acme", 1, 0))
		fillSynth(c, "acme", "a", synthCred("acme", 2, 0))
		require.Equal(t, 1, len(c.byKey))
		_, stale := c.byDevice[deviceKey{"acme", 1}]
		require.False(t, stale, "the device the credential left still indexes it")
		requireIndexed(t, c, "after a re-point re-fill")
	})

	t.Run("expired entries are swept, index and all", func(t *testing.T) {
		clock := &ccClock{t: t0}
		c := NewCredentialCache(100, 1<<20, withCredentialCacheClock(clock.now))
		for i := 1; i <= 5; i++ {
			fillSynth(c, "acme", strconv.Itoa(i), synthCred("acme", uint(i), 0))
		}
		clock.set(t0.Add(CredentialCacheTTL))
		fillSynth(c, "acme", "new", synthCred("acme", 99, 0))
		require.Equal(t, 1, len(c.byKey))
		require.Equal(t, 1, len(c.byDevice))
		requireIndexed(t, c, "after an expiry sweep")
	})
}

// credentialEntryOverhead covers what an entry costs beyond what credentialEntrySize counts
// by length: the entry struct and its list element, a slot in each map at its emptiest (a
// map just past growth is 7/16 full, as core's in-process cache reckons), and the key's
// place in the device index.
func TestCredentialEntryOverheadCoversTheStructs(t *testing.T) {
	slot := func(n uintptr) uintptr { return ((n+1)*16 + 6) / 7 }
	need := unsafe.Sizeof(credentialEntry{}) + unsafe.Sizeof(list.Element{}) +
		slot(sha256.Size+8) + slot(unsafe.Sizeof(deviceKey{})+24) + sha256.Size
	t.Logf("an entry needs %d B beyond its strings; credentialEntryOverhead is %d", need, credentialEntryOverhead)
	require.GreaterOrEqual(t, uintptr(credentialEntryOverhead), need)
}

// A broadcast eviction drops the named devices of the named tenant only, and a key that is
// not a row id is skipped without stopping the rest.
func TestABroadcastEvictionDropsOnlyItsTenantsDevices(t *testing.T) {
	c := NewCredentialCache(10, 1<<20)
	acme := fillSynth(c, "acme", "a", synthCred("acme", 7, 0))
	globex := fillSynth(c, "globex", "a", synthCred("globex", 7, 0))
	other := fillSynth(c, "acme", "b", synthCred("acme", 8, 0))

	c.ApplyEviction(messaging.CacheEviction{Tenant: "acme", Keys: []string{"x", "7"}})
	_, ok := c.lookup("acme", acme)
	require.False(t, ok, "acme's device 7 was not evicted")
	_, ok = c.lookup("globex", globex)
	require.True(t, ok, "globex's device 7 was evicted by acme's eviction")
	_, ok = c.lookup("acme", other)
	require.True(t, ok, "acme's device 8 was evicted")
	requireIndexed(t, c, "after a broadcast eviction")
}

// The writes name the devices whose cached credentials they change, under the ROW's tenant.
func TestEveryCredentialWriteNamesTheDevicesItChanges(t *testing.T) {
	setup := func(t *testing.T) (ccFixture, *captureEvictor) {
		f := newCredentialCacheFixture(t)
		ev := &captureEvictor{}
		f.api.CacheEvictor = ev
		return f, ev
	}
	acme := func(ids ...uint) []credentialEvict { return []credentialEvict{{"acme", ids}} }

	t.Run("a re-point names the device left and the device joined", func(t *testing.T) {
		f, ev := setup(t)
		_, err := f.api.UpdateDeviceCredential(f.ctx, "c-1", &DeviceCredentialUpdateRequest{DeviceToken: dcgraphql.OptionalStringOf("dev2")})
		require.NoError(t, err)
		require.Equal(t, acme(f.devId, f.dev2), ev.credentialEvicts)
	})
	t.Run("a metadata-only update still evicts", func(t *testing.T) {
		f, ev := setup(t)
		_, err := f.api.UpdateDeviceCredential(f.ctx, "c-1", &DeviceCredentialUpdateRequest{Metadata: dcgraphql.OptionalStringOf(`{}`)})
		require.NoError(t, err)
		require.Equal(t, acme(f.devId), ev.credentialEvicts)
	})
	t.Run("an update made under a system context names the row's tenant", func(t *testing.T) {
		f, ev := setup(t)
		sys := core.WithSystemContext(context.Background())
		_, err := f.api.UpdateDeviceCredential(sys, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		require.Equal(t, acme(f.devId), ev.credentialEvicts)
	})
	t.Run("a refused update names nothing", func(t *testing.T) {
		f, ev := setup(t)
		_, err := f.api.UpdateDeviceCredential(f.ctx, "c-1", &DeviceCredentialUpdateRequest{CredentialType: dcgraphql.OptionalStringOf("NOPE")})
		require.Error(t, err)
		require.Empty(t, ev.credentialEvicts)
	})
	t.Run("a credential delete names its device, and an unknown token nothing", func(t *testing.T) {
		f, ev := setup(t)
		deleted, err := f.api.DeleteDeviceCredential(f.ctx, "nope")
		require.NoError(t, err)
		require.False(t, deleted)
		require.Empty(t, ev.credentialEvicts)
		deleted, err = f.api.DeleteDeviceCredential(f.ctx, "c-1")
		require.NoError(t, err)
		require.True(t, deleted)
		require.Equal(t, acme(f.devId), ev.credentialEvicts)
		var n int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Model(&DeviceCredential{}).Where("token = ?", "c-1").Count(&n).Error)
		require.Zero(t, n, "the credential is still stored")
	})
	t.Run("a device delete names the device of every credential it removed", func(t *testing.T) {
		f, ev := setup(t)
		deleted, err := f.api.DeleteDevice(f.ctx, "dev2")
		require.NoError(t, err)
		require.True(t, deleted)
		require.Empty(t, ev.credentialEvicts, "dev2 holds no credential")
		deleted, err = f.api.DeleteDevice(f.ctx, "dev")
		require.NoError(t, err)
		require.True(t, deleted)
		// Two credentials, one device: two calls naming it, each folded to one id.
		require.Equal(t, []credentialEvict{{"acme", []uint{f.devId}}, {"acme", []uint{f.devId}}}, ev.credentialEvicts)
	})
	t.Run("a replacement names the device", func(t *testing.T) {
		f, ev := setup(t)
		_, err := f.api.ReplaceDevice(f.ctx, &DeviceReplaceRequest{DeviceToken: "dev"}, "test", f.t0)
		require.NoError(t, err)
		require.Equal(t, acme(f.devId), ev.credentialEvicts)
	})
	t.Run("a device update names the device", func(t *testing.T) {
		f, ev := setup(t)
		_, err := f.api.UpdateDevice(f.ctx, "dev2", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("n")})
		require.NoError(t, err)
		require.Equal(t, acme(f.dev2), ev.credentialEvicts)
	})
}

// An eviction with no tenant could only evict nothing; it evicts nothing and is not
// broadcast, rather than reaching an empty tenant everywhere.
func TestAnEvictionWithNoTenantEvictsNothing(t *testing.T) {
	f := newCredentialCacheFixture(t)
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "fill")
	f.capi.EvictDeviceCredentials(f.ctx, "", []uint{f.devId})
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "after an eviction naming no tenant")
	f.capi.EvictDeviceCredentials(f.ctx, "acme", []uint{f.devId})
	_, err, reads := f.check(basic("cred-1", "s3cret"))
	require.NoError(t, err)
	require.Equal(t, 1, reads, "an eviction naming the tenant evicted nothing")
}
