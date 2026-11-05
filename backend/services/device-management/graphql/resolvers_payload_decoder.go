// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"sync"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
)

// PayloadDecoderResolver resolves a stored decoder document. The kind, version and input come
// from a strict parse of the document alone, which is cheap; the computed fields (maxReadings,
// estimatedCost) need a compile, which is not (tens of milliseconds for a maximal document), so
// they come from a bounded memo of FIGURES keyed by the document's digest. A list of profiles that
// asks only for `decoder { kind }` therefore never compiles, and one that asks for the figures
// compiles each distinct document once.
//
// A document that does not parse or compile is an ERROR on the field that needed it, never a zero
// value: a decoder reported as costing nothing is a decoder nobody should trust.
type PayloadDecoderResolver struct {
	raw json.RawMessage

	once sync.Once
	doc  *payloaddecode.Document
	err  error
}

// newPayloadDecoderResolver is nil for no document.
func newPayloadDecoderResolver(raw json.RawMessage) *PayloadDecoderResolver {
	if len(raw) == 0 {
		return nil
	}
	return &PayloadDecoderResolver{raw: raw}
}

// head parses the document alone: kind, envVersion and input need no compile.
func (r *PayloadDecoderResolver) head() (*payloaddecode.Document, error) {
	r.once.Do(func() { r.doc, r.err = payloaddecode.ParseDocument(r.raw) })
	return r.doc, r.err
}

// decoderFigures are the two numbers a compile yields for the read API.
type decoderFigures struct {
	maxReadings   int32
	estimatedCost int32
	err           error
}

// compileFigures compiles the document with no metrics and keeps only the figures. A variable so a
// test can count compiles.
var compileFigures = func(doc *payloaddecode.Document) decoderFigures {
	plan, err := payloaddecode.Compile(doc, nil)
	if err != nil {
		return decoderFigures{err: err}
	}
	c := plan.EstimatedCost()
	if c > 1<<31-1 {
		c = 1<<31 - 1
	}
	return decoderFigures{maxReadings: int32(plan.MaxReadings()), estimatedCost: int32(c)}
}

// figureMemoSize bounds the memo. Entries are two integers and an error, keyed by a digest.
const figureMemoSize = 4096

// figureMemo is a process-wide LRU of figures only. It never holds a compiled program: programs
// are reachable only through their own tenant's resolution, so no cache here can serve one
// tenant's program to another. Identical document bytes yield identical figures whoever sends
// them, so sharing the figures across tenants reveals nothing.
var figureMemo = newFigureLRU(figureMemoSize)

type figureLRU struct {
	mu    sync.Mutex
	max   int
	order *list.List
	items map[[32]byte]*list.Element
	// inflight holds a compile in progress, so concurrent resolvers of the same document (the
	// executor resolves sibling fields in parallel) wait for one compile instead of each doing it.
	inflight map[[32]byte]chan struct{}
}

type figureEntry struct {
	key [32]byte
	val decoderFigures
}

func newFigureLRU(max int) *figureLRU {
	return &figureLRU{max: max, order: list.New(), items: make(map[[32]byte]*list.Element, max), inflight: map[[32]byte]chan struct{}{}}
}

func (c *figureLRU) get(key [32]byte) (decoderFigures, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*figureEntry).val, true
	}
	return decoderFigures{}, false
}

func (c *figureLRU) put(key [32]byte, val decoderFigures) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.Value.(*figureEntry).val = val
		c.order.MoveToFront(e)
		return
	}
	c.items[key] = c.order.PushFront(&figureEntry{key: key, val: val})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*figureEntry).key)
	}
}

func (r *PayloadDecoderResolver) figures() (decoderFigures, error) {
	doc, err := r.head()
	if err != nil {
		return decoderFigures{}, err
	}
	// The key covers the bytes and the library version the build compiles them against.
	key := sha256.Sum256(append([]byte{byte(payloaddecode.EnvVersion)}, r.raw...))
	f := figureMemo.figures(key, func() decoderFigures { return compileFigures(doc) })
	return f, f.err
}

