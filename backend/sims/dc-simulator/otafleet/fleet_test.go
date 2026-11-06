// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package otafleet_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	"github.com/devicechain-io/dc-simulator/internal/platformtest"
	"github.com/devicechain-io/dc-simulator/otafleet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	instance = "inst"
	tenant   = "acme"
	chunk    = 4096
	imgSize  = 5*chunk + 123 // six windows, the last one short
)

var testPolicy = ota.Policy{
	Acknowledge: time.Hour, Download: time.Hour, Verify: time.Hour, Install: time.Hour, Confirm: time.Hour,
}

// signer is a test signing key: the device is given pem, the platform signs with key.
type signer struct {
	key *ecdsa.PrivateKey
	pem string
	id  string
}

func newSigner(t *testing.T) signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	sum := sha256.Sum256(der)
	return signer{key: key, id: hex.EncodeToString(sum[:]),
		pem: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
}

// sign signs the raw SHA-256 digest of img, as the contract says.
func (s signer) sign(t *testing.T, img []byte) *ota.AssignmentSignature {
	t.Helper()
	d := sha256.Sum256(img)
	sig, err := ecdsa.SignASN1(rand.Reader, s.key, d[:])
	require.NoError(t, err)
	return &ota.AssignmentSignature{Signature: base64.StdEncoding.EncodeToString(sig), KeyID: s.id,
		Algorithm: ota.AlgorithmECDSAP256SHA256}
}

func image() []byte {
	b := make([]byte, imgSize)
	for i := range b {
		b[i] = byte(i*13 + 5)
	}
	return b
}

// platform plays the instance for a test: the broker's device-reports topics go to the real
// update reducer (one attempt per device), a download request is answered with a capability URL,
// and a sync makes it resend the assignment the device should still be working on. The artifact
// is served by an httptest server that honours Range, and records every request it saw.
type platform struct {
	t      *testing.T
	broker *fakemqtt.Broker
	srv    *httptest.Server
	clock  *platformtest.Clock
	img    []byte

	mu       sync.Mutex
	attempts map[string]*platformtest.Platform
	assigned map[string][]byte // the assignment message per device, for the resend on sync
	ranges   map[string][]string
	grants   int

	ignoreRange bool // answer every GET with 200 and the whole object
}

func newPlatform(t *testing.T) *platform {
	t.Helper()
	p := &platform{t: t, broker: fakemqtt.New(t), clock: platformtest.NewClock(time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)),
		img: image(), attempts: map[string]*platformtest.Platform{}, assigned: map[string][]byte{},
		ranges: map[string][]string{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.ranges[r.URL.Query().Get("dev")] = append(p.ranges[r.URL.Query().Get("dev")], r.Header.Get("Range"))
		p.mu.Unlock()
		if p.ignoreRange {
			_, _ = w.Write(p.img)
			return
		}
		http.ServeContent(w, r, "image", time.Time{}, strings.NewReader(string(p.img)))
	}))
	t.Cleanup(p.srv.Close)
	p.broker.Authorize = func(username, _, clientID string) bool {
		// The update session is a second session of the device, never its plain client id.
		return strings.HasPrefix(username, tenant+":") && strings.Contains(clientID, ":ota")
	}
	p.broker.OnPublish = p.onPublish
	return p
}

func (p *platform) desired(dev string) string {
	return fmt.Sprintf("%s/%s/device-desired/%s", instance, tenant, dev)
}

