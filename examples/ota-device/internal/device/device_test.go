// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package device_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/boot"
	"devicechain.io/examples/ota-device/internal/device"
	"devicechain.io/examples/ota-device/internal/testutil"
	"devicechain.io/examples/ota-device/internal/wire"
)

func runLauncher(t *testing.T, e *env, launches int) boot.Result {
	t.Helper()
	sup := &boot.Supervisor{Root: e.root, Run: boot.ProcessRunner(e.root, nil, time.Minute, nil),
		MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := sup.Loop(ctx, launches)
	require.NoError(t, err)
	return res
}

// The whole successful path with real processes: download, verify, preflight, install, a launcher
// that really starts the new slot, and a second boot that reports what is really running.
func TestSuccessfulUpdateEndToEnd(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)

	e.run(a, "fw-101")

	assert.Equal(t, []string{"RECEIVED", "DOWNLOADING", "DOWNLOADED", "VERIFIED", "INSTALLING", "REBOOTING"}, e.rec.stages(t))
	assert.True(t, e.didReboot(), "the agent asks to be restarted")
	assert.Equal(t, boot.State{Confirmed: "a", Pending: "b"}, e.bootState())
	got, err := os.ReadFile(boot.ImagePath(e.root, "b"))
	require.NoError(t, err)
	assert.Equal(t, img.digest, verifyDigest(got), "slot b holds exactly the artifact")
	assert.Equal(t, "describe\n", e.marker("b"), "the image was run once, as describe, and nothing else")
	assert.NoFileExists(t, filepath.Join(boot.SlotDir(e.root, "b"), "ota-device.part"+exeSuffix()), "no partial file is left behind")
	installBoot := e.rec.progress(t)[len(e.rec.progress(t))-1].BootID

	// The launcher really starts slot b, which confirms itself.
	res := runLauncher(t, e, 1)
	assert.Equal(t, []string{"b"}, res.Launches)
	assert.Equal(t, boot.State{Confirmed: "b"}, e.bootState())

	// Second boot: a new process, a new bootId, the version compiled into the running binary.
	before := len(e.rec.all())
	e.agent = e.newAgent("b", "1.0.1", "boot-2")
	require.NoError(t, e.agent.OnConnect(context.Background()))

	ps := e.rec.progress(t)
	last := ps[len(ps)-1]
	assert.Equal(t, "RUNNING", string(last.Stage))
	assert.Equal(t, "1.0.1", last.Running.Version)
	assert.Equal(t, img.digest, last.Running.Digest, "the digest is of the file that runs")
	assert.Equal(t, "boot-2", last.BootID)
	assert.NotEqual(t, installBoot, last.BootID, "RUNNING comes from a boot other than the install boot")
	assert.Equal(t, []string{wire.KindInventory, wire.KindSync, wire.KindProgress}, e.rec.kinds(t)[before:],
		"the platform is told before the slot is confirmed, then RUNNING")

	// The platform's own reducer, fed the stream this device emitted, applies every report and
	// lands on UPDATED.
	final, verdicts := foldPlatform(t, e.rec, img)
	for _, v := range verdicts {
		assert.Equal(t, ota.VerdictApplied, v)
	}
	assert.Equal(t, ota.StateUpdated, final.State)
	platformDecodesEverything(t, e.rec)

	// seq rises by one with every report of the attempt.
	var seqs []uint64
	for _, p := range ps {
		seqs = append(seqs, p.Seq)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq did not rise: %v", seqs)
		}
	}

	// A replay of the same assignment after it finished changes nothing.
	n := len(e.rec.all())
	e.deliver(a)
	assert.Len(t, e.rec.all(), n)
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// A new slot that does not come up is abandoned, the old one runs again, and the device says so.
func TestFailedBootRollsBackAndIsReportedNeverAsRunning(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-102", "1.0.2", product, "crash")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.run(a, "fw-102")
	require.True(t, e.didReboot())

	res := runLauncher(t, e, 2)
	assert.Equal(t, []string{"b", "a"}, res.Launches, "the crashing slot is tried once, then the confirmed slot runs")
	assert.Equal(t, boot.State{Confirmed: "a"}, e.bootState())

	e.agent = e.newAgent("a", "1.0.0", "boot-2")
	require.NoError(t, e.agent.OnConnect(context.Background()))

	code, detail, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "ROLLED_BACK", code)
	assert.Contains(t, detail, "booted 1.0.0, expected 1.0.2")
	assert.NotContains(t, e.rec.stages(t), "RUNNING", "a rolled-back update is never reported as running")
	final, verdicts := foldPlatform(t, e.rec, img)
	for _, v := range verdicts {
		assert.Equal(t, ota.VerdictApplied, v)
	}
	assert.Equal(t, ota.StateFailed, final.State, "the platform ends the attempt FAILED, not UPDATED")
	require.NotNil(t, final.Failure)
	assert.Equal(t, "ROLLED_BACK", final.Failure.Code)
	rb, err := boot.ReadRollback(e.root)
	require.NoError(t, err)
	assert.Nil(t, rb, "the note is consumed")

	// And a further connect repeats nothing.
	n := len(e.rec.progress(t))
	require.NoError(t, e.agent.OnConnect(context.Background()))
	assert.Len(t, e.rec.progress(t), n)
}

