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

// DecoderPlan is what ingest needs to know about the payload decoder of a device type: whether
// there is one, what it reads, and what an uplink for it is charged. The zero value is "no
// decoder", which is Cut A: the sender's decoded object is flattened here.
type DecoderPlan struct {
	// Present is true when the type's active profile version declares a decoder, usable or not. An
	// unusable one still reaches the resolver, which records the failure instead of flattening.
	Present bool
	// Input is the input kind the decoder needs ("FRAME", "JSON", "DECODED"), empty when unknown.
	Input string
	// MaxReadings is the most readings one decode produces.
	MaxReadings int
	// Cost prices an uplink's decode by its actual size.
	Cost plancost.Shape
	// ProfileVersionToken is the version the answer is for, "{profile}@{version}".
	ProfileVersionToken string
}

// ErrDecoderUnauthenticated is device-management's answer when it no longer accepts the token
// the request was authenticated with (a rotation between the pre-check and now).
var ErrDecoderUnauthenticated = errors.New("integration: device-management does not accept the integration's token")

// ErrDecoderUnsupported is a device-management that does not serve integrationDecoder: a replica
// of the release before payload decoders, during a rolling upgrade. It is the one error that
// means "there is no decoder" rather than "could not ask".
var ErrDecoderUnsupported = errors.New("integration: device-management does not serve integrationDecoder")

// DecoderBackend asks device-management whether a device type has a decoder.
type DecoderBackend interface {
	// Decoder returns the plan. A nil plan with a nil error is an authentication failure. An error
	// is a failure to ask or to understand the answer, EXCEPT ErrDecoderUnsupported.
	Decoder(ctx context.Context, tenant, integration, bearer, deviceTypeToken string) (*DecoderPlan, error)
}

// The query.
const decoderQuery = `query($integration: String!, $secret: String!, $deviceTypeToken: String!) {
  integrationDecoder(integration: $integration, secret: $secret, deviceTypeToken: $deviceTypeToken) {
    present input maxReadings composedCost parsesJson parsesDecoded profileVersionToken
  }
}`

// unsupportedField matches the validation error a device-management without the QUERY answers:
// graphql-go's FieldsOnCorrectTypeRule. It is deliberately narrow. Any other error is a failure to
// ask, because the 503 it becomes is the safe answer to something this build does not understand.
//
// 🔴 A DEVICE-MANAGEMENT THAT SERVES THE QUERY BUT NOT ONE OF THE PLAN'S FIELDS IS NOT "NO
// DECODER". It can have a decoder, and flattening that type's uplinks would store readings the
// decoder was meant to produce; so the missing field is an error, and the uplink waits (503) for a
// device-management that can answer completely.
var unsupportedField = regexp.MustCompile(`Cannot query field "integrationDecoder" on type "Query"`)

// IsDecoderUnsupported reports whether err is exactly the validation error of a device-management
// that predates the decoder query, and nothing else: every message must be one.
func IsDecoderUnsupported(err error) bool {
	var gqlErr *svcclient.GraphQLError
	if !errors.As(err, &gqlErr) || len(gqlErr.Messages) == 0 {
		return false
	}
	for _, m := range gqlErr.Messages {
		if !unsupportedField.MatchString(m) {
			return false
		}
	}
	return true
}

// Decoder is DecoderBackend over device-management's GraphQL endpoint.
func (d *DM) Decoder(ctx context.Context, tenant, integration, bearer, deviceTypeToken string) (*DecoderPlan, error) {
	var out struct {
		Plan *struct {
			Present             bool    `json:"present"`
			Input               *string `json:"input"`
			MaxReadings         int     `json:"maxReadings"`
			ComposedCost        int64   `json:"composedCost"`
			ParsesJSON          bool    `json:"parsesJson"`
			ParsesDecoded       bool    `json:"parsesDecoded"`
			ProfileVersionToken string  `json:"profileVersionToken"`
		} `json:"integrationDecoder"`
	}
	vars := map[string]any{"integration": integration, "secret": bearer, "deviceTypeToken": deviceTypeToken}
	if err := d.client.Query(ctx, d.url, tenant, decoderQuery, vars, &out); err != nil {
		if IsDecoderUnsupported(err) {
			return nil, ErrDecoderUnsupported
		}
		return nil, err
	}
	if out.Plan == nil {
		return nil, nil
	}
	p := &DecoderPlan{
		Present:     out.Plan.Present,
		MaxReadings: max(out.Plan.MaxReadings, 0),
		Cost: plancost.Shape{
			Composed: uint64(max(out.Plan.ComposedCost, 0)), ParsesJSON: out.Plan.ParsesJSON, ParsesDecoded: out.Plan.ParsesDecoded,
		},
		ProfileVersionToken: out.Plan.ProfileVersionToken,
	}
	if out.Plan.Input != nil {
		p.Input = *out.Plan.Input
	}
	return p, nil
}

// DecoderConfig tunes the decoder-plan cache. A zero field takes its default.
type DecoderConfig struct {
	// TTL is how long an answer is remembered, a present decoder and an absent one alike. It is
	// the bound on how long this replica charges and selects on a plan that has since been
	// published over.
	TTL time.Duration
	// StaleMax is the longest an answer is served when device-management cannot be asked, counted
	// from when it was learned. It matches the integration's token cache (15 minutes by default),
	// so an outage that leaves Cut A serving leaves a decoder-less type serving too.
	StaleMax time.Duration
	Entries  int
	Now      func() time.Time
	// OnUnsupported, when set, is called each time device-management answers that it predates the
	// query (once per fill, not per cached read).
	OnUnsupported func()
}