func (p *platform) onPublish(_, topic string, payload []byte) {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[2] != "device-reports" {
		return
	}
	dev := parts[3]
	rep, err := ota.DecodeDeviceReport(payload)
	require.NoError(p.t, err, "the platform's own decoder refused a frame from %s: %s", dev, payload)
	p.mu.Lock()
	att := p.attempts[dev]
	p.mu.Unlock()
	switch rep.Kind {
	case ota.KindProgress, ota.KindInventory:
		if att != nil {
			_, _ = att.Receive(payload)
		}
	case ota.KindDownloadRequest:
		p.mu.Lock()
		p.grants++
		p.mu.Unlock()
		msg, err := ota.EncodeDownloadGrant(rep.DownloadRequest.AssignmentID,
			p.srv.URL+"/artifact?dev="+dev, time.Now().Add(10*time.Minute)) // the device judges expiry by its own clock
		require.NoError(p.t, err)
		p.broker.Deliver(p.desired(dev), msg)
	case ota.KindSync:
		p.mu.Lock()
		msg := p.assigned[dev]
		p.mu.Unlock()
		if msg != nil && att != nil && !att.Attempt().State.Terminal() {
			p.broker.Deliver(p.desired(dev), msg)
		}
	}
}

// assign hands the device an update of img to version, signed (sig non-nil) or not.
func (p *platform) assign(dev, attempt, version string, img []byte, sig *ota.AssignmentSignature) {
	p.t.Helper()
	dig := "sha256:" + hex.EncodeToString(func() []byte { s := sha256.Sum256(img); return s[:] }())
	trust := ota.TrustUnverified
	if sig != nil {
		trust = ota.TrustSigned
	}
	att, err := ota.NewAttempt(attempt, "asg-"+attempt, "firmware",
		ota.Target{Version: version, Digest: dig, RequiresReboot: true}, p.clock.Now())
	require.NoError(p.t, err)
	msg, err := ota.EncodeUpdateAssignment(attempt, "asg-"+attempt, "firmware", trust, sig,
		ota.AssignmentArtifact{Version: version, Digest: dig, Size: int64(len(img)), RequiresReboot: true})
	require.NoError(p.t, err)
	p.mu.Lock()
	p.attempts[dev] = platformtest.New(p.clock, testPolicy, att)
	p.assigned[dev] = msg
	p.mu.Unlock()
	p.broker.Deliver(p.desired(dev), msg) // lost if the device is not subscribed: it syncs later
}

func (p *platform) attempt(dev string) ota.Attempt {
	p.mu.Lock()
	a := p.attempts[dev]
	p.mu.Unlock()
	if a == nil {
		return ota.Attempt{}
	}
	return a.Attempt()
}

func (p *platform) rangesOf(dev string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.ranges[dev]...)
}

func (p *platform) await(dev string, want ...ota.State) ota.Attempt {
	p.t.Helper()
	var a ota.Attempt
	require.Eventuallyf(p.t, func() bool {
		a = p.attempt(dev)
		for _, w := range want {
			if a.State == w {
				return true
			}
		}
		return false
	}, 15*time.Second, 20*time.Millisecond, "device %s never reached %v; platform sees %+v", dev, want, p.attempt(dev))
	return a
}

// fleet builds a fleet against the platform's broker and attaches n devices dev-0 .. dev-(n-1).
func (p *platform) fleet(opts otafleet.Options, n int) *otafleet.Fleet {
	p.t.Helper()
	if opts.InitialVersion == "" {
		opts.InitialVersion = "1.0.0"
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize = chunk
	}
	f, err := otafleet.New(instance, tenant, p.broker.URL(), nil, opts)
	require.NoError(p.t, err)
	p.t.Cleanup(f.Close)
	for i := range n {
		require.NoError(p.t, f.Attach(context.Background(), fmt.Sprintf("dev-%d", i), fmt.Sprintf("cred-%d", i), i))
	}
	return f
}

func (p *platform) version(f *otafleet.Fleet, dev string) string {
	d, ok := f.Device(dev)
	require.True(p.t, ok)
	return d.Version
}