// figures returns the memoised figures for key, computing them once however many callers ask at
// the same time.
func (c *figureLRU) figures(key [32]byte, compute func() decoderFigures) decoderFigures {
	for {
		if f, ok := c.get(key); ok {
			return f
		}
		c.mu.Lock()
		if wait, busy := c.inflight[key]; busy {
			c.mu.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		c.inflight[key] = done
		c.mu.Unlock()

		f := compute()
		c.put(key, f)
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
		close(done)
		return f
	}
}

func (r *PayloadDecoderResolver) Kind() (string, error) {
	d, err := r.head()
	if err != nil {
		return "", err
	}
	return string(d.Kind), nil
}

func (r *PayloadDecoderResolver) EnvVersion() (int32, error) {
	d, err := r.head()
	if err != nil {
		return 0, err
	}
	return int32(d.EnvVersion), nil
}

func (r *PayloadDecoderResolver) Input() (string, error) {
	d, err := r.head()
	if err != nil {
		return "", err
	}
	return string(d.Input), nil
}

func (r *PayloadDecoderResolver) Document() string { return string(r.raw) }

func (r *PayloadDecoderResolver) MaxReadings() (int32, error) {
	f, err := r.figures()
	return f.maxReadings, err
}

func (r *PayloadDecoderResolver) EstimatedCost() (int32, error) {
	f, err := r.figures()
	return f.estimatedCost, err
}

// Decoder is the profile's DRAFT decoder, or null.
func (r *DeviceProfileResolver) Decoder() *PayloadDecoderResolver {
	if r.M.DecoderDeclaration == nil {
		return nil
	}
	return newPayloadDecoderResolver(json.RawMessage(*r.M.DecoderDeclaration))
}

// Decoder is the decoder frozen into this version, or null. A snapshot that cannot be decoded is
// an error, not "no decoder": a missing decoder changes how every uplink of the profile resolves.
func (r *DeviceProfileVersionResolver) Decoder() (*PayloadDecoderResolver, error) {
	snap, err := model.ParseProfileSnapshot(r.M.Snapshot)
	if err != nil {
		return nil, err
	}
	return newPayloadDecoderResolver(snap.Decoder), nil
}

// ---------------------------
// Test result resolvers
// ---------------------------

type PayloadDecoderTestResultResolver struct {
	M model.PayloadDecoderTestResult
}

func (r *PayloadDecoderTestResultResolver) Ok() bool         { return r.M.OK }
func (r *PayloadDecoderTestResultResolver) Ignored() bool    { return r.M.Ignored }
func (r *PayloadDecoderTestResultResolver) Section() *string { return r.M.Section }
func (r *PayloadDecoderTestResultResolver) Cost() int32      { return r.M.Cost }
func (r *PayloadDecoderTestResultResolver) Uplinks() int32   { return r.M.Uplinks }

func (r *PayloadDecoderTestResultResolver) Readings() []*PayloadDecoderReadingResolver {
	out := make([]*PayloadDecoderReadingResolver, 0, len(r.M.Readings))
	for _, x := range r.M.Readings {
		out = append(out, &PayloadDecoderReadingResolver{M: x})
	}
	return out
}

func (r *PayloadDecoderTestResultResolver) Skipped() []*PayloadDecoderSkipResolver {
	out := make([]*PayloadDecoderSkipResolver, 0, len(r.M.Skipped))
	for _, x := range r.M.Skipped {
		out = append(out, &PayloadDecoderSkipResolver{M: x})
	}
	return out
}

func (r *PayloadDecoderTestResultResolver) Error() *PayloadDecoderErrorResolver {
	if r.M.Failure == nil {
		return nil
	}
	return &PayloadDecoderErrorResolver{M: *r.M.Failure}
}

func (r *PayloadDecoderTestResultResolver) Vectors() []*PayloadDecoderVectorResultResolver {
	out := make([]*PayloadDecoderVectorResultResolver, 0, len(r.M.Vectors))
	for _, x := range r.M.Vectors {
		out = append(out, &PayloadDecoderVectorResultResolver{M: x})
	}
	return out
}

type PayloadDecoderReadingResolver struct{ M model.PayloadDecoderReading }

func (r *PayloadDecoderReadingResolver) Key() string   { return r.M.Key }
func (r *PayloadDecoderReadingResolver) Value() string { return r.M.Value }

type PayloadDecoderSkipResolver struct{ M model.PayloadDecoderSkipped }

func (r *PayloadDecoderSkipResolver) Key() string    { return r.M.Key }
func (r *PayloadDecoderSkipResolver) Reason() string { return r.M.Reason }

type PayloadDecoderErrorResolver struct{ M model.PayloadDecoderFailure }

func (r *PayloadDecoderErrorResolver) Reason() string  { return r.M.Reason }
func (r *PayloadDecoderErrorResolver) Path() string    { return r.M.Path }
func (r *PayloadDecoderErrorResolver) Message() string { return r.M.Message }
func (r *PayloadDecoderErrorResolver) Offset() *int32  { return r.M.Offset }

type PayloadDecoderVectorResultResolver struct {
	M model.PayloadDecoderVectorOutcome
}

func (r *PayloadDecoderVectorResultResolver) Name() string { return r.M.Name }
func (r *PayloadDecoderVectorResultResolver) Ok() bool     { return r.M.OK }
func (r *PayloadDecoderVectorResultResolver) Cost() int32  { return r.M.Cost }
func (r *PayloadDecoderVectorResultResolver) Error() *string {
	if r.M.Error == "" {
		return nil
	}
	return &r.M.Error
}

type PayloadDecoderFunctionResolver struct{ M payloaddecode.FunctionDoc }

func (r *PayloadDecoderFunctionResolver) Name() string        { return r.M.Name }
func (r *PayloadDecoderFunctionResolver) Signature() string   { return r.M.Signature }
func (r *PayloadDecoderFunctionResolver) Description() string { return r.M.Doc }