// R10: the new image confirmed itself but the RUNNING report could not be sent. The next connect
// sends it; nothing is lost and nothing is reported twice.
func TestRunningIsRetriedIfTheLinkWasDown(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	e.run(e.publish(img, img.bytes, e.signer, nil), "fw-101")
	runLauncher(t, e, 1)

	e.agent = e.newAgent("b", "1.0.1", "boot-2")
	e.rec.failOn, e.rec.failN = `"stage":"RUNNING"`, 2 // the send and the immediate resend both fail
	require.NoError(t, e.agent.OnConnect(context.Background()))
	assert.NotContains(t, e.rec.stages(t), "RUNNING")
	assert.Equal(t, boot.State{Confirmed: "b"}, e.bootState(), "it confirmed even though the report was lost")

	require.NoError(t, e.agent.OnConnect(context.Background()))
	assert.Equal(t, 1, countOf(e.rec.stages(t), "RUNNING"))
	require.NoError(t, e.agent.OnConnect(context.Background()))
	assert.Equal(t, 1, countOf(e.rec.stages(t), "RUNNING"))
}

func countOf(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}

// A binary that is not the one the attempt installed, with no rollback note to explain it, is not
// reported as running.
func TestWrongVersionAfterRebootIsAFailureNotARunning(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	e.run(e.publish(img, img.bytes, e.signer, nil), "fw-101")
	// the device comes back still on 1.0.0 (say, the launcher was replaced and ignored the pending slot)
	e.agent = e.newAgent("a", "1.0.0", "boot-2")
	require.NoError(t, e.agent.OnConnect(context.Background()))
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "IMAGE_MISMATCH", code)
	assert.NotContains(t, e.rec.stages(t), "RUNNING")
}

// The version matches but the file that runs is not the file that was installed: RUNNING carries
// the digest of the running executable as evidence, and it must equal the attempt's.
func TestRightVersionWrongFileIsNotRunning(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	e.run(e.publish(img, img.bytes, e.signer, nil), "fw-101")
	// slot b now holds a different build that also says 1.0.1
	other := mkImage(t, "fw-101-other", "1.0.1", product, "hang")
	require.NotEqual(t, img.digest, other.digest)
	e.installImage("b", other.bytes)
	e.agent = e.newAgent("b", "1.0.1", "boot-2")
	require.NoError(t, e.agent.OnConnect(context.Background()))
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "IMAGE_MISMATCH", code)
	assert.NotContains(t, e.rec.stages(t), "RUNNING")
}

// ---------------------------------------------------------------------------------------------
// The guards. Each has a positive case (the good image passes, above) and a refusal here that
// asserts both what the platform is told AND what the device actually did with the bytes.
// ---------------------------------------------------------------------------------------------

// untouched asserts the device is exactly where it started: nothing staged, nothing in slot b.
func untouched(t *testing.T, e *env) {
	t.Helper()
	assert.Equal(t, boot.State{Confirmed: "a"}, e.bootState(), "boot state unchanged")
	assert.False(t, e.hasImage("b"), "no image was installed")
	assert.False(t, e.didReboot())
	assert.NoFileExists(t, filepath.Join(boot.SlotDir(e.root, "b"), "ota-device.part"+exeSuffix()), "the partial download was removed")
}