// A healthy device takes a signed update end to end against a real broker: assignment received,
// grant requested, the image fetched in ranged windows, size, hash and signature verified, the
// version swapped and confirmed, every report accepted by the platform's own reducer.
func TestHealthyDeviceTakesASignedUpdate(t *testing.T) {
	p := newPlatform(t)
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}}, 1)
	require.Equal(t, 1, p.broker.Subscribers(p.desired("dev-0")))

	p.assign("dev-0", "att1", "2.0.0", p.img, sg.sign(t, p.img))
	a := p.await("dev-0", ota.StateUpdated)
	assert.Nil(t, a.Failure)
	assert.Equal(t, "2.0.0", p.version(f, "dev-0"))

	// Range-resumable download: every window asked for starts where the last ended.
	var next int64
	for _, r := range p.rangesOf("dev-0") {
		var from, to int64
		_, err := fmt.Sscanf(r, "bytes=%d-%d", &from, &to)
		require.NoError(t, err, r)
		assert.Equal(t, next, from, "window %q is not contiguous", r)
		next = to + 1
	}
	assert.GreaterOrEqual(t, next, int64(imgSize))
	assert.GreaterOrEqual(t, len(p.rangesOf("dev-0")), 6)

	// No frame was refused: the platform's strict decoder took every one.
	assert.Zero(t, p.attempts["dev-0"].Rejected())
	r := f.Report()
	assert.Equal(t, map[string]int{"2.0.0": 1}, r.Versions)
	assert.Empty(t, r.Failures)
}

// The update session is not the device's plain session, so a command far end holding the plain
// client id is not kicked off the broker by it.
func TestUpdateSessionUsesADiscriminatedClientID(t *testing.T) {
	p := newPlatform(t)
	var seen string
	var mu sync.Mutex
	p.broker.Authorize = func(_, _, clientID string) bool { mu.Lock(); seen = clientID; mu.Unlock(); return true }
	p.fleet(otafleet.Options{AllowUnsigned: true}, 1)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, instance+":"+tenant+":dev-0:ota", seen)
}

// An unsigned image is refused by a device that requires signatures, and the refusal is a report,
// not a silence: FAILED UNKNOWN_SIGNER, version unchanged.
func TestUnsignedImageRefusedByDefault(t *testing.T) {
	p := newPlatform(t)
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}}, 1)
	p.assign("dev-0", "att1", "2.0.0", p.img, nil)
	a := p.await("dev-0", ota.StateFailed)
	require.NotNil(t, a.Failure)
	assert.Equal(t, ota.CodeUnknownSigner, a.Failure.Code)
	assert.Equal(t, "1.0.0", p.version(f, "dev-0"))
}

// Each injected fault produces the state the platform should see.
func TestFailureInjectionModes(t *testing.T) {
	sg := newSigner(t)
	cases := []struct {
		name   string
		faults otafleet.Faults
		state  ota.State
		code   string
		want   string // version the device runs afterwards
	}{
		{"bad hash", otafleet.Faults{BadHash: 1}, ota.StateFailed, "VERIFICATION_FAILED", "1.0.0"},
		{"bad signature", otafleet.Faults{BadSignature: 1}, ota.StateFailed, ota.CodeSignatureInvalid, "1.0.0"},
		{"interrupted download", otafleet.Faults{Interrupted: 1}, ota.StateUpdated, "", "2.0.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPlatform(t)
			f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}, Faults: c.faults}, 1)
			p.assign("dev-0", "att1", "2.0.0", p.img, sg.sign(t, p.img))
			a := p.await("dev-0", c.state)
			if c.code != "" {
				require.NotNil(t, a.Failure)
				assert.Equal(t, c.code, a.Failure.Code)
				assert.Contains(t, f.Report().Stages, "FAILED")
			} else {
				assert.Nil(t, a.Failure)
			}
			assert.Equal(t, c.want, p.version(f, "dev-0"), "a device that failed must not claim the new version")
			d, _ := f.Device("dev-0")
			if c.code != "" {
				assert.Equal(t, "FAILED "+c.code, d.LastReported)
			} else {
				assert.Equal(t, "RUNNING", d.LastReported)
			}
		})
	}
}

