// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"golang.org/x/sync/singleflight"
)

// ErrDeviceDecoderUnsupported is a device-management that does not serve deviceDecoder (the release
// before a device's own connection could be decoded). Unlike the integration route, where an old
// device-management means "no decoder, flatten here", it is NOT "absent" here: an absent plan on
// the device wire is refused, and refusing a device that does have a decoder would be wrong. The
// caller answers it as a retryable 503.
var ErrDeviceDecoderUnsupported = errors.New("integration: device-management does not serve deviceDecoder")

// ErrDeviceDecoderBudget is a fill refused because this replica's budget for plan lookups is spent.
var ErrDeviceDecoderBudget = errors.New("integration: this replica's device-decoder lookup budget is spent")

const deviceDecoderQuery = `query($deviceToken: String!, $credentialType: String!, $credentialId: String!, $credentialSecret: String) {
  deviceDecoder(deviceToken: $deviceToken, credentialType: $credentialType, credentialId: $credentialId, credentialSecret: $credentialSecret) {
    present input maxReadings composedCost parsesJson profileVersionToken
  }
}`

var unsupportedDeviceField = regexp.MustCompile(`Cannot query field "deviceDecoder" on type "Query"`)

// IsDeviceDecoderUnsupported reports whether err is exactly the validation error of a
// device-management that predates the query.
func IsDeviceDecoderUnsupported(err error) bool {
	var gqlErr *svcclient.GraphQLError
	if !errors.As(err, &gqlErr) || len(gqlErr.Messages) == 0 {
		return false
	}
	for _, m := range gqlErr.Messages {
		if !unsupportedDeviceField.MatchString(m) {
			return false
		}
	}
	return true
}

// DeviceCredential is the credential a device presented on the device-uplink route.
type DeviceCredential struct {
	Type   string
	ID     string
	Secret *string
}