// DefaultDecoderTTL is the lifetime of a remembered plan.
const DefaultDecoderTTL = time.Minute

func (c DecoderConfig) withDefaults() DecoderConfig {
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
		c.Entries = 10_000
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// DecoderLookup is the answer to a plan lookup.
type DecoderLookup struct {
	Plan DecoderPlan
	// Cached says no call was made.
	Cached bool
	// Stale says the answer is older than the TTL and was served because device-management could
	// not be asked.
	Stale bool
	// Unsupported says device-management predates payload decoders and Plan is the empty plan.
	Unsupported bool
}

// Decoders remembers, per (tenant, integration, device type), whether the type has a decoder and how
// an uplink for it is charged.
//
// 🔴 EVERY ANSWER IS ADVISORY, NOT AN AUTHORITY. The resolver decides what runs at the event from
// the profile version it resolves then; this decides only whether the uplink is flattened here or
// carried raw, and what the tenant is charged. A plan up to TTL stale therefore costs at worst the
// skew device-management counts (a charge made for another version), never a wrong reading.
//
// 🔴 AN OLD DEVICE-MANAGEMENT IS "NO DECODER", NOT AN OUTAGE. During a rolling upgrade this
// replica can be asking a device-management that has never heard of the query, and failing every
// integration request until it is replaced would turn a rollout into an outage for senders that
// have no decoder at all. That one validation error is remembered as an absent decoder; every
// other failure is returned, remembered as nothing, and becomes a 503.
//
// 🔴 A REAL ANSWER OUTLIVES ITS FRESHNESS. The lookup runs for every resolved uplink, so an entry
// that simply expired would turn a device-management outage into a 503 for every integration a
// minute in, while Cut A keeps serving from its token and device caches for minutes. The last real
// answer (a decoder present or absent) is therefore served stale, up to StaleMax, when asking
// fails. An error never creates or refreshes an entry, and a stale "no decoder" can only be wrong
// the way a fresh one can be: by the skew the resolver counts.
type Decoders struct {
	cfg     DecoderConfig
	backend DecoderBackend
	sf      singleflight.Group

	mu      sync.Mutex
	entries map[string]decoderEntry
}

type decoderEntry struct {
	plan        DecoderPlan
	unsupported bool
	at          time.Time
}

// NewDecoders builds the cache over a backend.
func NewDecoders(backend DecoderBackend, cfg DecoderConfig) *Decoders {
	return &Decoders{cfg: cfg.withDefaults(), backend: backend, entries: make(map[string]decoderEntry)}
}

// Lookup returns the plan for a device type. An error is a failure to ask or to understand the
// answer, and must be answered 503, except ErrDecoderUnauthenticated, which the route answers as
// it answers a token that stopped matching.
func (d *Decoders) Lookup(ctx context.Context, tenant, integration, bearer, deviceTypeToken string) (DecoderLookup, error) {
	key := tenant + "\x00" + integration + "\x00" + deviceTypeToken
	stale, fresh, have := d.get(key, d.cfg.Now())
	if have && fresh {
		return DecoderLookup{Plan: stale.plan, Cached: true, Unsupported: stale.unsupported}, nil
	}
	// The flight is keyed on the bearer too: a flight runs with its first caller's token, and a
	// token that device-management rejects must not become the answer of waiters that hold a
	// good one.
	v, err, _ := d.sf.Do(key+sep+bearer, func() (any, error) {
		// A flight is shared by callers whose contexts differ: run it detached, as the device
		// lookup does.
		plan, err := d.backend.Decoder(context.WithoutCancel(ctx), tenant, integration, bearer, deviceTypeToken)
		switch {
		case errors.Is(err, ErrDecoderUnsupported):
			if d.cfg.OnUnsupported != nil {
				d.cfg.OnUnsupported()
			}
			e := decoderEntry{unsupported: true, at: d.cfg.Now()}
			d.put(key, e)
			return e, nil
		case err != nil:
			return nil, err
		case plan == nil:
			return nil, ErrDecoderUnauthenticated
		}
		e := decoderEntry{plan: *plan, at: d.cfg.Now()}
		d.put(key, e)
		return e, nil
	})
	if err != nil {
		if have && !errors.Is(err, ErrDecoderUnauthenticated) {
			return DecoderLookup{Plan: stale.plan, Cached: true, Stale: true, Unsupported: stale.unsupported}, nil
		}
		return DecoderLookup{}, err
	}
	e := v.(decoderEntry)
	return DecoderLookup{Plan: e.plan, Unsupported: e.unsupported}, nil
}

// get returns the entry for key, whether it is still fresh, and whether there is one to serve at
// all (fresh, or stale within StaleMax).
func (d *Decoders) get(key string, now time.Time) (e decoderEntry, fresh, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok = d.entries[key]
	if !ok {
		return decoderEntry{}, false, false
	}
	age := now.Sub(e.at)
	if age >= d.cfg.StaleMax {
		delete(d.entries, key)
		return decoderEntry{}, false, false
	}
	return e, age < d.cfg.TTL, true
}

func (d *Decoders) put(key string, e decoderEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.entries[key]; !ok && len(d.entries) >= d.cfg.Entries {
		evictSome(d.entries, d.cfg.Entries/16+1, func(old decoderEntry) bool { return e.at.Sub(old.at) >= d.cfg.StaleMax })
	}
	d.entries[key] = e
}

// Forget drops every remembered plan of a tenant, for a caller that has learned its credential no
// longer holds.
func (d *Decoders) Forget(tenant string) {
	prefix := tenant + "\x00"
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(d.entries, k)
		}
	}
}

// sep separates the parts of a flight key.
const sep = ""
