// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// Reuse the capture harness's decoder, callbacks and writer, but stop its isolated
// pipeline before driving the full Execute lifecycle, as the HTTP restart tests do.
func newGatewayRestartHarness(t *testing.T, messages ...messaging.Message) *captureHarness {
	t.Helper()
	h := newCaptureHarness(t)
	require.True(t, h.source.stopPipeline(context.Background()))
	h.source.SetReader(&sliceReader{msgs: messages})
	t.Cleanup(func() { require.NoError(t, h.source.ExecuteStop(context.Background())) })
	return h
}

// release is safe both in the test body and in cleanup after a failed assertion.
func gatewayRestartLatch(t *testing.T) (entered chan struct{}, hold chan struct{}, release func()) {
	t.Helper()
	entered, hold = make(chan struct{}), make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	return
}

func requireGatewayRunUnchanged(t *testing.T, es *GatewayJetStreamSource) {
	t.Helper()
	messages, drained, publishes := es.messages, es.drained, es.publishes
	workersDone, submitterDone := es.workersDone, es.submitterDone
	cancel := reflect.ValueOf(es.cancel).Pointer()
	poison, ce, uplinks := es.poison, es.ce, es.uplinkRun
	err := es.ExecuteStart(context.Background())
	require.ErrorContains(t, err, "the previous run has not finished stopping")
	require.Contains(t, err.Error(), "retry ExecuteStop until it completes")
	require.Equal(t, messages, es.messages)
	require.Equal(t, drained, es.drained)
	require.Equal(t, publishes, es.publishes)
	require.Equal(t, workersDone, es.workersDone)
	require.Equal(t, submitterDone, es.submitterDone)
	require.Equal(t, cancel, reflect.ValueOf(es.cancel).Pointer())
	require.True(t, poison == es.poison)
	require.True(t, ce == es.ce)
	require.True(t, uplinks == es.uplinkRun)
}

func TestGatewayJetStreamSource_FirstStart(t *testing.T) {
	h := newGatewayRestartHarness(t)
	es := h.source
	require.NoError(t, es.ExecuteStart(context.Background()))
	require.NotNil(t, es.messages)
	require.NotNil(t, es.drained)
	require.NoError(t, es.ExecuteStop(context.Background()))
}

func TestGatewayJetStreamSource_RestartAfterReadLoopStopTimeout(t *testing.T) {
	for _, stops := range []int{1, 2} {
		name := "one timed-out stop"
		if stops == 2 {
			name = "two timed-out stops"
		}
		t.Run(name, func(t *testing.T) {
			h := newGatewayRestartHarness(t, capturedMsg(captureSubject, validEvent, 1, 1, &recordingAck{}))
			es := h.source
			entered, hold, release := gatewayRestartLatch(t)
			// received runs inside handle, before its send on es.messages. The read
			// loop cannot finish while this latch is held, even after cancellation.
			es.received = func(string, []byte) {
				close(entered)
				<-hold
			}
			require.NoError(t, es.ExecuteStart(context.Background()))
			<-entered
			expired, cancel := context.WithCancel(context.Background())
			cancel()
			for i := 0; i < stops; i++ {
				require.NoError(t, es.ExecuteStop(expired))
				select {
				case <-es.drained:
					t.Fatal("the read loop finished while handle was held")
				default:
				}
				requireGatewayRunUnchanged(t, es)
			}
			release()
			require.NoError(t, es.ExecuteStop(context.Background()))
			previousMessages, previousDrained := es.messages, es.drained
			require.NoError(t, es.ExecuteStart(context.Background()))
			require.NotEqual(t, previousMessages, es.messages)
			require.NotEqual(t, previousDrained, es.drained)
			require.NoError(t, es.ExecuteStop(context.Background()))
		})
	}
}