func TestTamperedBytesAreRefusedAndNeverExecuted(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	tampered := append([]byte(nil), img.bytes...)
	tampered[len(tampered)/2] ^= 0xff
	a := e.publish(img, tampered, e.signer, nil) // digest and signature are of the genuine image

	e.run(a, "fw-101")

	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "VERIFICATION_FAILED", code)
	assert.Equal(t, "", e.marker("b"), "the tampered image was never run, not even to ask who it is")
	untouched(t, e)
	assert.NotContains(t, e.rec.stages(t), "VERIFIED")
}

func TestSignatureFailuresAreRefusedAndNeverExecuted(t *testing.T) {
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	cases := []struct {
		name string
		mod  func(e *env, a *wire.Assignment)
		code string
	}{
		{"signed by a key this device does not trust", func(e *env, a *wire.Assignment) {
			s := testutil.NewEd25519()
			a.Signature, a.KeyID, a.Algorithm = s.Sign(img.digest), s.KeyID, s.Algorithm
		}, "UNKNOWN_SIGNER"},
		{"trusted key, signature over different bytes", func(e *env, a *wire.Assignment) {
			a.Signature = e.signer.Sign(verifyDigest([]byte("something else")))
		}, "SIGNATURE_INVALID"},
		{"trusted key id, signature by another key", func(e *env, a *wire.Assignment) {
			a.Signature = testutil.NewECDSA().Sign(img.digest)
		}, "SIGNATURE_INVALID"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			a := e.publish(img, img.bytes, e.signer, nil)
			c.mod(e, &a)
			e.run(a, "fw-101")
			code, _, ok := e.rec.failure(t)
			require.True(t, ok)
			assert.Equal(t, c.code, code)
			assert.Equal(t, "", e.marker("b"), "an unauthenticated image was never run")
			assert.NotContains(t, e.rec.stages(t), "VERIFIED")
			untouched(t, e)
		})
	}
}

func TestUnsignedAssignmentIsRefusedBeforeAnyDownload(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	e.run(e.publish(img, img.bytes, nil, nil), "fw-101")
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "UNKNOWN_SIGNER", code)
	assert.Zero(t, e.rec.count(t, wire.KindDownloadRequest))
	assert.Empty(t, e.ranges(), "nothing was fetched")
	untouched(t, e)
}

func TestWrongComponentIsRefusedBeforeAnyDownload(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	e.run(e.publish(img, img.bytes, e.signer, func(a *wire.Assignment) { a.Component = "bootloader" }), "fw-101")
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "WRONG_COMPONENT", code)
	assert.Zero(t, e.rec.count(t, wire.KindDownloadRequest))
	assert.Empty(t, e.ranges())
	untouched(t, e)
}

func TestDowngradeAndReplayAreRefusedEvenWhenCorrectlySigned(t *testing.T) {
	for _, v := range []string{"0.9.0", "1.0.0", "1.0.0-rc.1"} {
		t.Run(v, func(t *testing.T) {
			e := newEnv(t)
			old := mkImage(t, "fw-old", v, product, "good")
			e.run(e.publish(old, old.bytes, e.signer, nil), "fw-old") // a perfectly valid signature
			code, detail, ok := e.rec.failure(t)
			require.True(t, ok)
			assert.Equal(t, "DOWNGRADE_REFUSED", code)
			assert.Contains(t, detail, "not newer")
			assert.Zero(t, e.rec.count(t, wire.KindDownloadRequest))
			assert.Empty(t, e.ranges())
			untouched(t, e)
		})
	}
}

func TestAnImageThatLiesAboutItselfIsRefusedAndOnlyEverAsked(t *testing.T) {
	cases := map[string]image{
		"wrong product": mkImage(t, "liar-prod", "1.0.3", "some-other-product", "good"),
		"wrong version": mkImage(t, "liar-ver", "9.9.9", product, "good"), // says 9.9.9, the assignment says 1.0.3
	}
	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			a := e.publish(img, img.bytes, e.signer, func(a *wire.Assignment) { a.Artifact.Version = "1.0.3" })
			e.run(a, img.name)
			code, _, ok := e.rec.failure(t)
			require.True(t, ok)
			assert.Equal(t, "IMAGE_MISMATCH", code)
			assert.Equal(t, "describe\n", e.marker("b"), "the image was started exactly once, as describe")
			assert.Contains(t, e.rec.stages(t), "VERIFIED", "it had passed integrity and authenticity, which is why it could be asked")
			assert.NotContains(t, e.rec.stages(t), "INSTALLING")
			untouched(t, e)
		})
	}
}

