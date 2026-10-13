// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Device-information harvest: when a device becomes reachable, read the supported Object 3
// resources once and record them as the device's CLIENT-scope attributes (manufacturer, model
// number, serial number, firmware version, battery level), plus a record of the resources the
// device did not give.
//
// 🔴 IT MUST NEVER COST A REGISTRATION OR A MEASUREMENT ANYTHING, which is why it is built the way it
// is rather than as a few reads in the /rd handler:
//
//   - It is OFF the registration path. The handler only notes that the device became live; the
//     reads happen later, on the device's own shard worker (the one that serializes its commands),
//     and only once that device has no drain turn, live task or park waiting there (takeHarvest).
//     So a harvest never overlaps a command, never goes ahead of the device's held commands, never
//     runs on the CoAP read loop, and takes one slot of that worker per device.
//   - It is bounded. One GET per resource, the whole read bounded by ONE op timeout; the first
//     transport failure ends the harvest. One bounded retry later if the device did not answer.
//   - It belongs to the leadership term. Its queue is the term's shards, its retry checks the term,
//     and its attribute writes are bound by the term's ctx and joined by Run.
//   - It is throttled per device (harvestMinInterval): a device that re-handshakes every wake, or a
//     fleet re-registering after a failover, does not re-read or re-write what it already gave.
//   - Its attribute writes do not run on the worker. They are a device-management round trip each,
//     so they go to a small bounded pool, and a full pool drops the write (counted) rather than
//     queueing behind a slow device-management.
//   - Writes are skipped when the value is what was last written, so a stable device produces no
//     audit rows on re-registration.
const (
	// harvestMinInterval is the least time between two harvests of the same device.
	harvestMinInterval = time.Hour
	// harvestRetryDelay is the wait before the single retry of a harvest the device did not answer.
	harvestRetryDelay = 30 * time.Second
	// harvestMaxPending bounds the harvests queued on one shard; a full queue drops the request
	// (the next registration asks again).
	harvestMaxPending = 256
	// harvestWriters bounds concurrent attribute-writing goroutines across the whole service.
	harvestWriters = 4
	// harvestCacheMax bounds the per-device bookkeeping maps.
	harvestCacheMax = 50000
	// writeTimeout bounds one attribute-write round trip.
	harvestWriteTimeout = 10 * time.Second

	// missingAttrKey records the resources the device did not give, as a JSON array. It is written
	// even when empty so a resource that has since appeared clears its old record.
	missingAttrKey = "lwm2m.deviceInfoMissing"
)

// HarvestMetrics are the optional instruments; any nil field is skipped. Outcome is the only label
// and a bounded set (complete / partial / empty / unanswered).
type HarvestMetrics struct {
	Runs        *prometheus.CounterVec // harvests that ran, by outcome
	Retries     prometheus.Counter     // harvests re-queued after the device did not answer
	Skipped     prometheus.Counter     // requests not run: throttled, queue full, or the device was gone
	WriteErrors prometheus.Counter     // attribute writes that failed (retried at the next harvest)
	WriteShed   prometheus.Counter     // attribute batches dropped because the write pool was busy
}

// harvestJob is one queued harvest for one device.
type harvestJob struct {
	tenant      string
	deviceToken string
	attempt     int
}

// Harvester owns the harvest's bookkeeping and its attribute writes. The reads themselves are run
// by the Dispatcher's shard workers (see Dispatcher.Harvest).
type Harvester struct {
	client  commandQuerier
	baseURL string
	metrics HarvestMetrics
	now     func() time.Time

	mu      sync.Mutex
	last    map[deviceKey]time.Time
	written map[deviceKey]map[string]string
	sem     chan struct{}
	// inflight counts running attribute-write goroutines so tests can wait for them.
	inflight atomic.Int32
	// writers joins the attribute-write goroutines at the end of the term (Dispatcher.Run), so a
	// replica that has lost leadership is not still writing a device's attributes when it returns.
	writers sync.WaitGroup
}

// NewHarvester builds a harvester that writes attributes through the device-management GraphQL
// client + URL (the registrar's pair; the service token already carries device:write).
func NewHarvester(client commandQuerier, baseURL string, metrics HarvestMetrics) *Harvester {
	return &Harvester{
		client:  client,
		baseURL: baseURL,
		metrics: metrics,
		now:     time.Now,
		last:    map[deviceKey]time.Time{},
		written: map[deviceKey]map[string]string{},
		sem:     make(chan struct{}, harvestWriters),
	}
}

// due reports whether the device is past its throttle, and if so reserves it.
func (h *Harvester) due(k deviceKey) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if t, ok := h.last[k]; ok && now.Sub(t) < harvestMinInterval {
		return false
	}
	if len(h.last) >= harvestCacheMax {
		for kk, t := range h.last {
			if now.Sub(t) >= harvestMinInterval {
				delete(h.last, kk)
				delete(h.written, kk)
			}
		}
		if len(h.last) >= harvestCacheMax { // still full of recent entries: shed rather than grow
			return false
		}
	}
	h.last[k] = now
	return true
}

