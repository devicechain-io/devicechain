// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Default bounds.
const (
	DefaultControlQueue     = 256
	DefaultLogLines         = 2000
	DefaultLogBytes         = 1 << 20
	DefaultProgressInterval = 100 * time.Millisecond // at most 10 flushes a second per metric
	DefaultControlGrace     = 5 * time.Second
)

// Sink is a presenter's side of the hub. Deliver is called from one goroutine,
// in seq order, and may be slow: the hub buffers within its bounds so the
// engine is not held up.
type Sink interface{ Deliver(Envelope) }

// SinkFunc adapts a function to Sink.
type SinkFunc func(Envelope)

func (f SinkFunc) Deliver(e Envelope) { f(e) }

// Options configures a Hub. Zero values take the defaults.
type Options struct {
	Operation Operation
	// RunID is random when empty; tests fix it.
	RunID string
	Clock Clock
	Sink  Sink
	// ControlQueue bounds undelivered control events.
	ControlQueue int
	// LogLines and LogBytes bound both the log ring and undelivered log lines.
	LogLines int
	LogBytes int
	// ProgressInterval is the minimum gap between two flushes of one metric.
	ProgressInterval time.Duration
	// ControlGrace is how long a control event may wait for queue space
	// before delivery is declared stalled. It is measured in real time.
	ControlGrace time.Duration
	// OnDeliveryFailure is called once, outside any hub lock, when delivery
	// stalls. The caller wires it to a graceful cancel of the run.
	OnDeliveryFailure func()
}

// Stats are the hub's delivery counters.
type Stats struct {
	Delivered          uint64 `json:"delivered"`
	ControlPending     int    `json:"control_pending"`
	LogsPending        int    `json:"logs_pending"`
	ProgressPending    int    `json:"progress_pending"`
	LogsEvicted        uint64 `json:"logs_evicted"`     // aged out of the ring
	LogsUndelivered    uint64 `json:"logs_undelivered"` // dropped before a slow presenter took them
	ControlUndelivered uint64 `json:"control_undelivered"`
	DeliveryFailed     bool   `json:"delivery_failed"`
}

type entry struct {
	order   uint64
	kind    Kind
	step    StepID
	parent  StepID
	attempt int
	at      time.Time
	payload Payload
	size    int // log text bytes
}

type progressKey struct {
	step    StepID
	attempt int
	metric  string
}

type heldProgress struct {
	e     *entry
	timer Timer
}

// StepStatus is a step's state in the projection.
type StepStatus string

const (
	StatusPending  StepStatus = "pending"
	StatusActive   StepStatus = "active"
	StatusFinished StepStatus = "finished"
	StatusFailed   StepStatus = "failed"
)

type stepState struct {
	manifest ManifestStep
	attempt  int
	status   StepStatus
	started  time.Time
	outcome  StepOutcome
	failure  *StepFailed
	progress map[string]StepProgress
}

// Hub is the single serialisation point between an engine and a presenter.
type Hub struct {
	op    Operation
	runID string
	clock Clock
	sink  Sink
	opts  Options
	red   *redactor
	start time.Time

	failOnce sync.Once

	mu       sync.Mutex
	order    uint64
	seq      uint64
	control  []*entry
	logs     []*entry
	logBytes int
	queued   map[progressKey]*entry
	held     map[progressKey]*heldProgress
	lastSent map[progressKey]time.Time
	wake     chan struct{}
	space    chan struct{}

	started    bool
	dryRun     bool
	target     Target
	steps      map[StepID]*stepState
	stepOrder  []StepID
	active     []StepID // start order
	prompts    map[PromptID]bool
	cancelling *RunCancelling
	finished   bool
	final      Summary

	ring  logRing
	stats Stats
	done  chan struct{}

	// inflight is the order of the entry being handed to the sink, 0 when none;
	// delivered is closed (and replaced) after each hand-off. Flush waits on them.
	inflight  uint64
	delivered chan struct{}
}