// The bad-hash fault corrupts bytes on the wire, and it is the device's own digest check that
// refuses them: the platform sees the FAILED detail naming both digests.
func TestBadHashIsARealDigestMismatch(t *testing.T) {
	p := newPlatform(t)
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}, Faults: otafleet.Faults{BadHash: 1}}, 1)
	p.assign("dev-0", "att1", "2.0.0", p.img, sg.sign(t, p.img))
	a := p.await("dev-0", ota.StateFailed)
	assert.Contains(t, a.Failure.Detail, "digest mismatch")
	d, _ := f.Device("dev-0")
	assert.Equal(t, 1, d.Corrupted)
	assert.Equal(t, "FAILED VERIFICATION_FAILED", d.LastReported)
}

// An interrupted download resumes from the bytes it holds: the request after the cut starts at
// the offset the device had reached, never at zero, and the update completes.
func TestInterruptedDownloadResumesFromHeldBytes(t *testing.T) {
	p := newPlatform(t)
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}, Faults: otafleet.Faults{Interrupted: 1}}, 1)
	p.assign("dev-0", "att1", "2.0.0", p.img, sg.sign(t, p.img))
	p.await("dev-0", ota.StateUpdated)

	d, _ := f.Device("dev-0")
	assert.Equal(t, 1, d.Interrupted)
	assert.Equal(t, 1, d.Resumes)
	assert.Equal(t, int64(chunk), d.ResumedAtByte, "the cut came in the second window; the device held exactly the first")
	// The server saw the window it was cut in asked for again from the same offset, and no
	// request ever went back before what the device held.
	rs := p.rangesOf("dev-0")
	require.GreaterOrEqual(t, len(rs), 7)
	assert.Equal(t, rs[1], rs[2])
	assert.Equal(t, 1, f.Report().Counts["interruptions"])
	assert.Equal(t, 1, f.Report().Counts["resumes"])
}

// An offline device is never on the broker, so an assignment to it goes nowhere and its attempt
// stays where the platform left it. Nothing is faked on its behalf.
func TestOfflineDeviceStaysSilent(t *testing.T) {
	p := newPlatform(t)
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}, Faults: otafleet.Faults{Offline: 1}}, 1)
	assert.Zero(t, p.broker.Subscribers(p.desired("dev-0")))
	p.assign("dev-0", "att1", "2.0.0", p.img, sg.sign(t, p.img))
	time.Sleep(600 * time.Millisecond)
	assert.Equal(t, ota.StateQueued, p.attempt("dev-0").State)
	d, _ := f.Device("dev-0")
	assert.False(t, d.Connected)
	assert.Equal(t, "1.0.0", d.Version)
	assert.Contains(t, d.LastError, "offline")
}

// A device that comes back online syncs, the platform resends the assignment it missed, and the
// update completes: the catch-up path a rollout relies on.
func TestOfflineDeviceCatchesUpWhenItComesOnline(t *testing.T) {
	p := newPlatform(t)
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem},
		Faults: otafleet.Faults{Offline: 1, OfflineFor: 700 * time.Millisecond}}, 1)
	p.assign("dev-0", "att1", "2.0.0", p.img, sg.sign(t, p.img))
	p.await("dev-0", ota.StateUpdated)
	assert.Equal(t, "2.0.0", p.version(f, "dev-0"))
}

// NEGATIVE CONTROL. A signature made by a key the device trusts, but over other bytes, must be
// refused, and the device must not claim the new version. If the device's verification is bypassed
// (checkSignature always accepting) the update reaches UPDATED and this test goes red; it was run
// against exactly that mutant (see the PR).
func TestForgedSignatureIsRefused(t *testing.T) {
	p := newPlatform(t)
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}}, 1)

	other := append([]byte(nil), p.img...)
	other[0] ^= 1
	forged := sg.sign(t, other) // trusted key, valid signature, but over a different digest
	p.assign("dev-0", "att1", "2.0.0", p.img, forged)

	a := p.await("dev-0", ota.StateFailed, ota.StateUpdated)
	require.Equal(t, ota.StateFailed, a.State, "a device accepted an image whose signature does not cover it")
	require.NotNil(t, a.Failure)
	assert.Equal(t, ota.CodeSignatureInvalid, a.Failure.Code)
	assert.Equal(t, "1.0.0", p.version(f, "dev-0"))
}