// forget releases a reservation whose harvest got nothing, so the next registration tries again.
func (h *Harvester) forget(k deviceKey) {
	h.mu.Lock()
	delete(h.last, k)
	h.mu.Unlock()
}

// attrWrite is one attribute to upsert.
type attrWrite struct{ key, valueType, value string }

// plan turns a read into the attributes to write, dropping those equal to what was last written.
func (h *Harvester) plan(k deviceKey, info DeviceInfo) []attrWrite {
	var all []attrWrite
	for _, res := range deviceInfoResources {
		if v, ok := info.Values[res.Key]; ok {
			all = append(all, attrWrite{res.Key, infoValueType(res.Key), v})
		}
	}
	missing := info.Missing
	if missing == nil {
		missing = []InfoMissing{}
	}
	mb, _ := json.Marshal(missing) // a slice of plain structs cannot fail to marshal
	all = append(all, attrWrite{missingAttrKey, "JSON", string(mb)})

	h.mu.Lock()
	defer h.mu.Unlock()
	prev := h.written[k]
	var out []attrWrite
	for _, a := range all {
		if v, has := prev[a.key]; has && v == a.value {
			continue
		}
		out = append(out, a)
	}
	return out
}

// remember records a successful write.
func (h *Harvester) remember(k deviceKey, a attrWrite) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.written[k]
	if m == nil {
		m = map[string]string{}
		h.written[k] = m
	}
	m[a.key] = a.value
}

const setAttributeMutation = `mutation($request: EntityAttributeSetRequest!) {
  setEntityAttribute(request: $request) { id }
}`

// publish writes the harvest's attributes off the caller's goroutine. It never blocks: a busy pool
// drops the batch (the next harvest re-plans it, since nothing was remembered).
//
// The writes are bound by ctx, the leadership term's: when the term ends they stop at the next
// write rather than running on a replica that no longer serves the device, and Run joins them
// (wait). tenantGone is re-checked before every write, because the device read that produced them
// took up to an op timeout and the tenant may have been deleted meanwhile.
func (h *Harvester) publish(ctx context.Context, k deviceKey, info DeviceInfo, tenantGone func(string) bool) {
	writes := h.plan(k, info)
	if len(writes) == 0 {
		return
	}
	select {
	case h.sem <- struct{}{}:
	default:
		if h.metrics.WriteShed != nil {
			h.metrics.WriteShed.Inc()
		}
		return
	}
	h.inflight.Add(1)
	h.writers.Add(1)
	go func() {
		defer h.writers.Done()
		defer h.inflight.Add(-1)
		defer func() { <-h.sem }()
		for _, a := range writes {
			if ctx.Err() != nil || (tenantGone != nil && tenantGone(k.tenant)) {
				return // nothing remembered: a later harvest re-plans what was not written
			}
			wctx, cancel := context.WithTimeout(ctx, harvestWriteTimeout)
			err := h.client.Query(wctx, h.baseURL, k.tenant, setAttributeMutation, map[string]any{
				"request": map[string]any{
					"entityType": "device",
					"entity":     k.deviceToken,
					"scope":      "CLIENT",
					"attrKey":    a.key,
					"valueType":  a.valueType,
					"value":      a.value,
				},
			}, &struct{}{})
			cancel()
			if err != nil {
				if h.metrics.WriteErrors != nil {
					h.metrics.WriteErrors.Inc()
				}
				log.Debug().Err(err).Str("tenant", k.tenant).Str("attr", a.key).
					Msg("LwM2M device-information attribute write failed; the next harvest retries it.")
				continue
			}
			h.remember(k, a)
		}
	}()
}

// wait joins every attribute-write goroutine. Run calls it after its workers have exited, so no
// publish can race it.
func (h *Harvester) wait() { h.writers.Wait() }

// WaitForWrites blocks until in-flight attribute writes finish. Tests use it; nothing in the service does.
func (h *Harvester) WaitForWrites() {
	for deadline := time.Now().Add(5 * time.Second); h.inflight.Load() > 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
}

// outcome labels.
func harvestOutcome(info DeviceInfo) string {
	switch {
	case info.Aborted && len(info.Values) == 0:
		return "unanswered"
	case len(info.Values) == 0:
		return "empty"
	case len(info.Missing) > 0:
		return "partial"
	default:
		return "complete"
	}
}

func (h *Harvester) count(outcome string) {
	if h.metrics.Runs != nil {
		h.metrics.Runs.WithLabelValues(outcome).Inc()
	}
}

func (h *Harvester) skip() {
	if h.metrics.Skipped != nil {
		h.metrics.Skipped.Inc()
	}
}

// ── Dispatcher side ───────────────────────────────────────────────────────