// NewHub starts a hub delivering to opts.Sink.
func NewHub(opts Options) (*Hub, error) {
	if opts.Sink == nil {
		return nil, errors.New("installflow: a hub needs a sink")
	}
	if opts.Operation == "" {
		opts.Operation = OperationInstall
	}
	if !oneOf(opts.Operation, OperationInstall, OperationBootstrap, OperationUpgrade, OperationDestroy) {
		return nil, fmt.Errorf("%w: unknown operation", ErrInvalidEvent)
	}
	if opts.RunID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, fmt.Errorf("installflow: run id: %w", err)
		}
		opts.RunID = hex.EncodeToString(b[:])
	}
	if opts.Clock == nil {
		opts.Clock = SystemClock{}
	}
	if opts.ControlQueue <= 0 {
		opts.ControlQueue = DefaultControlQueue
	}
	if opts.LogLines <= 0 {
		opts.LogLines = DefaultLogLines
	}
	if opts.LogBytes <= 0 {
		opts.LogBytes = DefaultLogBytes
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = DefaultProgressInterval
	}
	if opts.ControlGrace <= 0 {
		opts.ControlGrace = DefaultControlGrace
	}
	h := &Hub{
		op: opts.Operation, runID: opts.RunID, clock: opts.Clock, sink: opts.Sink, opts: opts,
		red: newRedactor(), start: opts.Clock.Now(),
		queued: map[progressKey]*entry{}, held: map[progressKey]*heldProgress{},
		lastSent: map[progressKey]time.Time{},
		wake:     make(chan struct{}, 1), space: make(chan struct{}),
		steps: map[StepID]*stepState{}, prompts: map[PromptID]bool{},
		ring: logRing{maxLines: opts.LogLines, maxBytes: opts.LogBytes},
		done: make(chan struct{}), delivered: make(chan struct{}),
	}
	go h.pump()
	return h, nil
}

// RunID is this run's identifier.
func (h *Hub) RunID() string { return h.runID }

// Done is closed once the run.summary has been handed to the sink.
func (h *Hub) Done() <-chan struct{} { return h.done }

// AddSecret registers value for redaction from every text the hub carries
// from now on.
func (h *Hub) AddSecret(value string) error {
	if !h.red.add(value) {
		return ErrSecretTooShort
	}
	return nil
}

// Final returns the summary that was sent, once there is one.
func (h *Hub) Final() (Summary, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.final, h.finished
}

// Stats returns the delivery counters.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statsLocked()
}

func (h *Hub) statsLocked() Stats {
	s := h.stats
	s.ControlPending = len(h.control)
	s.LogsPending = len(h.logs)
	s.ProgressPending = len(h.queued)
	s.LogsEvicted = h.ring.evicted
	return s
}

// Emit validates, scrubs and queues one event.
func (h *Hub) Emit(ev Event) error {
	if ev.Payload == nil {
		return fmt.Errorf("%w: no payload", ErrInvalidEvent)
	}
	kind := ev.Payload.Kind()
	if kind == KindRunSummary {
		return fmt.Errorf("%w: a summary is sent with Finish", ErrLifecycle)
	}
	if err := ev.Payload.validate(h.red); err != nil {
		return err
	}
	if err := checkOptionalID(h.red, "step id", string(ev.Step)); err != nil {
		return err
	}
	if err := checkOptionalID(h.red, "parent id", string(ev.Parent)); err != nil {
		return err
	}
	p := ev.Payload.scrub(h.red)

	h.mu.Lock()
	if kind.isControl() {
		if err := h.awaitControlSpaceLocked(); err != nil {
			h.mu.Unlock()
			if errors.Is(err, ErrDeliveryStalled) {
				h.deliveryFailed()
			}
			return err
		}
	}
	err := h.emitLocked(ev, kind, p)
	h.mu.Unlock()
	return err
}