func TestGatewayJetStreamSource_RetryStopAfterPipelineTimeout(t *testing.T) {
	for _, stage := range []string{"decode workers", "publish submitter"} {
		t.Run(stage, func(t *testing.T) {
			h := newGatewayRestartHarness(t, capturedMsg(captureSubject, validEvent, 1, 1, &recordingAck{}))
			es := h.source
			entered, hold, release := gatewayRestartLatch(t)
			if stage == "decode workers" {
				build := es.build
				es.build = func(source, tenant string, event *model.UnresolvedEvent, payload interface{}, id string) (context.Context, messaging.Message, bool) {
					close(entered)
					<-hold
					return build(source, tenant, event, payload, id)
				}
			} else {
				es.SetWriter(gatewayRestartHeldWriter{harnessWriter{h}, entered, hold})
			}
			require.NoError(t, es.ExecuteStart(context.Background()))
			<-entered
			// Cancel only after the stop has closed the channel ahead of the held
			// stage. This proves the timeout happened inside stopPipeline.
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			stopped := make(chan struct{})
			var stopErr error
			go func() {
				defer close(stopped)
				stopErr = es.ExecuteStop(ctx)
			}()
			t.Cleanup(func() {
				release()
				cancel()
				<-stopped
			})
			if stage == "decode workers" {
				_, open := <-es.messages
				require.False(t, open)
			} else {
				_, open := <-es.publishes
				require.False(t, open)
			}
			cancel()
			<-stopped
			require.NoError(t, stopErr)
			requireGatewayRunUnchanged(t, es)
			// A second expired stop must not close either channel twice. The held
			// stage still prevents completion, so restarting remains forbidden.
			require.NotPanics(t, func() { require.NoError(t, es.ExecuteStop(ctx)) })
			requireGatewayRunUnchanged(t, es)
			release()
			require.NoError(t, es.ExecuteStop(context.Background()))
			es.SetWriter(harnessWriter{h})
			require.NoError(t, es.ExecuteStart(context.Background()))
			require.NoError(t, es.ExecuteStop(context.Background()))
		})
	}
}

type gatewayRestartHeldWriter struct {
	harnessWriter
	entered chan struct{}
	hold    chan struct{}
}

func (w gatewayRestartHeldWriter) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	close(w.entered)
	<-w.hold
	w.harnessWriter.Publish(ctx, msg, done)
}

func TestGatewayJetStreamSource_RetryStopWaitsForOriginalWriterClose(t *testing.T) {
	h := newGatewayRestartHarness(t)
	es := h.source
	entered, hold, release := gatewayRestartLatch(t)
	writer := &gatewayRestartClosingWriter{harnessWriter: harnessWriter{h}, entered: entered, hold: hold}
	es.SetWriter(writer)
	require.NoError(t, es.ExecuteStart(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stopped := make(chan struct{})
	var stopErr error
	go func() {
		defer close(stopped)
		stopErr = es.ExecuteStop(ctx)
	}()
	t.Cleanup(func() {
		release()
		cancel()
		<-stopped
	})
	<-entered
	cancel()
	<-stopped
	require.NoError(t, stopErr)
	requireGatewayRunUnchanged(t, es)
	closed := es.writerClosed
	require.NotNil(t, closed)
	require.NoError(t, es.ExecuteStop(ctx))
	require.Equal(t, closed, es.writerClosed)
	requireGatewayRunUnchanged(t, es)
	release()
	require.NoError(t, es.ExecuteStop(context.Background()))
	writer.mu.Lock()
	calls := writer.calls
	writer.mu.Unlock()
	require.Equal(t, 1, calls, "stop retries must await the original Close")
	es.SetWriter(harnessWriter{h})
	require.NoError(t, es.ExecuteStart(context.Background()))
	require.NoError(t, es.ExecuteStop(context.Background()))
}

// Like OrderedWriter, repeat Close calls return immediately, even while the first
// call is still waiting for settlement. The source must retain that first wait.
type gatewayRestartClosingWriter struct {
	harnessWriter
	entered chan struct{}
	hold    chan struct{}
	mu      sync.Mutex
	calls   int
}

func (w *gatewayRestartClosingWriter) Close() {
	w.mu.Lock()
	w.calls++
	first := w.calls == 1
	w.mu.Unlock()
	if first {
		close(w.entered)
		<-w.hold
	}
}