// OnLive is the conn table's live hook (ConnTable.SetOnLive): a device that became reachable on a
// fresh conn is gated and woken for a drain of its held commands FIRST, and only then queued for a
// harvest. The order matters: Drain marks the device as wanting a drain turn, which is what keeps
// takeHarvest from running its harvest ahead of its commands. One method, so main.go and the
// wiring test share the order rather than each restating it.
func (d *Dispatcher) OnLive(tenant, deviceToken string) {
	d.Drain(tenant, deviceToken)
	d.Harvest(tenant, deviceToken)
}

// Harvest asks for a device-information harvest of a device that has just become reachable. It
// never blocks and never does I/O: it checks the throttle and queues the request on the device's
// shard, whose worker runs it. A no-op without a harvester, and on a standby (no active term).
func (d *Dispatcher) Harvest(tenant, deviceToken string) {
	if d.harvester == nil {
		return
	}
	s := d.shardFor(deviceToken)
	if s == nil {
		return
	}
	key := deviceKey{tenant, deviceToken}
	if !d.harvester.due(key) {
		d.harvester.skip()
		return
	}
	if !s.addHarvest(harvestJob{tenant: tenant, deviceToken: deviceToken}) {
		d.harvester.forget(key)
		d.harvester.skip()
	}
}

// addHarvest queues a job unless the shard is at its bound or the device already has one pending.
func (s *shardState) addHarvest(j harvestJob) bool {
	s.mu.Lock()
	for _, p := range s.harvests {
		if p.tenant == j.tenant && p.deviceToken == j.deviceToken {
			s.mu.Unlock()
			return true
		}
	}
	if len(s.harvests) >= harvestMaxPending {
		s.mu.Unlock()
		return false
	}
	s.harvests = append(s.harvests, j)
	s.mu.Unlock()
	s.poke()
	return true
}

// takeHarvest pops the oldest queued job whose device has no command work waiting on this shard.
//
// 🔴 COMMANDS FIRST, PER DEVICE. Drain wakes the device before Harvest queues it, but the worker
// serves the ready list and the harvest queue one entry each per iteration, so with other devices
// ahead in the ready list a device's harvest could otherwise run BEFORE its own drain turn — and a
// sleeping device's held commands would wait behind up to an op timeout of information reads, in
// the short window it is awake. A device that still wants a drain turn, or has a live task queued
// or a park in flight, is skipped; the worker iteration that serves that work runs runOneHarvest
// right after it, so the skipped job is reconsidered then.
func (s *shardState) takeHarvest() (harvestJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.harvests {
		if g := s.devs[deviceKey{j.tenant, j.deviceToken}]; g != nil && (g.wanted || g.queuedLive > 0 || g.parksInFlight > 0) {
			continue
		}
		s.harvests = slices.Delete(s.harvests, i, i+1)
		if len(s.harvests) > 0 {
			defer s.poke()
		}
		return j, true
	}
	return harvestJob{}, false
}

// runOneHarvest runs at most one queued harvest on the shard's worker, after the worker's live task
// and drain turn. It reads on the device's CURRENT conn (looked up now, not when it was queued), so
// a device that dropped in between is simply skipped.
func (d *Dispatcher) runOneHarvest(ctx context.Context, s *shardState) {
	if d.harvester == nil || ctx.Err() != nil {
		return
	}
	j, ok := s.takeHarvest()
	if !ok {
		return
	}
	key := deviceKey{j.tenant, j.deviceToken}
	if d.tenantDeleted(j.tenant) {
		d.harvester.forget(key)
		d.harvester.skip()
		return
	}
	conn, reach := d.conns.Lookup(j.tenant, j.deviceToken)
	if reach != ReachLive {
		d.harvester.forget(key)
		d.harvester.skip()
		return
	}
	// The WHOLE read is bounded by one op timeout — the same bound lwm2m.readDeviceInfo gets as a
	// command — not one per resource: a device that answers each GET just inside the timeout would
	// otherwise hold this shard's worker, and every other device's commands on it, for five.
	hctx, cancel := context.WithTimeout(ctx, d.opTimeout)
	info := ReadDeviceInfo(hctx, conn, d.opTimeout)
	cancel()
	if ctx.Err() != nil {
		return
	}
	outcome := harvestOutcome(info)
	d.harvester.count(outcome)
	if outcome == "unanswered" && j.attempt == 0 {
		if d.harvester.metrics.Retries != nil {
			d.harvester.metrics.Retries.Inc()
		}
		retry := j
		retry.attempt = 1
		d.clock.AfterFunc(harvestRetryDelay, func() {
			if ctx.Err() == nil {
				s.addHarvest(retry)
			}
		})
		return
	}
	if outcome == "unanswered" {
		d.harvester.forget(key) // let the next registration try again rather than waiting an hour
	}
	if len(info.Missing) > 0 {
		log.Debug().Str("tenant", j.tenant).Str("outcome", outcome).
			Str("missing", fmt.Sprint(summarizeMissing(info.Missing))).
			Msg("LwM2M device information harvested with resources missing.")
	}
	d.harvester.publish(ctx, key, info, d.tenantDeleted)
}