// T3: nothing executes before the digest and the signature have passed. The positive control is
// that the good image DOES leave a marker once they pass (TestSuccessfulUpdateEndToEnd).
func TestNothingRunsBeforeIntegrityAndAuthenticity(t *testing.T) {
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	t.Run("bad digest", func(t *testing.T) {
		e := newEnv(t)
		bad := append([]byte(nil), img.bytes...)
		bad[0] ^= 1
		e.run(e.publish(img, bad, e.signer, nil), "fw-101")
		assert.Equal(t, "", e.marker("b"))
	})
	t.Run("bad signature", func(t *testing.T) {
		e := newEnv(t)
		e.run(e.publish(img, img.bytes, e.signer, func(a *wire.Assignment) { a.Signature = testutil.NewECDSA().Sign(img.digest) }), "fw-101")
		assert.Equal(t, "", e.marker("b"))
	})
	t.Run("positive control: a good image is run, once", func(t *testing.T) {
		e := newEnv(t)
		e.run(e.publish(img, img.bytes, e.signer, nil), "fw-101")
		assert.Equal(t, "describe\n", e.marker("b"))
	})
}

// ---------------------------------------------------------------------------------------------
// Download behaviour as the agent drives it.
// ---------------------------------------------------------------------------------------------

func TestInterruptedDownloadResumesFromTheBytesHeld(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	cut := len(img.bytes) * 4 / 10
	e.cutFirst["fw-101"] = cut

	e.deliver(a)
	e.deliver(e.grantFor(a, "fw-101")) // dies at ~40%
	assert.Equal(t, 2, e.rec.count(t, wire.KindDownloadRequest), "the agent asked for a fresh grant to carry on")
	assert.NotContains(t, e.rec.stages(t), "DOWNLOADED")

	e.deliver(e.grantFor(a, "fw-101"))
	assert.Equal(t, []string{"", "bytes=" + strconv.Itoa(cut) + "-"}, e.ranges(), "the second request resumed at the offset held")
	assert.Contains(t, e.rec.stages(t), "REBOOTING")
	got, _ := os.ReadFile(boot.ImagePath(e.root, "b"))
	assert.Equal(t, img.digest, verifyDigest(got))
}

// N4 in miniature: a flipped byte in the half-downloaded file is caught by the digest, after the
// resume, and the image is never run.
func TestCorruptPartialIsCaughtAfterResume(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.cutFirst["fw-101"] = len(img.bytes) / 2
	e.deliver(a)
	e.deliver(e.grantFor(a, "fw-101"))

	part := filepath.Join(boot.SlotDir(e.root, "b"), "ota-device.part"+exeSuffix())
	b, err := os.ReadFile(part)
	require.NoError(t, err)
	b[10] ^= 0xff
	require.NoError(t, os.WriteFile(part, b, 0o600))

	e.deliver(e.grantFor(a, "fw-101"))
	assert.Len(t, e.ranges(), 2, "the resume was a ranged request")
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "VERIFICATION_FAILED", code)
	assert.Equal(t, "", e.marker("b"))
	untouched(t, e)
}

func TestRestartResumesTheSameAttempt(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.cutFirst["fw-101"] = len(img.bytes) / 3
	e.deliver(a)
	e.deliver(e.grantFor(a, "fw-101"))
	received := countOf(e.rec.stages(t), "RECEIVED")
	lastSeq := e.rec.progress(t)[len(e.rec.progress(t))-1].Seq

	// the process dies and restarts; the platform resends the assignment after the device's sync
	e.agent = e.newAgent("a", "1.0.0", "boot-2")
	require.NoError(t, e.agent.OnConnect(context.Background()))
	e.deliver(a)
	assert.Equal(t, received, countOf(e.rec.stages(t), "RECEIVED"), "the attempt is resumed, not started again")
	e.deliver(e.grantFor(a, "fw-101"))

	ps := e.rec.progress(t)
	assert.Equal(t, "REBOOTING", string(ps[len(ps)-1].Stage))
	assert.Greater(t, ps[len(ps)-1].Seq, lastSeq, "seq carries on across the restart")
	assert.Equal(t, "boot-2", ps[len(ps)-1].BootID)
}