// awaitControlSpaceLocked waits, releasing the lock, until a control slot is
// free, the run finishes, or the grace period runs out.
func (h *Hub) awaitControlSpaceLocked() error {
	if len(h.control) < h.opts.ControlQueue {
		return nil
	}
	if h.stats.DeliveryFailed {
		h.stats.ControlUndelivered++
		return ErrDeliveryStalled
	}
	timer := time.NewTimer(h.opts.ControlGrace)
	defer timer.Stop()
	for len(h.control) >= h.opts.ControlQueue {
		if h.finished {
			return ErrFinished
		}
		space := h.space
		h.mu.Unlock()
		select {
		case <-space:
			h.mu.Lock()
		case <-timer.C:
			h.mu.Lock()
			if len(h.control) < h.opts.ControlQueue {
				return nil
			}
			h.stats.DeliveryFailed = true
			h.stats.ControlUndelivered++
			return ErrDeliveryStalled
		}
	}
	return nil
}

func (h *Hub) deliveryFailed() {
	if f := h.opts.OnDeliveryFailure; f != nil {
		h.failOnce.Do(f)
	}
}

func (h *Hub) emitLocked(ev Event, kind Kind, p Payload) error {
	if h.finished {
		return ErrFinished
	}
	if !h.started && kind != KindRunStarted {
		return ErrNotStarted
	}
	now := h.clock.Now()
	switch pl := p.(type) {
	case RunStarted:
		if h.started {
			return fmt.Errorf("%w: run already started", ErrLifecycle)
		}
		h.started, h.dryRun, h.target = true, pl.DryRun, pl.Target
		for _, m := range pl.Manifest {
			h.steps[m.ID] = &stepState{manifest: m, status: StatusPending}
			h.stepOrder = append(h.stepOrder, m.ID)
		}
	case StepStarted:
		if err := h.startStepLocked(ev, now); err != nil {
			return err
		}
	case StepProgress:
		return h.progressLocked(ev, pl, now)
	case StepFinished:
		if err := h.checkActiveLocked(ev); err != nil {
			return err
		}
		if pl.Outcome == StepSucceeded && h.dryRun {
			return fmt.Errorf("%w: a dry run never reports a step succeeded", ErrLifecycle)
		}
		if pl.Outcome == StepPlanned && !h.dryRun {
			return fmt.Errorf("%w: only a dry run reports a step planned", ErrLifecycle)
		}
		if err := h.checkNoActiveChildrenLocked(ev.Step); err != nil {
			return err
		}
		st := h.steps[ev.Step]
		pl.DurationMS = now.Sub(st.started).Milliseconds()
		p = pl
		h.closeStepLocked(ev.Step, StatusFinished, pl.Outcome, nil)
	case StepFailed:
		if err := h.checkActiveLocked(ev); err != nil {
			return err
		}
		if err := h.checkNoActiveChildrenLocked(ev.Step); err != nil {
			return err
		}
		st := h.steps[ev.Step]
		pl.DurationMS = now.Sub(st.started).Milliseconds()
		p = pl
		f := pl
		h.closeStepLocked(ev.Step, StatusFailed, "", &f)
	case LogLine:
		h.logLocked(ev.Step, pl, now)
		return nil
	case PreflightObservation:
		if err := h.checkActiveLocked(ev); err != nil {
			return err
		}
	case PromptRequested:
		if _, seen := h.prompts[pl.ID]; seen {
			return fmt.Errorf("%w: prompt id reused", ErrLifecycle)
		}
		h.prompts[pl.ID] = true
	case PromptResolved:
		if !h.prompts[pl.ID] {
			return fmt.Errorf("%w: prompt not outstanding", ErrLifecycle)
		}
		h.prompts[pl.ID] = false
	case RunCancelling:
		if h.cancelling != nil {
			return nil // one cancel per run; later requests are the same request
		}
		c := pl
		h.cancelling = &c
	default:
		return fmt.Errorf("%w: unexpected payload", ErrInvalidEvent)
	}
	h.enqueueControlLocked(&entry{kind: kind, step: ev.Step, parent: ev.Parent, attempt: ev.Attempt, at: now, payload: p})
	return nil
}

