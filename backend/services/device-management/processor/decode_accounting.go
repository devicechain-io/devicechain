// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// THE ACCOUNTING OF A PAYLOAD DECODER.
//
// The resolver tells a DecodeAccountant the outcome of every decode attempt. The accountant:
//
//   - answers whether a FAILURE may be recorded (the per-(tenant, device type) record budget, see
//     decode_budget.go). A refusal makes the uplink "suppressed": acked without a failed-events
//     record, and counted;
//   - tallies every uplink into exactly one of decoded, ignored, failed (recorded) or suppressed
//     (not recorded), per (tenant, profile version, minute), in memory;
//   - queues a sample of a recorded failure, within the per-(tenant, profile) sample budget;
//   - and, from Flush, writes those tallies and samples to the service's own schema.
//
// 🔴 WHAT IS DURABLE AND WHAT IS NOT. A recorded failure is a failed-events record written before
// the source is acked, and that is the exact count. The tallies and samples are not: a tally lives
// in memory until the next flush (about a minute), so a crash loses up to that much of the COUNTS,
// and a sample is best-effort. A suppressed failure happens once 20 failures for its device type
// have been ADMITTED in the window. Normally their records are out, but a failed-events outage may
// leave admitted records unwritten: a redelivery can end suppressed with no record, still counted
// in the suppressed tally. After a crash (or a failed-events outage) a redelivered source can be
// counted twice, so counts are "at least". The resolver tells the accountant once per delivery,
// not once per store-hold re-run.
//
// 🔴 THE FLUSH IS IDEMPOTENT. Each row is this replica's CUMULATIVE tally for the window and is
// written with SET, so a write whose commit succeeded but whose reply was lost, written again on
// the next pass, lands on the same value. A failed write keeps the tally dirty and is retried with
// whatever it has grown to.
const (
	// DecodeHealthWindow is the width of a health row.
	DecodeHealthWindow = time.Minute
	// decodeSamplesQueueMax bounds the samples awaiting a write. The sample budget already bounds
	// the rate per profile; this bounds the backlog when the database is away.
	decodeSamplesQueueMax = 64
	// decodeTallyMax bounds the tallies held in memory. Past it, closed windows go first, counted
	// on the lost metric; an open window is never dropped.
	decodeTallyMax = 20_000
	// maxTallyError is the bound of a stored last-error text (the column's size).
	maxTallyError = 512
)

// DecodeAccountingStore is the service's own schema as the accountant writes it. *model.Api is one.
type DecodeAccountingStore interface {
	WriteDecoderHealth(ctx context.Context, rows []model.DecoderHealthWrite) (written, skipped int, err error)
	// TrimDecoderHealth deletes every tenant's expired health rows.
	TrimDecoderHealth(ctx context.Context, before time.Time) error
	StoreDecoderSample(ctx context.Context, s model.DecoderSampleWrite, now time.Time) (bool, error)
}

type tallyKey struct {
	tenant  string
	profile string
	version int32
	window  time.Time
}

type tally struct {
	decoded, ignored, failed, suppressed, skew int64
	lastFailureAt                              time.Time
	lastError                                  string
	// gen counts changes; a flush clears dirty only if gen is what it wrote.
	gen   uint64
	dirty bool
}

type queuedSample struct {
	tenant string
	write  model.DecoderSampleWrite
	at     time.Time
}

// AccountingMetrics counts what the accounting itself did. Label-free of tenant on purpose.
type AccountingMetrics struct {
	suppressed     prometheus.Counter
	samplesDropped prometheus.Counter
	flushFailures  prometheus.Counter
	tallyLost      prometheus.Counter
	unattributed   prometheus.Counter
}

// NewAccountingMetrics registers the accounting counters on the service.
func NewAccountingMetrics(ms *core.Microservice) *AccountingMetrics {
	return &AccountingMetrics{
		suppressed: ms.NewCounter("resolve_decode_suppressed_total",
			"Decode failures NOT recorded in failed-events because the device type's record budget "+
				"(a burst of 20, refilled 10 a minute, per replica) was spent. They are acked and counted here "+
				"and in the profile's decoder health; none is silent."),
		samplesDropped: ms.NewCounter("resolve_decode_samples_dropped_total",
			"Failing uplinks that were not kept as a sample because the write queue was full or the write failed."),
		flushFailures: ms.NewCounter("resolve_decode_health_flush_failures_total",
			"Decoder health or sample writes that failed. A health tally stays in memory and the next pass retries it."),
		tallyLost: ms.NewCounter("resolve_decode_health_lost_total",
			"Closed decoder-health windows dropped from memory unwritten, because the database stayed away "+
				"until the in-memory bound was reached."),
		unattributed: ms.NewCounter("resolve_decode_unattributed_total",
			"Decode outcomes with no profile version to count them against; they are in resolve_decode_total only."),
	}
}

// DecodeAccountant is the DecodeObserver of the service. See the comment above.
type DecodeAccountant struct {
	store   DecodeAccountingStore
	replica string
	budget  *decodeBudget
	now     func() time.Time
	metrics *AccountingMetrics // nil counts nothing

	mu      sync.Mutex
	tallies map[tallyKey]*tally
	samples []queuedSample
	trimmed time.Time
}

