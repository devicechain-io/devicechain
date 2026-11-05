// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package otafleet

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Mode is how one simulated device behaves when the platform updates it.
type Mode string

const (
	// Healthy devices take the update the way the reference device does.
	Healthy Mode = "healthy"
	// BadHash devices are served an image with one byte flipped, so the SHA-256 of what they hold
	// is not the digest they were assigned. The corruption is real, in the bytes on the wire, and
	// the device's own verification is what refuses it: FAILED, VERIFICATION_FAILED.
	BadHash Mode = "bad-hash"
	// BadSignature devices have one bit of the assignment's signature flipped before it is
	// verified, so a correctly signed image is refused: FAILED, SIGNATURE_INVALID.
	BadSignature Mode = "bad-signature"
	// Interrupted devices lose the connection part-way through the download once, then resume
	// with a Range request from the bytes they already hold and finish normally.
	Interrupted Mode = "interrupted"
	// Offline devices never reach the broker (or, with OfflineFor, only after that long): the
	// platform assigns to a device that does not answer.
	Offline Mode = "offline"
)

// Faults says how many devices of the fleet misbehave, and how. Devices are taken in the order
// the scenario provisioned them: the first BadHash, then Interrupted, BadSignature and Offline,
// and every device after those is Healthy.
type Faults struct {
	BadHash      int
	Interrupted  int
	BadSignature int
	Offline      int
	// OfflineFor brings an Offline device onto the broker after this long; zero keeps it off for
	// as long as the process runs.
	OfflineFor time.Duration
}

// Validate rejects a negative count.
func (f Faults) Validate() error {
	for name, n := range map[string]int{"bad-hash": f.BadHash, "interrupted": f.Interrupted,
		"bad-signature": f.BadSignature, "offline": f.Offline} {
		if n < 0 {
			return fmt.Errorf("otafleet: the %s device count must not be negative", name)
		}
	}
	if f.OfflineFor < 0 {
		return errors.New("otafleet: OfflineFor must not be negative")
	}
	return nil
}

// Total is the number of devices that misbehave.
func (f Faults) Total() int { return f.BadHash + f.Interrupted + f.BadSignature + f.Offline }

// ModeFor is the mode of the device at index i of the fleet.
func (f Faults) ModeFor(i int) Mode {
	switch {
	case i < f.BadHash:
		return BadHash
	case i < f.BadHash+f.Interrupted:
		return Interrupted
	case i < f.BadHash+f.Interrupted+f.BadSignature:
		return BadSignature
	case i < f.Total():
		return Offline
	}
	return Healthy
}

// errCut is what a body read returns when the injected interruption cuts the connection.
var errCut = fmt.Errorf("otafleet: connection cut (injected): %w", io.ErrUnexpectedEOF)

// faultTransport injects the network faults into a device's download. It sits between the
// updater's ranged GETs and the real transport, so the updater sees exactly what a flaky path
// would give it: bytes that are wrong, or a body that ends early.
type faultTransport struct {
	next http.RoundTripper
	mode Mode

	corrupted atomic.Int64

	mu           sync.Mutex
	interrupted  int
	resumes      int
	resumeOffset int64 // start of the first ranged GET after the interruption
	cutDone      bool
}

func (t *faultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	var start, end, total int64
	switch resp.StatusCode {
	case http.StatusPartialContent:
		var ok bool
		if start, end, total, ok = parseContentRange(resp.Header.Get("Content-Range")); !ok {
			return resp, nil
		}
	case http.StatusOK:
		// A server that ignores Range and sends the whole object: the updater accepts that at
		// offset zero, so the faults must apply to it too or a one-window image would sail through
		// a device that was meant to misbehave.
		body, err := readAll(resp)
		if err != nil {
			return resp, err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		start, end, total = 0, int64(len(body))-1, int64(len(body))
	default:
		return resp, nil
	}
	switch t.mode {
	case BadHash:
		// The first window this device fetches, wherever it starts: a device resumed from a
		// persisted state dir picks up at a nonzero offset and must still be served bad bytes.
		if t.corrupted.Load() > 0 {
			return resp, nil
		}
		body, err := readAll(resp)
		if err != nil || len(body) == 0 {
			return resp, err
		}
		body[0] ^= 0xFF
		t.corrupted.Add(1)
		resp.Body = io.NopCloser(bytes.NewReader(body))
	case Interrupted:
		t.mu.Lock()
		cut := !t.cutDone && (start > 0 || end+1 >= total)
		if t.cutDone && t.resumes == 0 {
			t.resumes++
			t.resumeOffset = start
		}
		if cut {
			t.cutDone = true
			t.interrupted++
		}
		t.mu.Unlock()
		if !cut {
			return resp, nil
		}
		body, err := readAll(resp)
		if err != nil {
			return resp, err
		}
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body[:len(body)/2]), errReader{errCut}))
	}
	return resp, nil
}

func (t *faultTransport) counters() (corrupted, interrupted, resumes int, resumeOffset int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return int(t.corrupted.Load()), t.interrupted, t.resumes, t.resumeOffset
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func readAll(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// parseContentRange reads `bytes a-b/total`.
func parseContentRange(h string) (start, end, total int64, ok bool) {
	rest, found := strings.CutPrefix(h, "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, size, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, 0, false
	}
	a, b, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err error
	if start, err = strconv.ParseInt(strings.TrimSpace(a), 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if end, err = strconv.ParseInt(strings.TrimSpace(b), 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if total, err = strconv.ParseInt(strings.TrimSpace(size), 10, 64); err != nil {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

// DownloadTransport is the transport the fleet fetches artifacts with. dialAddr, when set,
// is the host:port every connection goes to whatever the URL names: the address an instance is
// reached through (a port-forward, a load balancer) while the capability URL names the instance
// host, which stays the TLS server name and the Host header. insecure skips verification of the
// instance's certificate, the same trust decision the handshake records for the MQTT gateway.
//
// redirectAddr, when set (and dialAddr is not), redirects a connection to the same HOST as that
// address but another port: a capability URL names the instance on its default port while the sim
// reached the instance on a remapped one (--server localhost:9180).
func DownloadTransport(dialAddr, redirectAddr string, insecure bool) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	switch {
	case dialAddr != "":
		t.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, dialAddr)
		}
	case redirectAddr != "":
		rh, _, _ := net.SplitHostPort(redirectAddr)
		t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if h, _, err := net.SplitHostPort(addr); err == nil && strings.EqualFold(h, rh) {
				addr = redirectAddr
			}
			return d.DialContext(ctx, network, addr)
		}
	}
	if insecure {
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		t.TLSClientConfig.InsecureSkipVerify = true // #nosec G402 -- the handshake's recorded trust decision
	}
	return t
}