func (h *Hub) startStepLocked(ev Event, now time.Time) error {
	st, ok := h.steps[ev.Step]
	if !ok {
		return fmt.Errorf("%w: step is not in the manifest", ErrLifecycle)
	}
	if ev.Parent != st.manifest.Parent {
		return fmt.Errorf("%w: step parent differs from the manifest", ErrLifecycle)
	}
	if ev.Parent != "" && h.steps[ev.Parent].status != StatusActive {
		return fmt.Errorf("%w: a parent starts before its children", ErrLifecycle)
	}
	if st.status == StatusActive {
		return fmt.Errorf("%w: step already active", ErrLifecycle)
	}
	if ev.Attempt != st.attempt+1 {
		return fmt.Errorf("%w: attempt %d does not follow attempt %d", ErrLifecycle, ev.Attempt, st.attempt)
	}
	st.attempt, st.status, st.started = ev.Attempt, StatusActive, now
	st.outcome, st.failure = "", nil
	st.progress = map[string]StepProgress{} // a retry starts with a fresh denominator
	h.active = append(h.active, ev.Step)
	return nil
}

func (h *Hub) checkActiveLocked(ev Event) error {
	st, ok := h.steps[ev.Step]
	if !ok || st.status != StatusActive || st.attempt != ev.Attempt {
		return fmt.Errorf("%w: step attempt is not active", ErrLifecycle)
	}
	return nil
}

func (h *Hub) checkNoActiveChildrenLocked(step StepID) error {
	for _, id := range h.active {
		if h.steps[id].manifest.Parent == step {
			return fmt.Errorf("%w: a child is still active", ErrLifecycle)
		}
	}
	return nil
}

// closeStepLocked flushes the attempt's held progress ahead of its terminal
// event, then marks it closed.
func (h *Hub) closeStepLocked(step StepID, status StepStatus, outcome StepOutcome, failure *StepFailed) {
	st := h.steps[step]
	for k, hp := range h.held {
		if k.step == step && k.attempt == st.attempt {
			hp.timer.Stop()
			delete(h.held, k)
			h.enqueueProgressLocked(k, hp.e)
		}
	}
	for k := range h.lastSent {
		if k.step == step {
			delete(h.lastSent, k)
		}
	}
	st.status, st.outcome, st.failure = status, outcome, failure
	for i, id := range h.active {
		if id == step {
			h.active = append(h.active[:i], h.active[i+1:]...)
			break
		}
	}
}

func (h *Hub) progressLocked(ev Event, p StepProgress, now time.Time) error {
	if err := h.checkActiveLocked(ev); err != nil {
		return err
	}
	h.steps[ev.Step].progress[p.MetricID] = p
	k := progressKey{step: ev.Step, attempt: ev.Attempt, metric: p.MetricID}
	e := &entry{kind: KindStepProgress, step: ev.Step, parent: ev.Parent, attempt: ev.Attempt, at: now, payload: p}
	if hp, ok := h.held[k]; ok {
		hp.e = e
		return nil
	}
	if last, ok := h.lastSent[k]; ok {
		if wait := h.opts.ProgressInterval - now.Sub(last); wait > 0 {
			hp := &heldProgress{e: e}
			h.held[k] = hp
			hp.timer = h.clock.AfterFunc(wait, func() { h.flushHeld(k, hp) })
			return nil
		}
	}
	h.enqueueProgressLocked(k, e)
	return nil
}

func (h *Hub) flushHeld(k progressKey, hp *heldProgress) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held[k] != hp || h.finished {
		return
	}
	delete(h.held, k)
	h.enqueueProgressLocked(k, hp.e)
}

// enqueueProgressLocked replaces any undelivered value of the same metric, so
// at most one per metric waits for the presenter.
func (h *Hub) enqueueProgressLocked(k progressKey, e *entry) {
	h.order++
	e.order = h.order
	h.queued[k] = e
	h.lastSent[k] = h.clock.Now()
	h.signal()
}

func (h *Hub) enqueueControlLocked(e *entry) {
	h.order++
	e.order = h.order
	h.control = append(h.control, e)
	h.signal()
}

func (h *Hub) logLocked(step StepID, l LogLine, now time.Time) {
	h.ring.add(LogEntry{Time: now, Step: step, Level: l.Level, Source: l.Source, Code: l.Code, Text: l.Text})
	h.order++
	e := &entry{order: h.order, kind: KindLogLine, step: step, at: now, payload: l, size: len(l.Text.s)}
	h.logs = append(h.logs, e)
	h.logBytes += e.size
	for len(h.logs) > h.opts.LogLines || h.logBytes > h.opts.LogBytes {
		h.logBytes -= h.logs[0].size
		h.logs[0] = nil
		h.logs = h.logs[1:]
		h.stats.LogsUndelivered++
	}
	h.signal()
}