// NewDecodeAccountant builds the accountant for one replica. replica names the pod in the rows it
// writes; two replicas never write the same row.
func NewDecodeAccountant(store DecodeAccountingStore, replica string, m *AccountingMetrics) *DecodeAccountant {
	return newDecodeAccountant(store, replica, m, time.Now)
}

func newDecodeAccountant(store DecodeAccountingStore, replica string, m *AccountingMetrics, now func() time.Time) *DecodeAccountant {
	return &DecodeAccountant{store: store, replica: replica, budget: newDecodeBudget(now), now: now, metrics: m,
		tallies: map[tallyKey]*tally{}, trimmed: now()}
}

// splitProfileVersion splits the resolution's "<profile token>@<version>".
func splitProfileVersion(pv string) (string, int32, bool) {
	i := strings.LastIndexByte(pv, '@')
	if i <= 0 {
		return "", 0, false
	}
	v, err := strconv.ParseInt(pv[i+1:], 10, 32)
	if err != nil {
		return "", 0, false
	}
	return pv[:i], int32(v), true
}

// boundText cuts s to at most n bytes without splitting a rune.
func boundText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// ObserveDecode implements DecodeObserver. It returns false only for a failed decode whose record
// budget is spent: the one verdict the resolver acts on.
func (a *DecodeAccountant) ObserveDecode(o DecodeObservation) bool {
	record := true
	// 🔴 A REDELIVERED FAILURE IS HELD TO THE BUDGET LIKE A FIRST ONE.
	//
	// A redelivery does not prove the failure was ever admitted: a first delivery that fails before
	// the decode (a transient lookup error, a store hold released at shutdown) is redelivered with
	// no token ever taken. Exempting redeliveries would let a restart with a backlog of a failing
	// tenant's uplinks record every one of them, which is the flood the budget exists to stop. The
	// cost is that a failure whose first record never landed may be suppressed once the bucket is
	// spent; it is tallied as suppressed, which is what the budget means.
	if o.Outcome == DecodeFailed {
		record = a.budget.AllowRecord(o.Tenant, o.DeviceTypeId)
		if !record && a.metrics != nil {
			a.metrics.suppressed.Inc()
		}
	}
	profile, version, ok := splitProfileVersion(o.ProfileVersionToken)
	if !ok {
		if a.metrics != nil {
			a.metrics.unattributed.Inc()
		}
		return record
	}
	now := a.now()
	k := tallyKey{o.Tenant, profile, version, now.UTC().Truncate(DecodeHealthWindow)}
	a.mu.Lock()
	t := a.tallies[k]
	if t == nil {
		t = &tally{}
		a.tallies[k] = t
	}
	switch o.Outcome {
	case DecodeDecoded:
		t.decoded++
		t.skew += int64(o.ChargeSkew)
	case DecodeIgnored:
		t.ignored++
	default:
		if record {
			t.failed++
		} else {
			t.suppressed++
		}
		t.lastFailureAt, t.lastError = now.UTC(), boundText(o.Error, maxTallyError)
	}
	t.gen++
	t.dirty = true
	a.mu.Unlock()

	if o.Outcome == DecodeFailed && record && o.Uplink != nil && a.budget.AllowSample(o.Tenant, profile) {
		a.queueSample(o, profile, version, now)
	}
	return record
}

func (a *DecodeAccountant) queueSample(o DecodeObservation, profile string, version int32, now time.Time) {
	raw, err := marshalSample(o.Uplink)
	if err != nil {
		a.sampleLost()
		return
	}
	s := queuedSample{tenant: o.Tenant, at: now, write: model.DecoderSampleWrite{
		ProfileToken: profile, Version: version, DeviceToken: o.Device, Reason: string(o.Reason),
		Error: boundText(o.Error, maxTallyError), Input: raw}}
	a.mu.Lock()
	if len(a.samples) >= decodeSamplesQueueMax {
		a.mu.Unlock()
		a.sampleLost()
		return
	}
	a.samples = append(a.samples, s)
	a.mu.Unlock()
}

// vectorOf is THE mapping from a carried uplink to the form a decoder vector and a stored sample
// share (payloaddecode.VectorInput). A stored sample is replayed through testPayloadDecoder by
// turning this back into an input (model inputFromVector), so the two must agree; the round-trip
// test replays a stored sample and expects the live outcome.
//
// It refuses a carried json or decoded object that is not valid JSON. The carrier holds the canonical
// JSON event-sources produced, so this should not happen; if it does, a sample is not stored (and is
// counted on resolve_decode_samples_dropped_total) rather than stored in a form that would replay as
// a different failure. The failure itself is still recorded.
func vectorOf(up *esmodel.UnresolvedIntegrationUplinkPayload) (payloaddecode.VectorInput, error) {
	in := payloaddecode.VectorInput{Link: up.Link}
	if len(up.Json) > 0 {
		if !json.Valid(up.Json) {
			return in, errors.New("the carried json is not valid JSON")
		}
		in.JSON = up.Json
	}
	if len(up.Decoded) > 0 {
		if !json.Valid(up.Decoded) {
			return in, errors.New("the carried decoded object is not valid JSON")
		}
		in.Decoded = up.Decoded
	}
	if up.Frame != nil {
		h := hex.EncodeToString(up.Frame)
		in.FrameHex = &h
	}
	if up.FPort != nil {
		v := int(*up.FPort)
		in.FPort = &v
	}
	if up.FCnt != nil {
		v := int(*up.FCnt)
		in.FCnt = &v
	}
	return in, nil
}