func TestSizeMismatchIsAFailure(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, append(append([]byte(nil), img.bytes...), 'x'), e.signer, nil) // one byte more than declared
	e.run(a, "fw-101")
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "SIZE_MISMATCH", code)
	assert.Equal(t, "", e.marker("b"))
	untouched(t, e)
}

func TestGivesUpAfterBoundedGrantAttempts(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.status["fw-101"] = 503
	e.deliver(a)
	for i := 0; i < 6; i++ {
		e.deliver(e.grantFor(a, "fw-101"))
	}
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "DOWNLOAD_FAILED", code)
	assert.LessOrEqual(t, len(e.ranges()), 3, "bounded")
	untouched(t, e)
}

func TestCancelBeforeInstallAbandonsAndAfterInstallIsIgnored(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		e := newEnv(t)
		img := mkImage(t, "fw-101", "1.0.1", product, "good")
		a := e.publish(img, img.bytes, e.signer, nil)
		e.cutFirst["fw-101"] = len(img.bytes) / 2
		e.deliver(a)
		e.deliver(e.grantFor(a, "fw-101")) // leaves a partial file
		e.deliver(wire.Cancel{V: 1, Kind: wire.KindCancel, AttemptID: a.AttemptID, AssignmentID: a.AssignmentID})
		ps := e.rec.stages(t)
		assert.Equal(t, "ABANDONED", ps[len(ps)-1])
		untouched(t, e)
	})
	t.Run("after", func(t *testing.T) {
		e := newEnv(t)
		img := mkImage(t, "fw-101", "1.0.1", product, "good")
		a := e.publish(img, img.bytes, e.signer, nil)
		e.run(a, "fw-101")
		n := len(e.rec.all())
		e.deliver(wire.Cancel{V: 1, Kind: wire.KindCancel, AttemptID: a.AttemptID, AssignmentID: a.AssignmentID})
		assert.Len(t, e.rec.all(), n, "an update that has begun installing cannot be recalled")
		assert.Equal(t, boot.State{Confirmed: "a", Pending: "b"}, e.bootState())
	})
}

