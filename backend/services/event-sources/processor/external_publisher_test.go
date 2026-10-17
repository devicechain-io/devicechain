// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAsyncWriter records what an AsyncEventPublisher does to its writer, and answers each
// Submit with a chosen outcome.
type fakeAsyncWriter struct {
	mu        sync.Mutex
	submitted int
	outcome   error
	draining  atomic.Int32
	closed    atomic.Int32
}

func (w *fakeAsyncWriter) Submit(_ context.Context, msgs []messaging.Message, done func(error)) {
	w.mu.Lock()
	w.submitted += len(msgs)
	err := w.outcome
	w.mu.Unlock()
	done(err)
}
func (w *fakeAsyncWriter) Settle(context.Context) error { return nil }
func (w *fakeAsyncWriter) Draining()                    { w.draining.Add(1) }
func (w *fakeAsyncWriter) Close()                       { w.closed.Add(1) }

type publishRecorder struct {
	backpressured, failed atomic.Int32
	lastErr               atomic.Value
}

func (r *publishRecorder) hooks() PublishHooks {
	return PublishHooks{
		Backpressured: func(string) { r.backpressured.Add(1) },
		Failed:        func(_ string, err error) { r.failed.Add(1); r.lastErr.Store(err) },
	}
}

func buildOK(source, tenant string, _ *model.UnresolvedEvent, _ interface{}, _ string) (context.Context, messaging.Message, bool) {
	return core.WithTenant(context.Background(), tenant), messaging.Message{Value: []byte(source)}, true
}

func buildDrop(string, string, *model.UnresolvedEvent, interface{}, string) (context.Context, messaging.Message, bool) {
	return nil, messaging.Message{}, false
}

func publishOne(p *AsyncEventPublisher) error {
	var got error
	called := 0
	p.Publish("ext", "acme", &model.UnresolvedEvent{}, nil, "", func(err error) { got = err; called++ })
	if called != 1 {
		return errors.New("done did not run exactly once")
	}
	return got
}

func TestAPublisherRefusesWhatItCouldNotReportThrough(t *testing.T) {
	r := &publishRecorder{}
	_, err := NewAsyncEventPublisher("ext", nil, r.hooks())
	require.Error(t, err, "no message builder")
	_, err = NewAsyncEventPublisher("ext", buildOK, PublishHooks{Failed: func(string, error) {}})
	require.ErrorIs(t, err, errNoPublishHooks, "no backpressure hook")
	_, err = NewAsyncEventPublisher("ext", buildOK, PublishHooks{Backpressured: func(string) {}})
	require.ErrorIs(t, err, errNoPublishHooks, "no failure hook")
}

// A failed publish is COUNTED and the source's done gets the error: it is never a silent
// drop. The control (a stored event) counts nothing.
func TestAFailedPublishIsCountedAndReported(t *testing.T) {
	r := &publishRecorder{}
	p, err := NewAsyncEventPublisher("ext", buildOK, r.hooks())
	require.NoError(t, err)
	w := &fakeAsyncWriter{}
	p.SetWriter(w)

	require.NoError(t, publishOne(p))
	assert.Zero(t, r.failed.Load(), "a stored event is not a failure")

	boom := errors.New("broker said no")
	w.mu.Lock()
	w.outcome = boom
	w.mu.Unlock()
	require.ErrorIs(t, publishOne(p), boom)
	assert.Equal(t, int32(1), r.failed.Load(), "a failed publish must be counted")
	assert.ErrorIs(t, r.lastErr.Load().(error), boom)
	assert.Zero(t, r.backpressured.Load(), "a failure is not backpressure")
}

// A refusal at a closed gate is backpressure, counted as such and NOT as a lost event, as
// the synchronous publish counted it.
func TestABackpressureRefusalIsCountedAsBackpressureNotAsFailure(t *testing.T) {
	r := &publishRecorder{}
	p, err := NewAsyncEventPublisher("ext", buildOK, r.hooks())
	require.NoError(t, err)
	p.SetWriter(&fakeAsyncWriter{outcome: &messaging.BackpressureError{Stale: true}})

	require.ErrorIs(t, publishOne(p), messaging.ErrStreamBackpressure)
	assert.Equal(t, int32(1), r.backpressured.Load())
	assert.Zero(t, r.failed.Load())
}

// A publish with no writer installed (before the broker components exist, or after Close) is
// reported failed and counted, never dropped quietly.
func TestAPublishWithNoWriterFailsLoudly(t *testing.T) {
	r := &publishRecorder{}
	p, err := NewAsyncEventPublisher("ext", buildOK, r.hooks())
	require.NoError(t, err)

	require.ErrorIs(t, publishOne(p), errNoWriter)
	assert.Equal(t, int32(1), r.failed.Load())

	w := &fakeAsyncWriter{}
	p.SetWriter(w)
	p.Close()
	require.ErrorIs(t, publishOne(p), errNoWriter, "a closed publisher must not publish")
	assert.Equal(t, 0, w.submitted)
}

// An event the builder drops terminally publishes nothing and settles nil.
func TestATerminalDropPublishesNothing(t *testing.T) {
	r := &publishRecorder{}
	p, err := NewAsyncEventPublisher("ext", buildDrop, r.hooks())
	require.NoError(t, err)
	w := &fakeAsyncWriter{}
	p.SetWriter(w)

	require.NoError(t, publishOne(p))
	assert.Equal(t, 0, w.submitted)
	assert.Zero(t, r.failed.Load())
}

// Replacing the writer, and Close, drain and close the one being let go, once each: a
// service that builds its broker components again must not leak the earlier writer.
func TestReplacingOrClosingTheWriterClosesTheOldOne(t *testing.T) {
	p, err := NewAsyncEventPublisher("ext", buildOK, (&publishRecorder{}).hooks())
	require.NoError(t, err)
	first, second := &fakeAsyncWriter{}, &fakeAsyncWriter{}

	p.SetWriter(first)
	assert.Zero(t, first.closed.Load())
	p.SetWriter(second)
	assert.Equal(t, int32(1), first.closed.Load(), "the replaced writer must be closed")
	assert.Equal(t, int32(1), first.draining.Load(), "and told it is draining, so a backoff does not hold the close")
	assert.Zero(t, second.closed.Load())

	p.Close()
	p.Close()
	assert.Equal(t, int32(1), second.closed.Load(), "Close closes the installed writer, once")
	assert.Equal(t, int32(1), first.closed.Load())
}

// Settle with nothing installed has nothing to wait for.
func TestSettleWithNoWriterIsNil(t *testing.T) {
	p, err := NewAsyncEventPublisher("ext", buildOK, (&publishRecorder{}).hooks())
	require.NoError(t, err)
	require.NoError(t, p.Settle(context.Background()))
}

func TestASourceRefusesANilPublisher(t *testing.T) {
	_, err := NewMqttEventSource("ext", "cid", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, nil,
		func(string, string, []byte, error) error { return nil },
		nil, admitAllReadings, admitAll, alwaysOwns, func(error) {})
	require.ErrorIs(t, err, errNoPublisher)
}