// marshalSample is the stored form of a failing uplink, bounded by model.MaxDecoderSampleBytes.
func marshalSample(up *esmodel.UnresolvedIntegrationUplinkPayload) ([]byte, error) {
	in, err := vectorOf(up)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(raw) > model.MaxDecoderSampleBytes {
		return nil, errors.New("the sample is too large")
	}
	return raw, nil
}

func (a *DecodeAccountant) sampleLost() {
	if a.metrics != nil {
		a.metrics.samplesDropped.Inc()
	}
}

// Flush writes the pending samples and every dirty tally, and once an hour trims health rows
// past their retention. A failure is counted and logged; a tally that
// did not land stays dirty and is written again by the next call, a sample is dropped and counted
// (samples are best-effort). It returns the first error so a caller can log it.
func (a *DecodeAccountant) Flush(ctx context.Context) error {
	var first error
	note := func(err error) {
		if err != nil {
			if first == nil {
				first = err
			}
			if a.metrics != nil {
				a.metrics.flushFailures.Inc()
			}
		}
	}

	type pending struct {
		key tallyKey
		row model.DecoderHealthWrite
		gen uint64
	}
	a.mu.Lock()
	samples := a.samples
	a.samples = nil
	var dirty []pending
	cur := a.now().UTC().Truncate(DecodeHealthWindow)
	for k, t := range a.tallies {
		if !t.dirty {
			if k.window.Before(cur) {
				delete(a.tallies, k) // closed and already written
			}
			continue
		}
		w := model.DecoderHealthWrite{ProfileToken: k.profile, Version: k.version, WindowStart: k.window,
			Replica: a.replica, Decoded: t.decoded, Ignored: t.ignored, Failed: t.failed,
			Suppressed: t.suppressed, ChargeSkew: t.skew, LastError: t.lastError}
		if !t.lastFailureAt.IsZero() {
			ts := t.lastFailureAt
			w.LastFailureAt = &ts
		}
		dirty = append(dirty, pending{k, w, t.gen})
	}
	a.mu.Unlock()

	for _, s := range samples {
		_, err := a.store.StoreDecoderSample(core.WithTenant(ctx, s.tenant), s.write, s.at)
		if err != nil {
			log.Warn().Err(err).Msg("Could not store a decoder failure sample; dropped.")
			a.sampleLost()
			note(err)
		}
	}

	byTenant := map[string][]pending{}
	for _, p := range dirty {
		byTenant[p.key.tenant] = append(byTenant[p.key.tenant], p)
	}
	for tenant, ps := range byTenant {
		rows := make([]model.DecoderHealthWrite, len(ps))
		for i, p := range ps {
			rows[i] = p.row
		}
		_, _, err := a.store.WriteDecoderHealth(core.WithTenant(ctx, tenant), rows)
		if err != nil {
			log.Warn().Err(err).Str("tenant", tenant).Msg("Could not write decoder health; it stays in memory and is retried.")
			note(err)
			continue
		}
		a.mu.Lock()
		for _, p := range ps {
			t := a.tallies[p.key]
			if t == nil || t.gen != p.gen {
				continue // changed while it was written: still dirty, written again next pass
			}
			t.dirty = false
			if p.key.window.Before(cur) {
				delete(a.tallies, p.key)
			}
		}
		a.mu.Unlock()
	}

	a.boundTallies(cur)
	note(a.trim(ctx))
	return first
}

// boundTallies drops closed windows past decodeTallyMax.
func (a *DecodeAccountant) boundTallies(cur time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, t := range a.tallies {
		if len(a.tallies) <= decodeTallyMax {
			return
		}
		if k.window.Before(cur) {
			delete(a.tallies, k)
			if t.dirty && a.metrics != nil {
				a.metrics.tallyLost.Inc()
			}
		}
	}
}

// trim deletes every tenant's health rows past their retention, at most once an hour. Which tenants
// have such rows is the store's to find (see Api.TrimDecoderHealth), not this process's traffic.
func (a *DecodeAccountant) trim(ctx context.Context) error {
	now := a.now()
	a.mu.Lock()
	if now.Sub(a.trimmed) < time.Hour {
		a.mu.Unlock()
		return nil
	}
	a.trimmed = now
	a.mu.Unlock()
	if err := a.store.TrimDecoderHealth(ctx, now.Add(-model.DecoderHealthRetention)); err != nil {
		log.Warn().Err(err).Msg("Could not trim decoder health past its retention.")
		a.mu.Lock()
		a.trimmed = time.Time{} // due again on the next pass
		a.mu.Unlock()
		return err
	}
	return nil
}