// A signature by a key outside the device's trust set is UNKNOWN_SIGNER, however well formed.
func TestUntrustedSignerIsRefused(t *testing.T) {
	p := newPlatform(t)
	trusted, stranger := newSigner(t), newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{trusted.pem}}, 1)
	p.assign("dev-0", "att1", "2.0.0", p.img, stranger.sign(t, p.img))
	a := p.await("dev-0", ota.StateFailed, ota.StateUpdated)
	require.Equal(t, ota.StateFailed, a.State)
	assert.Equal(t, ota.CodeUnknownSigner, a.Failure.Code)
	assert.Equal(t, "1.0.0", p.version(f, "dev-0"))
}

// A group rollout staggered by maxInFlight: this test's own loop (standing in for the platform's
// rollout, which is not under test here) assigns the next device only while fewer than maxInFlight
// attempts are open. The fleet has healthy devices and one of each fault; what this proves is that
// the fleet finishes every attempt when assignments arrive in waves, and that the tallies are what
// the injected faults say. The limit itself is the loop's, so it is not asserted.
func TestRolloutWithMaxInFlight(t *testing.T) {
	const maxInFlight = 2
	p := newPlatform(t)
	sg := newSigner(t)
	faults := otafleet.Faults{BadHash: 1, Interrupted: 1, BadSignature: 1}
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}, Faults: faults}, 8)
	sig := sg.sign(t, p.img)

	devs := make([]string, 8)
	for i := range devs {
		devs[i] = fmt.Sprintf("dev-%d", i)
	}
	next := 0
	require.Eventually(t, func() bool {
		open, ended := 0, 0
		for _, d := range devs[:next] {
			if p.attempt(d).State.Terminal() {
				ended++
			} else {
				open++
			}
		}
		for open < maxInFlight && next < len(devs) {
			p.assign(devs[next], "att-"+devs[next], "2.0.0", p.img, sig)
			next++
			open++
		}
		return ended == len(devs)
	}, 30*time.Second, 10*time.Millisecond)

	var updated, failed int
	codes := map[string]int{}
	for _, d := range devs {
		a := p.attempt(d)
		switch a.State {
		case ota.StateUpdated:
			updated++
			assert.Equal(t, "2.0.0", p.version(f, d))
		case ota.StateFailed:
			failed++
			codes[a.Failure.Code]++
			assert.Equal(t, "1.0.0", p.version(f, d), "%s failed but claims the new version", d)
		}
	}
	assert.Equal(t, 6, updated)
	assert.Equal(t, 2, failed)
	assert.Equal(t, map[string]int{"VERIFICATION_FAILED": 1, ota.CodeSignatureInvalid: 1}, codes)
	r := f.Report()
	assert.Equal(t, map[string]int{"1.0.0": 2, "2.0.0": 6}, r.Versions)
	assert.Len(t, r.Failures, 2)
}

func TestOptionsValidation(t *testing.T) {
	_, err := otafleet.New(instance, tenant, "tcp://127.0.0.1:1", nil, otafleet.Options{InitialVersion: "1.0.0"})
	require.Error(t, err, "no trust and no unsigned allowance would fail every update silently")
	_, err = otafleet.New(instance, tenant, "tcp://127.0.0.1:1", nil, otafleet.Options{AllowUnsigned: true})
	require.Error(t, err)
	_, err = otafleet.New(instance, tenant, "tcp://127.0.0.1:1", nil,
		otafleet.Options{InitialVersion: "1", AllowUnsigned: true, Faults: otafleet.Faults{BadHash: -1}})
	require.Error(t, err)
	_, err = otafleet.New(instance, tenant, "", nil, otafleet.Options{InitialVersion: "1", AllowUnsigned: true})
	require.Error(t, err)
}

