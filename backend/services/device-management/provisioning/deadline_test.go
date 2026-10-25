// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowClient is a client that sends a valid request one byte at a time. It is a raw socket
// because net/http will not send a body that slowly.
type slowClient struct {
	conn net.Conn
	body []byte
	sent int
}

func (f *fixture) dialSlow(profile, external string, n int) *slowClient {
	f.t.Helper()
	body, err := json.Marshal(f.body(profile, external, n))
	require.NoError(f.t, err)
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.srv.URL, "http://"))
	require.NoError(f.t, err)
	f.t.Cleanup(func() { conn.Close() })
	_, err = fmt.Fprintf(conn, "POST /provisioning/acme/register HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"+
		"Content-Length: %d\r\n\r\n", len(body))
	require.NoError(f.t, err)
	return &slowClient{conn: conn, body: body}
}

// trickle sends the next byte; false once the body is complete or the peer has gone.
func (c *slowClient) trickle() bool {
	if c.sent >= len(c.body) {
		return false
	}
	if _, err := c.conn.Write(c.body[c.sent : c.sent+1]); err != nil {
		return false
	}
	c.sent++
	return true
}

// status reads the response, waiting at most within; 0 when the peer closed with none.
func (c *slowClient) status(within time.Duration) int {
	_ = c.conn.SetReadDeadline(time.Now().Add(within))
	resp, err := http.ReadResponse(bufio.NewReader(c.conn), nil)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// gaugeWatch samples the in-flight gauge while a test runs and reports the highest value seen.
type gaugeWatch struct {
	stop chan struct{}
	done sync.WaitGroup
	max  atomic.Int64
}

func (f *fixture) watchInFlight() *gaugeWatch {
	w := &gaugeWatch{stop: make(chan struct{})}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		for {
			select {
			case <-w.stop:
				return
			case <-time.After(2 * time.Millisecond):
				if v := int64(testutil.ToFloat64(f.metrics.InFlight)); v > w.max.Load() {
					w.max.Store(v)
				}
			}
		}
	}()
	return w
}

func (w *gaugeWatch) finish() int64 { close(w.stop); w.done.Wait(); return w.max.Load() }

// 🔴 THE SLOW-BODY NEGATIVE CONTROL. A valid body sent at a byte per 100 ms is cut by the
// read deadline, answered 408 and counted read_timeout, and at no point did it hold a slot or
// reach authentication. The paired control sends the SAME body promptly and registers, so the
// refusal is the deadline's and not the body's; and the same client against a long deadline
// is NOT cut in the same window, so the cut is the setting's and not the harness's.
func TestASlowBodyIsCutByTheReadDeadline(t *testing.T) {
	const deadline = 400 * time.Millisecond

	t.Run("cut at the deadline", func(t *testing.T) {
		f := newFixture(t, withConfig(func(c *Config) { c.ReadDeadline = deadline }))
		f.profile("pp-1", model.ProvisionAllowNew)
		watch := f.watchInFlight()

		c := f.dialSlow("pp-1", "SN-1", 1)
		start := time.Now()
		for time.Since(start) < deadline+300*time.Millisecond && c.trickle() {
			time.Sleep(100 * time.Millisecond)
		}
		got := c.status(2 * time.Second)
		elapsed := time.Since(start)

		assert.Equal(t, http.StatusRequestTimeout, got, "the slow body was not cut")
		assert.GreaterOrEqual(t, elapsed, deadline-50*time.Millisecond, "cut before the deadline")
		assert.Less(t, elapsed, deadline+1500*time.Millisecond, "cut long after the deadline")
		assert.Less(t, c.sent, len(c.body), "the whole body was delivered")
		assert.Equal(t, int64(0), watch.finish(), "a request still sending its body held a slot")
		assert.Equal(t, map[string]float64{ResultReadTimeout: 1}, f.counted())
		assert.Equal(t, int64(0), f.registrar.auth.Load(), "a slow body reached authentication")
		assert.Equal(t, int64(0), f.compares.Load())
		d, _ := f.rows()
		assert.Equal(t, int64(0), d)
	})

	t.Run("the same body promptly registers", func(t *testing.T) {
		f := newFixture(t, withConfig(func(c *Config) { c.ReadDeadline = deadline }))
		f.profile("pp-1", model.ProvisionAllowNew)
		assert.Equal(t, 201, f.post("acme", f.body("pp-1", "SN-1", 1)).Status)
		assert.Equal(t, map[string]float64{ResultCreated: 1}, f.counted())
	})

	t.Run("a long deadline does not cut the same client", func(t *testing.T) {
		f := newFixture(t, withConfig(func(c *Config) { c.ReadDeadline = 30 * time.Second }))
		f.profile("pp-1", model.ProvisionAllowNew)
		c := f.dialSlow("pp-1", "SN-1", 1)
		start := time.Now()
		for time.Since(start) < deadline+300*time.Millisecond && c.trickle() {
			time.Sleep(100 * time.Millisecond)
		}
		assert.Equal(t, 0, c.status(100*time.Millisecond), "answered before the body was complete")
		assert.Empty(t, f.counted(), "the request finished while its body was still arriving")
	})
}

// Slow bodies hold no slot: with every slot's worth of them parked, a prompt request is
// served. This is the body-before-slot ordering proved from outside.
func TestSlowBodiesDoNotConsumeSlots(t *testing.T) {
	const slots = 3
	f := newFixture(t, withConfig(func(c *Config) { c.MaxInFlight = slots; c.ReadDeadline = 30 * time.Second }))
	f.profile("pp-1", model.ProvisionAllowNew)
	var parked []*slowClient
	for i := 0; i < slots*3; i++ {
		c := f.dialSlow("pp-1", fmt.Sprintf("SN-slow-%d", i), i+10)
		require.True(t, c.trickle())
		parked = append(parked, c)
	}
	time.Sleep(100 * time.Millisecond)

	got := f.post("acme", f.body("pp-1", "SN-prompt", 1))
	assert.Equal(t, 201, got.Status, "slow bodies starved a prompt request: %s", got.Raw)
	// Read by the request itself on entry to the model, so no sampling interval can miss it.
	assert.Equal(t, int64(1), f.registrar.maxGauge.Load(), "only the prompt request ever held a slot")
	assert.Equal(t, 0.0, f.count(ResultBusy))
	_ = parked
}

// If the connection cannot take a per-request deadline (a wrapper hides it), the request is
// refused rather than served without its bound.
func TestARequestThatCannotCarryADeadlineIsRefused(t *testing.T) {
	f := newFixture(t)
	f.profile("pp-1", model.ProvisionAllowNew)
	rec := &noDeadlineWriter{header: http.Header{}}
	body, _ := json.Marshal(f.body("pp-1", "SN-1", 1))
	req, _ := http.NewRequest(http.MethodPost, "/provisioning/acme/register", strings.NewReader(string(body)))
	req.SetPathValue("tenant", "acme")
	f.handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.status)
	assert.Equal(t, 1.0, f.count(ResultUnavailable))
	d, _ := f.rows()
	assert.Equal(t, int64(0), d)
}

// noDeadlineWriter is a ResponseWriter with no Unwrap and no SetReadDeadline.
type noDeadlineWriter struct {
	header http.Header
	status int
}

func (w *noDeadlineWriter) Header() http.Header         { return w.header }
func (w *noDeadlineWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *noDeadlineWriter) WriteHeader(code int)        { w.status = code }