func (h *Hub) signal() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Finish sends the run's one summary. Anything still open is closed first:
// outstanding prompts resolve as cancelled, and active steps fail with
// cancelled set when the run was cancelled. A run with open steps, an invalid
// summary or a stalled delivery never reports success. A second call returns
// ErrFinished and sends nothing.
func (h *Hub) Finish(s Summary) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return ErrFinished
	}
	var errs []error
	if err := s.validate(h.red); err != nil {
		errs = append(errs, err)
		s = s.normalise(h.red)
	}
	if h.dryRun && s.Outcome == RunSucceeded || !h.dryRun && s.Outcome == RunPlanned {
		errs = append(errs, fmt.Errorf("%w: outcome %s does not match the run's dry-run mode", ErrLifecycle, s.Outcome))
		s.Outcome = RunFailed
	}
	now := h.clock.Now()
	for id, open := range h.prompts {
		if open {
			h.prompts[id] = false
			h.enqueueControlLocked(&entry{kind: KindPromptResolved, at: now,
				payload: PromptResolved{ID: id, Disposition: PromptCancelled}})
		}
	}
	if len(h.active) > 0 {
		errs = append(errs, fmt.Errorf("%w: steps still active at finish", ErrLifecycle))
		if s.Outcome != RunCancelled {
			if s.Succeeded() {
				s.Outcome = RunFailed
			}
			if s.FailedStep == "" {
				s.FailedStep = h.active[len(h.active)-1]
			}
		}
		if s.PartialState == EffectNoneObserved {
			s.PartialState = EffectMayHaveChanged
		}
	}
	for len(h.active) > 0 { // children before parents
		id := h.active[len(h.active)-1]
		st := h.steps[id]
		f := StepFailed{Code: "run.step-abandoned", Retry: RetryUnknown, Cancelled: s.Outcome == RunCancelled,
			Effect: EffectMayHaveChanged, DurationMS: now.Sub(st.started).Milliseconds()}
		attempt := st.attempt
		h.closeStepLocked(id, StatusFailed, "", &f)
		h.enqueueControlLocked(&entry{kind: KindStepFailed, step: id, parent: st.manifest.Parent,
			attempt: attempt, at: now, payload: f})
	}
	if h.stats.DeliveryFailed {
		s.DeliveryIncomplete = true
		if s.Succeeded() {
			s.Outcome = RunFailed
		}
	}
	for k, hp := range h.held { // held progress belongs to active steps, all closed above
		hp.timer.Stop()
		delete(h.held, k)
	}
	s = s.scrub(h.red).(Summary)
	// The summary bypasses the control bound: it is the one event that must
	// always be queued, and it is the last.
	h.enqueueControlLocked(&entry{kind: KindRunSummary, at: now, payload: s})
	h.finished, h.final = true, s
	return errors.Join(errs...)
}

// next removes and returns the oldest queued entry, by emit order.
func (h *Hub) nextLocked() *entry {
	var best *entry
	from := 0
	if len(h.control) > 0 {
		best, from = h.control[0], 1
	}
	if len(h.logs) > 0 && (best == nil || h.logs[0].order < best.order) {
		best, from = h.logs[0], 2
	}
	var bestKey progressKey
	for k, e := range h.queued {
		if best == nil || e.order < best.order {
			best, from, bestKey = e, 3, k
		}
	}
	switch from {
	case 1:
		h.control[0] = nil
		h.control = h.control[1:]
		close(h.space)
		h.space = make(chan struct{})
	case 2:
		h.logBytes -= best.size
		h.logs[0] = nil
		h.logs = h.logs[1:]
	case 3:
		delete(h.queued, bestKey)
	}
	return best
}