func TestModeAssignmentOrder(t *testing.T) {
	f := otafleet.Faults{BadHash: 1, Interrupted: 2, BadSignature: 1, Offline: 1}
	var got []otafleet.Mode
	for i := range 7 {
		got = append(got, f.ModeFor(i))
	}
	assert.Equal(t, []otafleet.Mode{otafleet.BadHash, otafleet.Interrupted, otafleet.Interrupted,
		otafleet.BadSignature, otafleet.Offline, otafleet.Healthy, otafleet.Healthy}, got)
}

// A server that ignores Range and answers 200 with the whole object does not get a device out of
// its injected fault: a one-window image still arrives corrupted.
func TestBadHashAppliesToAServerThatIgnoresRange(t *testing.T) {
	p := newPlatform(t)
	p.ignoreRange = true
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}, ChunkSize: 1 << 20, Faults: otafleet.Faults{BadHash: 1}}, 1)
	p.assign("dev-0", "att1", "2.0.0", p.img, sg.sign(t, p.img))
	a := p.await("dev-0", ota.StateFailed, ota.StateUpdated)
	require.Equal(t, ota.StateFailed, a.State)
	assert.Equal(t, "1.0.0", p.version(f, "dev-0"))
}

// A cancel that arrives while the device is downloading is seen between steps, while the update is
// still recallable, and the device reports ABANDONED instead of finishing the install.
func TestCancelMidDownloadIsHonoured(t *testing.T) {
	p := newPlatform(t)
	sg := newSigner(t)
	f := p.fleet(otafleet.Options{TrustedKeys: []string{sg.pem}, StepDelay: 150 * time.Millisecond}, 1)
	p.assign("dev-0", "att1", "2.0.0", p.img, sg.sign(t, p.img))
	require.Eventually(t, func() bool {
		d, _ := f.Device("dev-0")
		return d.BytesHave > 0 && d.BytesHave < imgSize
	}, 10*time.Second, 10*time.Millisecond)
	require.NoError(t, p.attempts["dev-0"].RequestCancel())
	msg, err := ota.EncodeUpdateCancel("att1", "asg-att1")
	require.NoError(t, err)
	p.broker.Deliver(p.desired("dev-0"), msg)
	p.await("dev-0", ota.StateCancelled)
	assert.Equal(t, "1.0.0", p.version(f, "dev-0"))
}

// Close racing Attach (run under -race) leaves nothing behind: no panic, no WaitGroup misuse, and
// no live session.
func TestCloseDuringAttach(t *testing.T) {
	for range 20 {
		p := newPlatform(t)
		f, err := otafleet.New(instance, tenant, p.broker.URL(), nil, otafleet.Options{InitialVersion: "1.0.0", AllowUnsigned: true,
			Faults: otafleet.Faults{Offline: 1, OfflineFor: time.Millisecond}})
		require.NoError(t, err)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := range 4 {
				_ = f.Attach(context.Background(), fmt.Sprintf("dev-%d", i), "c", i)
			}
		}()
		f.Close()
		<-done
		f.Close()
	}
}

// An offline device whose first attempts to come online are refused keeps trying, with backoff,
// until it is on the broker.
func TestOfflineDeviceRetriesComingOnline(t *testing.T) {
	p := newPlatform(t)
	var mu sync.Mutex
	refusals := 0
	p.broker.Authorize = func(string, string, string) bool {
		mu.Lock()
		defer mu.Unlock()
		if refusals < 2 {
			refusals++
			return false
		}
		return true
	}
	f := p.fleet(otafleet.Options{AllowUnsigned: true, Faults: otafleet.Faults{Offline: 1, OfflineFor: 50 * time.Millisecond}}, 1)
	require.Eventually(t, func() bool { return p.broker.Subscribers(p.desired("dev-0")) == 1 }, 15*time.Second, 50*time.Millisecond)
	d, _ := f.Device("dev-0")
	assert.True(t, d.Connected)
}