// T7: the grant-origin pin.
func TestGrantOriginPin(t *testing.T) {
	https, _ := url.Parse("https://dc.example.com")
	httpO, _ := url.Parse("http://127.0.0.1:8080")
	https8443, _ := url.Parse("https://dc.example.com:8443")
	ok := "/api/update-management/updates/download/fw?cap=x"
	cases := []struct {
		name     string
		origin   *url.URL
		insecure bool
		grant    string
		allowed  bool
	}{
		{"same origin", https, false, "https://dc.example.com" + ok, true},
		{"host is case-insensitive", https, false, "https://DC.Example.COM" + ok, true},
		{"explicit default port", https, false, "https://dc.example.com:443" + ok, true},
		{"another host", https, false, "https://evil.example.net" + ok, false},
		{"a suffix of the host", https, false, "https://dc.example.com.evil.net" + ok, false},
		{"same host, another port", https, false, "https://dc.example.com:8443" + ok, false},
		{"another scheme", https, false, "http://dc.example.com" + ok, false},
		{"userinfo trick", https, false, "https://dc.example.com@evil.example.net" + ok, false},
		{"userinfo with the real host", https, false, "https://user:pw@dc.example.com" + ok, false},
		{"not an update download path", https, false, "https://dc.example.com/admin/secrets?cap=x", false},
		{"plain http refused by default", httpO, false, "http://127.0.0.1:8080" + ok, false},
		{"plain http accepted when told", httpO, true, "http://127.0.0.1:8080" + ok, true},
		{"plain http, another port", httpO, true, "http://127.0.0.1:9090" + ok, false},
		{"https origin, http grant on the same explicit port, insecure allowed", https8443, true, "http://dc.example.com:8443" + ok, false},
		{"ftp", https, false, "ftp://dc.example.com" + ok, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := device.CheckGrantURL(c.origin, c.insecure, c.grant)
			if c.allowed {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// The pin at the agent: a grant for another host is a failure, and nothing is ever requested from it.
func TestGrantForAnotherHostIsNeverFetched(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.deliver(a)
	g := e.grantFor(a, "fw-101")
	g.URL = "http://127.0.0.2:1" + downloadAt + "fw-101?cap=" + capSecret
	e.deliver(g)
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "GRANT_REJECTED", code)
	assert.Empty(t, e.ranges())
	untouched(t, e)
}

func TestCapabilityNeverAppearsInAReport(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.cutFirst["fw-101"] = len(img.bytes) / 2
	e.run(a, "fw-101")
	for _, f := range e.rec.all() {
		assert.NotContains(t, string(f), capSecret)
	}
}

// ---------------------------------------------------------------------------------------------
// Robustness of what the agent reads.
// ---------------------------------------------------------------------------------------------

func TestGarbageOnDesiredIsDroppedWithoutEffect(t *testing.T) {
	e := newEnv(t)
	for _, m := range []string{`{`, `[]`, `{"v":1,"kind":"update.assignment"}`, `{"v":1,"kind":"update.assignment","extra":1}`, `{"v":1,"kind":"download.grant","assignmentId":"x","url":"https://h/","expiresAt":"nope"}`} {
		e.agent.Handle(context.Background(), []byte(m))
	}
	assert.Empty(t, e.rec.all())
}

func TestCorruptAttemptFileReportsNothing(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(e.root, "attempt.json"), []byte("{corrupt"), 0o600))
	e.agent = e.newAgent("a", "1.0.0", "boot-2")
	require.NoError(t, e.agent.OnConnect(context.Background()))
	assert.Equal(t, []string{wire.KindInventory, wire.KindSync}, e.rec.kinds(t), "no progress report for an attempt it cannot read")
}

func TestStartupSendsInventoryThenSync(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.agent.OnConnect(context.Background()))
	assert.Equal(t, []string{wire.KindInventory, wire.KindSync}, e.rec.kinds(t))
	var inv struct {
		Running struct{ Version string }
		BootID  string
	}
	require.NoError(t, json.Unmarshal(e.rec.all()[0], &inv))
	assert.Equal(t, "1.0.0", inv.Running.Version, "the version compiled into the binary")
	assert.Equal(t, "boot-1", inv.BootID)
}

// Every kind of report the contract lists as valid is one this device actually emits when driven.
func TestDrivenRunsEmitEveryFixtureKind(t *testing.T) {
	fx, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "backend", "core", "ota", "testdata", "reports", "valid", "*.json"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(fx), 10)
	var want []string
	for _, f := range fx {
		want = append(want, strings.TrimSuffix(filepath.Base(f), ".json"))
	}

	seen := map[string]bool{}
	note := func(r *recorder) {
		for _, f := range r.all() {
			if k, err := wire.CheckOutbound(f); err == nil && k == wire.KindInventory {
				seen["inventory"] = true
			}
		}
		for _, s := range r.stages(t) {
			seen[strings.ToLower(s)] = true
		}
	}

	good := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	good.run(good.publish(img, img.bytes, good.signer, nil), "fw-101")
	runLauncher(t, good, 1)
	good.agent = good.newAgent("b", "1.0.1", "boot-2")
	require.NoError(t, good.agent.OnConnect(context.Background()))
	note(good.rec)

	bad := newEnv(t)
	bad.run(bad.publish(img, img.bytes, nil, nil), "fw-101")
	note(bad.rec)

	ab := newEnv(t)
	a := ab.publish(img, img.bytes, ab.signer, nil)
	ab.cutFirst["fw-101"] = 1000
	ab.run(a, "fw-101")
	ab.deliver(wire.Cancel{V: 1, Kind: wire.KindCancel, AssignmentID: a.AssignmentID})
	note(ab.rec)

	var got []string
	for k := range seen {
		got = append(got, k)
	}
	assert.ElementsMatch(t, want, got)
}

func TestNewRequiresTheConfigurationThatMakesItSafe(t *testing.T) {
	_, err := device.New(device.Config{Root: t.TempDir(), RequireSigned: true, Origin: &url.URL{Scheme: "https", Host: "h"}}, &recorder{})
	assert.Error(t, err, "signatures required with no trust set")
	_, err = device.New(device.Config{Root: t.TempDir()}, &recorder{})
	assert.Error(t, err, "no origin to pin grants to")
}