func (h *Hub) pump() {
	for {
		h.mu.Lock()
		e := h.nextLocked()
		for e == nil {
			h.mu.Unlock()
			<-h.wake
			h.mu.Lock()
			e = h.nextLocked()
		}
		h.seq++
		env := Envelope{
			Schema: Schema, RunID: h.runID, Seq: h.seq, Time: e.at.UTC(),
			ElapsedMS: e.at.Sub(h.start).Milliseconds(), Operation: h.op, Kind: e.kind,
			StepID: e.step, ParentID: e.parent, Attempt: e.attempt, Payload: e.payload,
		}
		h.stats.Delivered++
		h.inflight = e.order
		h.mu.Unlock()
		h.sink.Deliver(env)
		h.mu.Lock()
		h.inflight = 0
		close(h.delivered)
		h.delivered = make(chan struct{})
		h.mu.Unlock()
		if e.kind == KindRunSummary {
			close(h.done)
			return
		}
	}
}

// Flush waits until every event emitted before the call has been handed to the
// sink, or dropped by the log and progress bounds. Progress still held back by
// coalescing is not waited for.
//
// It exists for a presenter that writes some output directly and renders the
// rest from events, as the plain presenter does: flushing before each direct
// write keeps the two in the order the engine produced them. It must never be
// called from a Sink, which is the goroutine it waits on.
func (h *Hub) Flush() {
	h.mu.Lock()
	mark := h.order
	for h.pendingThroughLocked(mark) {
		ch := h.delivered
		h.mu.Unlock()
		select {
		case <-ch:
		case <-h.done:
			return
		}
		h.mu.Lock()
	}
	h.mu.Unlock()
}

// pendingThroughLocked reports whether any entry with an order at or before
// mark is still queued or being delivered.
func (h *Hub) pendingThroughLocked(mark uint64) bool {
	if h.inflight != 0 && h.inflight <= mark {
		return true
	}
	if len(h.control) > 0 && h.control[0].order <= mark {
		return true
	}
	if len(h.logs) > 0 && h.logs[0].order <= mark {
		return true
	}
	for _, e := range h.queued {
		if e.order <= mark {
			return true
		}
	}
	return false
}

// StepView is one step in the projection.
type StepView struct {
	ID       StepID         `json:"id"`
	Parent   StepID         `json:"parent,omitempty"`
	Optional bool           `json:"optional,omitempty"`
	Attempt  int            `json:"attempt"`
	Status   StepStatus     `json:"status"`
	Outcome  StepOutcome    `json:"outcome,omitempty"`
	Failure  *StepFailed    `json:"failure,omitempty"`
	Progress []StepProgress `json:"progress,omitempty"`
}

// Projection is the hub's safe, current view of the run, for a presenter
// that attaches late or redraws.
type Projection struct {
	RunID      string         `json:"run_id"`
	Started    bool           `json:"started"`
	DryRun     bool           `json:"dry_run"`
	Target     Target         `json:"target"`
	Steps      []StepView     `json:"steps"`
	Cancelling *RunCancelling `json:"cancelling,omitempty"`
	Summary    *Summary       `json:"summary,omitempty"`
	Stats      Stats          `json:"stats"`
}

// Projection returns a copy of the current view.
func (h *Hub) Projection() Projection {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := Projection{RunID: h.runID, Started: h.started, DryRun: h.dryRun, Target: h.target, Stats: h.statsLocked()}
	for _, id := range h.stepOrder {
		st := h.steps[id]
		v := StepView{ID: id, Parent: st.manifest.Parent, Optional: st.manifest.Optional,
			Attempt: st.attempt, Status: st.status, Outcome: st.outcome}
		if st.failure != nil {
			f := *st.failure
			v.Failure = &f
		}
		metrics := make([]string, 0, len(st.progress))
		for m := range st.progress {
			metrics = append(metrics, m)
		}
		sort.Strings(metrics)
		for _, m := range metrics {
			v.Progress = append(v.Progress, st.progress[m].scrub(h.red).(StepProgress))
		}
		p.Steps = append(p.Steps, v)
	}
	if h.cancelling != nil {
		c := *h.cancelling
		p.Cancelling = &c
	}
	if h.finished {
		s := h.final
		p.Summary = &s
	}
	return p
}