// digest is a fixed-width fingerprint of the credential, so the plan cache is keyed by WHO is
// asking and not only by what is asked: an answer remembered for one credential is never served to
// a request that presents another. Each field is length-prefixed, so no two credentials share a
// preimage; nothing about the credential is kept but this one-way value.
func (c DeviceCredential) digest() string {
	h := sha256.New()
	var n [8]byte
	put := func(s string) {
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	put(c.Type)
	put(c.ID)
	if c.Secret != nil {
		put("1")
		put(*c.Secret)
	} else {
		put("0")
	}
	var sum [sha256.Size]byte
	return hex.EncodeToString(h.Sum(sum[:0]))
}

// DeviceDecoderBackend asks device-management about the decoder of a device's type, FOR A DEVICE
// THAT HAS PROVEN ITSELF.
type DeviceDecoderBackend interface {
	// DeviceDecoder authenticates the credential and returns the plan. A nil plan with a nil error
	// is "not authenticated", whatever the reason (a wrong or unknown credential, a credential that
	// is another device's, a device that does not exist): the one answer that discloses nothing. A
	// non-nil plan with Present false is an authenticated device whose type has no decoder. An error
	// is a failure to ask, ErrDeviceDecoderUnsupported included.
	DeviceDecoder(ctx context.Context, tenant, deviceToken string, cred DeviceCredential) (*DecoderPlan, error)
}

// DeviceDecoder is DeviceDecoderBackend over device-management's GraphQL endpoint.
func (d *DM) DeviceDecoder(ctx context.Context, tenant, deviceToken string, cred DeviceCredential) (*DecoderPlan, error) {
	var out struct {
		Plan *struct {
			Present             bool    `json:"present"`
			Input               *string `json:"input"`
			MaxReadings         int     `json:"maxReadings"`
			ComposedCost        int64   `json:"composedCost"`
			ParsesJSON          bool    `json:"parsesJson"`
			ProfileVersionToken string  `json:"profileVersionToken"`
		} `json:"deviceDecoder"`
	}
	vars := map[string]any{
		"deviceToken": deviceToken, "credentialType": cred.Type, "credentialId": cred.ID, "credentialSecret": cred.Secret,
	}
	if err := d.client.Query(ctx, d.url, tenant, deviceDecoderQuery, vars, &out); err != nil {
		if IsDeviceDecoderUnsupported(err) {
			return nil, ErrDeviceDecoderUnsupported
		}
		return nil, err
	}
	if out.Plan == nil {
		return nil, nil
	}
	p := &DecoderPlan{
		Present:             out.Plan.Present,
		MaxReadings:         max(out.Plan.MaxReadings, 0),
		Cost:                plancost.Shape{Composed: uint64(max(out.Plan.ComposedCost, 0)), ParsesJSON: out.Plan.ParsesJSON},
		ProfileVersionToken: out.Plan.ProfileVersionToken,
	}
	if out.Plan.Input != nil {
		p.Input = *out.Plan.Input
	}
	return p, nil
}

// DeviceDecoderConfig tunes the device plan cache. A zero field takes its default.
type DeviceDecoderConfig struct {
	TTL      time.Duration // freshness of an authenticated answer, present or absent
	StaleMax time.Duration // longest an authenticated answer is served when device-management cannot be asked
	// RefusedTTL is how long a "not authenticated" answer is remembered for the very same request
	// (same tenant, device and credential), so a device retrying a bad credential in a loop costs
	// device-management one question per window and not one per retry. It is short on purpose: a
	// credential that was just enrolled or fixed must not be refused for long.
	RefusedTTL time.Duration
	Entries    int
	// There are THREE budgets on the questions this replica puts to device-management, and which one
	// a question spends from is decided by what the replica already knows about the asker.
	//
	// 🔴 NONE IS KEYED BY THE URL. The tenant and device in the path are chosen by the caller before
	// anything is proven, so a bucket keyed by them is a bucket anyone can empty for anyone: naming a
	// victim tenant would exhaust its refreshes. A question is charged to a TENANT only once the
	// replica holds an authenticated answer for that very credential, which is the one fact that
	// makes the tenant the caller's own.
	//
	//   - A COLD question (no authenticated answer held for this credential) cannot be attributed to
	//     any tenant yet, so it spends from one replica-wide bucket, ColdFillsPerSecond/ColdFillBurst.
	//     This is the bound on a flood of invented credentials. It is also the limit this design
	//     cannot remove: while it is empty, a legitimate device on its FIRST contact with this
	//     replica is answered 503 with Retry-After, and a device already vouched for is unaffected.
	//   - A REFRESH (an authenticated answer held, now past its TTL) spends from the authenticated
	//     tenant's own bucket, TenantFillsPerSecond/TenantFillBurst, so one tenant's refreshes cannot
	//     starve another's, and from the replica's refresh bucket, FillsPerSecond/FillBurst, sized
	//     above the steady refresh rate of a full cache (Entries / TTL) or healthy traffic would be
	//     served stale.
	FillsPerSecond       float64
	FillBurst            float64
	TenantFillsPerSecond float64
	TenantFillBurst      float64
	ColdFillsPerSecond   float64
	ColdFillBurst        float64
	// MaxTenantBuckets bounds the per-tenant buckets kept; past it the oldest are dropped.
	MaxTenantBuckets int
	Now              func() time.Time
}

// Defaults of DeviceDecoderConfig.
const (
	DefaultDeviceDecoderEntries = 65536
	DefaultDeviceFillsPerSecond = 1500
	DefaultDeviceFillBurst      = 3000
	DefaultTenantFillsPerSecond = 300
	DefaultTenantFillBurst      = 600
	DefaultColdFillsPerSecond   = 1500
	DefaultColdFillBurst        = 3000
	DefaultMaxTenantBuckets     = 4096
	DefaultRefusedTTL           = 5 * time.Second
)

func (c DeviceDecoderConfig) withDefaults() DeviceDecoderConfig {
	if c.TTL <= 0 {
		c.TTL = DefaultDecoderTTL
	}
	if c.StaleMax <= 0 {
		c.StaleMax = 15 * time.Minute
	}
	if c.StaleMax < c.TTL {
		c.StaleMax = c.TTL
	}
	if c.RefusedTTL <= 0 {
		c.RefusedTTL = DefaultRefusedTTL
	}
	if c.Entries <= 0 {
		c.Entries = DefaultDeviceDecoderEntries
	}
	if c.FillsPerSecond <= 0 {
		c.FillsPerSecond = DefaultDeviceFillsPerSecond
	}
	if c.FillBurst <= 0 {
		c.FillBurst = DefaultDeviceFillBurst
	}
	if c.TenantFillsPerSecond <= 0 {
		c.TenantFillsPerSecond = DefaultTenantFillsPerSecond
	}
	if c.TenantFillBurst <= 0 {
		c.TenantFillBurst = DefaultTenantFillBurst
	}
	if c.ColdFillsPerSecond <= 0 {
		c.ColdFillsPerSecond = DefaultColdFillsPerSecond
	}
	if c.ColdFillBurst <= 0 {
		c.ColdFillBurst = DefaultColdFillBurst
	}
	if c.MaxTenantBuckets <= 0 {
		c.MaxTenantBuckets = DefaultMaxTenantBuckets
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

type deviceDecoderEntry struct {
	plan DecoderPlan
	// authed is true when device-management authenticated the credential this entry is keyed by.
	// A false entry is a remembered refusal, held for RefusedTTL only.
	authed bool
	at     time.Time
}

// fresh reports whether the entry may be served without asking again.
func (e deviceDecoderEntry) fresh(now time.Time, cfg DeviceDecoderConfig) bool {
	age := now.Sub(e.at)
	if e.authed {
		return age < cfg.TTL
	}
	return age < cfg.RefusedTTL
}

// DeviceDecoders remembers, per (tenant, device, credential), the decoder plan of the device's type
// and the fact that device-management authenticated the credential.
//
// 🔴 EVERY ANSWER IS ADVISORY, as for Decoders: device-management decides what runs from the
// profile version it resolves and authenticates every event again, and this only chooses the charge
// and whether to carry the bytes. What it must NOT be is an oracle: it answers a caller who has not
// proven the device's credential with one answer (Authenticated false), whatever the device and its
// type are, so the route's response cannot tell an anonymous caller which devices exist or which
// carry a decoder.
//
// 🔴 THE HIT PATH IS THE HOT PATH. It is one read lock, one map read and one fingerprint of the
// credential per uplink on the device wire (and nothing at all on the routes that do not use it); a
// fill takes the singleflight and a budget, which a hit never touches.
type DeviceDecoders struct {
	cfg     DeviceDecoderConfig
	backend DeviceDecoderBackend
	sf      singleflight.Group

	mu      sync.RWMutex
	entries map[string]deviceDecoderEntry

	fillMu     sync.Mutex
	coldTokens float64
	coldAt     time.Time
	fillTokens float64
	fillAt     time.Time
	tenants    map[string]*fillBucket

	// betweenMissAndFlight is a test seam: it runs after a lookup read a miss and before it joins the
	// flight, the one window in which another flight can store the answer. nil in service.
	betweenMissAndFlight func()
}

type fillBucket struct {
	tokens float64
	at     time.Time
}

// NewDeviceDecoders builds the cache over a backend.
func NewDeviceDecoders(backend DeviceDecoderBackend, cfg DeviceDecoderConfig) *DeviceDecoders {
	cfg = cfg.withDefaults()
	now := cfg.Now()
	return &DeviceDecoders{cfg: cfg, backend: backend, entries: make(map[string]deviceDecoderEntry),
		coldTokens: cfg.ColdFillBurst, coldAt: now, fillTokens: cfg.FillBurst, fillAt: now, tenants: make(map[string]*fillBucket)}
}

// DeviceLookup is the answer to a device plan lookup.
type DeviceLookup struct {
	// Authenticated is false when device-management did not authenticate the credential. Plan is then
	// the zero plan and carries nothing, present or absent.
	Authenticated bool
	Plan          DecoderPlan
	Cached        bool
	Stale         bool
}

// Lookup authenticates the credential and returns the plan for a device. An error is a failure to
// ask (ErrDeviceDecoderUnsupported and ErrDeviceDecoderBudget included), answered 503 with
// Retry-After.
func (d *DeviceDecoders) Lookup(ctx context.Context, tenant, device string, cred DeviceCredential) (DeviceLookup, error) {
	key := tenant + "\x00" + device + "\x00" + cred.digest()
	now := d.cfg.Now()
	e, have := d.get(key)
	if have {
		if e.fresh(now, d.cfg) {
			return lookupOf(e, true, false), nil
		}
		if now.Sub(e.at) >= d.cfg.StaleMax {
			have = false
		}
	}
	// An authenticated answer, however old (within StaleMax), is what lets a stale one stand in when
	// device-management cannot be asked. A remembered refusal never stands in for anything.
	stale := have && e.authed
	if d.betweenMissAndFlight != nil {
		d.betweenMissAndFlight()
	}
	// 🔴 THE BUDGET IS SPENT INSIDE THE FLIGHT, once per backend call. Spent before it, every
	// concurrent miss for one cold device would pay for the one call they share, and a burst on a
	// single device could drain a bucket.
	v, err, _ := d.sf.Do(key, func() (any, error) {
		// 🔴 THE CACHE IS READ AGAIN INSIDE THE FLIGHT. A goroutine that read a miss can reach here
		// just after another flight for this key finished and stored its answer; without this it
		// would spend a token and ask again for something that is now held.
		at := d.cfg.Now()
		cur, ok := d.get(key)
		if ok && cur.fresh(at, d.cfg) {
			return cur, nil
		}
		// Only an authenticated answer still inside StaleMax makes this a refresh charged to a tenant.
		refresh := ok && cur.authed && at.Sub(cur.at) < d.cfg.StaleMax
		if !d.takeFill(tenant, refresh, at) {
			return nil, ErrDeviceDecoderBudget
		}
		plan, err := d.backend.DeviceDecoder(context.WithoutCancel(ctx), tenant, device, cred)
		if err != nil {
			return nil, err
		}
		fresh := deviceDecoderEntry{at: d.cfg.Now()}
		if plan != nil {
			fresh.plan, fresh.authed = *plan, true
		}
		d.put(key, fresh)
		return fresh, nil
	})
	if err != nil {
		// A refusal stored since this request read the entry (by a flight that finished first) is newer
		// than what it holds, and a stale authentication must never stand in over it.
		if cur, ok := d.get(key); stale && !errors.Is(err, ErrDeviceDecoderUnsupported) && !(ok && !cur.authed) {
			return lookupOf(e, true, true), nil
		}
		return DeviceLookup{}, err
	}
	// The flight may have returned the entry a concurrent flight stored: either way it is an answer
	// obtained now, not one remembered from before this request.
	return lookupOf(v.(deviceDecoderEntry), false, false), nil
}

func lookupOf(e deviceDecoderEntry, cached, stale bool) DeviceLookup {
	if !e.authed {
		return DeviceLookup{Cached: cached}
	}
	return DeviceLookup{Authenticated: true, Plan: e.plan, Cached: cached, Stale: stale}
}

func (d *DeviceDecoders) get(key string) (deviceDecoderEntry, bool) {
	d.mu.RLock()
	e, ok := d.entries[key]
	d.mu.RUnlock()
	return e, ok
}

// takeFill spends the budget a question is charged to (see DeviceDecoderConfig). A refresh spends
// one token from the authenticated tenant's bucket and one from the replica's refresh bucket, and a
// tenant out of tokens spends nothing from the replica's, so a flood is charged to the tenant that
// sent it. A cold question spends one token from the cold bucket and nothing else.
func (d *DeviceDecoders) takeFill(tenant string, refresh bool, now time.Time) bool {
	d.fillMu.Lock()
	defer d.fillMu.Unlock()
	if !refresh {
		if el := now.Sub(d.coldAt); el > 0 {
			d.coldTokens = min(d.cfg.ColdFillBurst, d.coldTokens+el.Seconds()*d.cfg.ColdFillsPerSecond)
			d.coldAt = now
		}
		if d.coldTokens < 1 {
			return false
		}
		d.coldTokens--
		return true
	}
	if el := now.Sub(d.fillAt); el > 0 {
		d.fillTokens = min(d.cfg.FillBurst, d.fillTokens+el.Seconds()*d.cfg.FillsPerSecond)
		d.fillAt = now
	}
	b, ok := d.tenants[tenant]
	if !ok {
		if len(d.tenants) >= d.cfg.MaxTenantBuckets {
			n := 0
			for k := range d.tenants {
				delete(d.tenants, k)
				if n++; n >= d.cfg.MaxTenantBuckets/16+1 {
					break
				}
			}
		}
		b = &fillBucket{tokens: d.cfg.TenantFillBurst, at: now}
		d.tenants[tenant] = b
	}
	if el := now.Sub(b.at); el > 0 {
		b.tokens = min(d.cfg.TenantFillBurst, b.tokens+el.Seconds()*d.cfg.TenantFillsPerSecond)
		b.at = now
	}
	if b.tokens < 1 || d.fillTokens < 1 {
		return false
	}
	b.tokens--
	d.fillTokens--
	return true
}

func (d *DeviceDecoders) put(key string, e deviceDecoderEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.entries[key]; !ok && len(d.entries) >= d.cfg.Entries {
		evictSome(d.entries, d.cfg.Entries/16+1, func(old deviceDecoderEntry) bool { return e.at.Sub(old.at) >= d.cfg.StaleMax })
	}
	d.entries[key] = e
}
