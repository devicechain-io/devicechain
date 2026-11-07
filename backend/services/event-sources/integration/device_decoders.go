// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
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

const deviceDecoderQuery = `query($deviceToken: String!) {
  deviceDecoder(deviceToken: $deviceToken) {
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

// DeviceDecoderBackend asks device-management about the decoder of a device's type.
type DeviceDecoderBackend interface {
	// DeviceDecoder returns the plan. An unknown device and a type with no decoder are the same
	// answer (Present false). An error is a failure to ask, ErrDeviceDecoderUnsupported included.
	DeviceDecoder(ctx context.Context, tenant, deviceToken string) (*DecoderPlan, error)
}

// DeviceDecoder is DeviceDecoderBackend over device-management's GraphQL endpoint.
func (d *DM) DeviceDecoder(ctx context.Context, tenant, deviceToken string) (*DecoderPlan, error) {
	var out struct {
		Plan struct {
			Present             bool    `json:"present"`
			Input               *string `json:"input"`
			MaxReadings         int     `json:"maxReadings"`
			ComposedCost        int64   `json:"composedCost"`
			ParsesJSON          bool    `json:"parsesJson"`
			ProfileVersionToken string  `json:"profileVersionToken"`
		} `json:"deviceDecoder"`
	}
	if err := d.client.Query(ctx, d.url, tenant, deviceDecoderQuery, map[string]any{"deviceToken": deviceToken}, &out); err != nil {
		if IsDeviceDecoderUnsupported(err) {
			return nil, ErrDeviceDecoderUnsupported
		}
		return nil, err
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
	TTL      time.Duration // freshness of an answer, present or absent
	StaleMax time.Duration // longest an answer is served when device-management cannot be asked
	Entries  int
	// FillsPerSecond and FillBurst bound the lookups this replica makes for keys it has no fresh
	// answer for. The key is built from an unauthenticated path segment, so without the bound a
	// flood of invented device tokens would become a flood of device-management calls.
	//
	// There are two buckets, and a fill spends from both: the tenant's own, so one tenant's flood of
	// invented device tokens cannot starve another tenant's refreshes, and the replica's, so a flood
	// spread over invented tenants is still bounded. The replica bucket is sized above the steady
	// refresh rate of a full cache (Entries / TTL), or healthy traffic would be served stale.
	FillsPerSecond       float64
	FillBurst            float64
	TenantFillsPerSecond float64
	TenantFillBurst      float64
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
	DefaultMaxTenantBuckets     = 4096
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
	at   time.Time
}

// DeviceDecoders remembers, per (tenant, device), the decoder plan of the device's type.
//
// 🔴 EVERY ANSWER IS ADVISORY, as for Decoders: device-management decides what runs from the
// profile version it resolves, and this only chooses the charge and whether to carry the bytes.
//
// 🔴 THE HIT PATH IS THE HOT PATH. It is one read lock and one map read per uplink on the device
// wire (and nothing at all on the routes that do not use it); a fill takes the singleflight and the
// fill budget, which a hit never touches.
type DeviceDecoders struct {
	cfg     DeviceDecoderConfig
	backend DeviceDecoderBackend
	sf      singleflight.Group

	mu      sync.RWMutex
	entries map[string]deviceDecoderEntry

	fillMu     sync.Mutex
	fillTokens float64
	fillAt     time.Time
	tenants    map[string]*fillBucket
}

type fillBucket struct {
	tokens float64
	at     time.Time
}

// NewDeviceDecoders builds the cache over a backend.
func NewDeviceDecoders(backend DeviceDecoderBackend, cfg DeviceDecoderConfig) *DeviceDecoders {
	cfg = cfg.withDefaults()
	return &DeviceDecoders{cfg: cfg, backend: backend, entries: make(map[string]deviceDecoderEntry), fillTokens: cfg.FillBurst, fillAt: cfg.Now(), tenants: make(map[string]*fillBucket)}
}

// DeviceLookup is the answer to a device plan lookup.
type DeviceLookup struct {
	Plan   DecoderPlan
	Cached bool
	Stale  bool
}

// Lookup returns the plan for a device. An error is a failure to ask (ErrDeviceDecoderUnsupported
// and ErrDeviceDecoderBudget included), answered 503 with Retry-After.
func (d *DeviceDecoders) Lookup(ctx context.Context, tenant, device string) (DeviceLookup, error) {
	key := tenant + "\x00" + device
	now := d.cfg.Now()
	d.mu.RLock()
	e, have := d.entries[key]
	d.mu.RUnlock()
	if have {
		age := now.Sub(e.at)
		if age < d.cfg.TTL {
			return DeviceLookup{Plan: e.plan, Cached: true}, nil
		}
		if age >= d.cfg.StaleMax {
			have = false
		}
	}
	// 🔴 THE BUDGET IS SPENT INSIDE THE FLIGHT, once per backend call. Spent before it, every
	// concurrent miss for one cold device would pay for the one call they share, and a burst on a
	// single device could drain the tenant's bucket.
	v, err, _ := d.sf.Do(key, func() (any, error) {
		if !d.takeFill(tenant, d.cfg.Now()) {
			return nil, ErrDeviceDecoderBudget
		}
		plan, err := d.backend.DeviceDecoder(context.WithoutCancel(ctx), tenant, device)
		if err != nil {
			return nil, err
		}
		if plan == nil {
			plan = &DecoderPlan{}
		}
		fresh := deviceDecoderEntry{plan: *plan, at: d.cfg.Now()}
		d.put(key, fresh)
		return fresh, nil
	})
	if err != nil {
		if have && !errors.Is(err, ErrDeviceDecoderUnsupported) {
			return DeviceLookup{Plan: e.plan, Cached: true, Stale: true}, nil
		}
		return DeviceLookup{}, err
	}
	return DeviceLookup{Plan: v.(deviceDecoderEntry).plan}, nil
}

// takeFill spends one token from the tenant's bucket and one from the replica's. A tenant out of
// tokens spends nothing from the replica's, so a flood is charged to the tenant that sent it.
func (d *DeviceDecoders) takeFill(tenant string, now time.Time) bool {
	d.fillMu.Lock()
	defer d.fillMu.Unlock()
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
