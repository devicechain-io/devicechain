// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package device is the payload's update agent: it reads the platform's assignments, downloads
// the image, decides whether it may be run, installs it into the inactive slot and, on the next
// boot, reports what is really running.
//
// The order of the checks on a downloaded image is the security property of this package, and it
// is deliberate and fixed:
//
//  1. the digest of the bytes held equals the assignment's  (nothing is executed)
//  2. a signature by a key this device already trusts verifies  (nothing is executed)
//  3. only then is the image run, once, as `describe`, and it must say it is this device's
//     product and the version the assignment named
//
// No image executes before the first two have passed. TestNothingRunsBeforeIntegrityAndAuthenticity
// holds that order.
package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"devicechain.io/examples/ota-device/internal/boot"
	"devicechain.io/examples/ota-device/internal/download"
	"devicechain.io/examples/ota-device/internal/fsutil"
	"devicechain.io/examples/ota-device/internal/verify"
	"devicechain.io/examples/ota-device/internal/wire"
)

// Codes this device reports beyond the ones in verify and wire.
const (
	CodeRolledBack       = "ROLLED_BACK"
	CodeSizeMismatch     = "SIZE_MISMATCH"
	CodeDownloadFailed   = "DOWNLOAD_FAILED"
	CodeGrantRejected    = "GRANT_REJECTED"
	CodeNoLauncher       = "NO_LAUNCHER"
	CodeNoSpace          = "NO_SPACE"
	CodeInstallFailed    = "INSTALL_FAILED"
	diskHeadroomBytes    = 1 << 20
	downloadPathFragment = "/updates/download/"
)

// Sender publishes one frame on this device's device-reports topic.
type Sender interface {
	Send(payload []byte) error
}

// Config is everything the agent needs from its surroundings.
type Config struct {
	Root      string // state directory (boot.json, attempt.json, slots/)
	Slot      string // the slot this process runs from ("" when run without the launcher)
	Component string
	Product   string
	Version   string // compiled into the running binary
	BootID    string // minted once per process start

	Trust          *verify.TrustSet
	RequireSigned  bool
	AllowDowngrade bool

	Origin        *url.URL // the instance origin download grants must point at
	AllowInsecure bool     // permit an http download (lab use only)
	HTTP          *http.Client

	PreflightTimeout time.Duration
	ProgressEvery    time.Duration
	GrantAttempts    int
	RetryDelay       time.Duration
	Sleep            func(context.Context, time.Duration)
	Now              func() time.Time
	SelfPath         string // the running executable, hashed to prove what runs
	Async            bool   // run a grant's download on its own goroutine
	Reboot           func() // called once the new slot is staged
	Log              func(format string, args ...any)
}

// attempt is attempt.json: the one update this device is working on.
type attempt struct {
	AttemptID      string `json:"attemptId"`
	AssignmentID   string `json:"assignmentId"`
	Component      string `json:"component"`
	Version        string `json:"version"`
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	Trust          string `json:"trust"`
	Signature      string `json:"signature,omitempty"`
	KeyID          string `json:"keyId,omitempty"`
	Algorithm      string `json:"algorithm,omitempty"`
	RequiresReboot bool   `json:"requiresReboot"`
	Seq            uint64 `json:"seq"`
	Stage          string `json:"stage"`
}

// Agent is the update agent.
type Agent struct {
	cfg  Config
	send Sender

	mu       sync.Mutex
	at       *attempt
	busy     bool
	cancelDL context.CancelFunc
	asks     int
	lastProg time.Time
}

// New builds an agent and loads the attempt it was working on, if any. An attempt file that cannot
// be read is ignored (and logged) rather than guessed at: the agent reports nothing for it.
func New(cfg Config, send Sender) (*Agent, error) {
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	if cfg.PreflightTimeout == 0 {
		cfg.PreflightTimeout = 5 * time.Second
	}
	if cfg.ProgressEvery == 0 {
		cfg.ProgressEvery = time.Second
	}
	if cfg.GrantAttempts == 0 {
		cfg.GrantAttempts = 5
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.Origin == nil {
		return nil, errors.New("device: an instance origin is required to pin download grants")
	}
	if cfg.Trust == nil && cfg.RequireSigned {
		return nil, errors.New("device: signatures are required but the trust set is empty")
	}
	a := &Agent{cfg: cfg, send: send}
	b, err := os.ReadFile(a.attemptPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		cfg.Log("attempt state unreadable, ignoring it: %v", err)
	default:
		var at attempt
		if err := json.Unmarshal(b, &at); err != nil || wire.StageRank(at.Stage) == 0 {
			cfg.Log("attempt state corrupt, ignoring it")
		} else {
			a.at = &at
		}
	}
	return a, nil
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (a *Agent) attemptPath() string { return filepath.Join(a.cfg.Root, "attempt.json") }

func (a *Agent) inactive() string {
	if a.cfg.Slot == "" {
		return ""
	}
	return boot.Other(a.cfg.Slot)
}

func (a *Agent) partPath() string {
	return filepath.Join(boot.SlotDir(a.cfg.Root, a.inactive()), "ota-device.part"+fsutil.ExeSuffix())
}

func (a *Agent) removePart() {
	if a.inactive() != "" {
		_ = os.Remove(a.partPath())
	}
}

func (a *Agent) saveLocked() error {
	b, err := json.Marshal(a.at)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(a.attemptPath(), b, 0o600)
}

// reportLocked sends one update.progress for the current attempt. The sequence number is made
// durable BEFORE the frame is sent, so a crash can only ever skip a number, never reuse one for a
// different report. The stage is committed only once the frame has gone out, so a RUNNING that
// could not be sent is sent again on the next connect.
func (a *Agent) reportLocked(stage string, mod func(*wire.Report)) error {
	at := a.at
	at.Seq++
	if err := a.saveLocked(); err != nil {
		return err
	}
	r := wire.Report{
		V: wire.Version, Kind: wire.KindProgress, AttemptID: at.AttemptID, AssignmentID: at.AssignmentID,
		Component: at.Component, ArtifactDigest: at.Digest, Seq: at.Seq, Stage: stage, BootID: a.cfg.BootID,
	}
	if mod != nil {
		mod(&r)
	}
	b, err := wire.Encode(r)
	if err != nil {
		return err
	}
	if err := a.send.Send(b); err != nil {
		return err
	}
	at.Stage = stage
	return a.saveLocked()
}

func (a *Agent) failLocked(code, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	if len(detail) > 500 {
		detail = detail[:500]
	}
	a.cfg.Log("update %s failed: %s: %s", a.at.AttemptID, code, detail)
	a.removePart()
	if err := a.reportLocked(wire.StageFailed, func(r *wire.Report) {
		r.Result = &wire.Result{Code: code, Detail: detail}
	}); err != nil {
		a.cfg.Log("could not report the failure: %v", err)
	}
}

func (a *Agent) failVerifyLocked(err error) {
	var f *verify.Failure
	if errors.As(err, &f) {
		a.failLocked(f.Code, "%s", f.Detail)
		return
	}
	a.failLocked(CodeInstallFailed, "%v", err)
}

func (a *Agent) sendFrame(v any) error {
	b, err := wire.Encode(v)
	if err != nil {
		return err
	}
	return a.send.Send(b)
}

func (a *Agent) requestGrantLocked() error {
	return a.sendFrame(wire.DownloadRequest{V: wire.Version, Kind: wire.KindDownloadRequest, AssignmentID: a.at.AssignmentID})
}

// OnConnect runs on every (re)connect, after the subscription to device-desired is in place. It
// settles whatever the last boot left undecided, tells the platform what really runs, and syncs.
//
//  1. a rollback note from the launcher: the update the platform is waiting for did not arrive,
//     so report it FAILED/ROLLED_BACK;
//  2. if this slot is the pending one, it has come up healthy (it is connected), so confirm it,
//     and report RUNNING with the version compiled into THIS binary and the digest of THIS file;
//  3. an inventory, which is how an update the platform marked UNKNOWN is resolved;
//  4. the sync, which makes the platform resend anything this device should still be doing.
func (a *Agent) OnConnect(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	inFlight := func() bool {
		return a.at != nil && !wire.Terminal(a.at.Stage) && wire.StageRank(a.at.Stage) >= wire.StageRank(wire.StageInstalling)
	}

	rb, err := boot.ReadRollback(a.cfg.Root)
	if err != nil {
		a.cfg.Log("rollback note unreadable: %v", err)
	}
	if rb != nil {
		if inFlight() {
			a.failLocked(CodeRolledBack, "booted %s, expected %s: %s", a.cfg.Version, a.at.Version, rb.Reason)
		}
		if err := boot.ClearRollback(a.cfg.Root); err != nil {
			a.cfg.Log("could not clear the rollback note: %v", err)
		}
	}

	if a.cfg.Slot != "" {
		if changed, err := boot.Confirm(a.cfg.Root, a.cfg.Slot); err != nil {
			a.cfg.Log("could not confirm slot %s: %v", a.cfg.Slot, err)
		} else if changed {
			a.cfg.Log("slot %s is healthy and confirmed", a.cfg.Slot)
		}
	}

	if inFlight() {
		self, _, err := verify.FileDigest(a.selfPath())
		switch {
		case err != nil:
			a.cfg.Log("cannot hash the running image: %v", err)
		case a.cfg.Version == a.at.Version && self == a.at.Digest:
			if err := a.reportLocked(wire.StageRunning, func(r *wire.Report) {
				r.Running = &wire.Running{Version: a.cfg.Version, Digest: self}
			}); err != nil {
				a.cfg.Log("could not report RUNNING (will retry on the next connect): %v", err)
			}
		default:
			a.failLocked(verify.CodeImageMismatch, "running %s (%s), the update was for %s (%s)", a.cfg.Version, self, a.at.Version, a.at.Digest)
		}
	}

	if err := a.sendFrame(wire.Report{
		V: wire.Version, Kind: wire.KindInventory, Component: a.cfg.Component,
		Running: &wire.Running{Version: a.cfg.Version}, BootID: a.cfg.BootID,
	}); err != nil {
		return fmt.Errorf("inventory: %w", err)
	}
	return a.sendFrame(wire.Sync{V: wire.Version, Kind: wire.KindSync, BootID: a.cfg.BootID})
}

func (a *Agent) selfPath() string {
	if a.cfg.SelfPath != "" {
		return a.cfg.SelfPath
	}
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// Handle processes one message read from device-desired. A message that does not decode is
// dropped and logged; it never reaches the agent's state.
func (a *Agent) Handle(ctx context.Context, payload []byte) {
	m, err := wire.DecodeDesired(payload)
	if err != nil {
		a.cfg.Log("dropping a message that does not decode: %v", err)
		return
	}
	switch m.Kind {
	case wire.KindAssignment:
		a.OnAssignment(ctx, *m.Assignment)
	case wire.KindGrant:
		if a.cfg.Async {
			go a.OnGrant(ctx, *m.Grant)
			return
		}
		a.OnGrant(ctx, *m.Grant)
	case wire.KindCancel:
		a.OnCancel(*m.Cancel)
	}
}

// OnAssignment accepts, resumes or refuses an assignment.
func (a *Agent) OnAssignment(ctx context.Context, asg wire.Assignment) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if at := a.at; at != nil {
		if at.AttemptID == asg.AttemptID {
			if wire.Terminal(at.Stage) || wire.StageRank(at.Stage) >= wire.StageRank(wire.StageInstalling) {
				return // done, or past the point of recall
			}
			if !a.busy { // a resend after a reconnect or a restart: carry on from the bytes held
				if err := a.requestGrantLocked(); err != nil {
					a.cfg.Log("download.request: %v", err)
				}
			}
			return
		}
		if !wire.Terminal(at.Stage) && wire.StageRank(at.Stage) >= wire.StageRank(wire.StageInstalling) {
			return // installing something else: it cannot be recalled
		}
		if a.cancelDL != nil {
			a.cancelDL()
		}
		a.removePart()
	}

	a.at = &attempt{
		AttemptID: asg.AttemptID, AssignmentID: asg.AssignmentID, Component: asg.Component,
		Version: asg.Artifact.Version, Digest: asg.Artifact.Digest, Size: asg.Artifact.Size,
		Trust: asg.Trust, Signature: asg.Signature, KeyID: asg.KeyID, Algorithm: asg.Algorithm,
		RequiresReboot: asg.Artifact.RequiresReboot, Stage: "",
	}
	a.asks = 0
	if err := a.saveLocked(); err != nil {
		a.cfg.Log("cannot persist the attempt, refusing it: %v", err)
		a.at = nil
		return
	}
	if err := a.reportLocked(wire.StageReceived, nil); err != nil {
		a.cfg.Log("RECEIVED: %v", err)
		return
	}

	pol := verify.Policy{Component: a.cfg.Component, Current: a.cfg.Version, RequireSigned: a.cfg.RequireSigned, AllowDowngrade: a.cfg.AllowDowngrade}
	if err := pol.CheckPolicy(asg); err != nil {
		a.failVerifyLocked(err)
		return
	}
	if a.cfg.Slot == "" {
		a.failLocked(CodeNoLauncher, "this process was not started by the launcher, so there is no slot to install into")
		return
	}
	if err := os.MkdirAll(boot.SlotDir(a.cfg.Root, a.inactive()), 0o700); err != nil {
		a.failLocked(CodeInstallFailed, "%v", err)
		return
	}
	held := int64(0)
	if fi, err := os.Stat(a.partPath()); err == nil {
		held = fi.Size()
	}
	if free, err := fsutil.FreeBytes(boot.SlotDir(a.cfg.Root, a.inactive())); err == nil {
		if need := uint64(asg.Artifact.Size-min(held, asg.Artifact.Size)) + diskHeadroomBytes; free < need {
			a.failLocked(CodeNoSpace, "need %d bytes, %d free", need, free)
			return
		}
	}
	if err := a.requestGrantLocked(); err != nil {
		a.cfg.Log("download.request: %v", err)
	}
}

// CheckGrantURL is the grant-origin pin. A grant is a URL chosen by the platform, delivered over a
// broker; this device fetches only from the origin it was configured with, over the scheme it was
// configured with, and never from a URL that smuggles another host in through userinfo.
func CheckGrantURL(origin *url.URL, allowInsecure bool, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("grant url does not parse")
	}
	if u.User != nil {
		return errors.New("grant url carries userinfo")
	}
	if u.Scheme != origin.Scheme {
		return fmt.Errorf("grant scheme %q is not the configured %q", u.Scheme, origin.Scheme)
	}
	if u.Scheme == "http" && !allowInsecure {
		return errors.New("grant is plain http and this device was not told to allow that")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("grant is neither https nor http")
	}
	if !strings.EqualFold(u.Hostname(), origin.Hostname()) || portOf(u) != portOf(origin) {
		return fmt.Errorf("grant host %q is not the configured instance origin", u.Host)
	}
	if !strings.Contains(u.Path, downloadPathFragment) {
		return errors.New("grant path is not an update download")
	}
	return nil
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// OnGrant downloads, verifies and installs.
func (a *Agent) OnGrant(ctx context.Context, g wire.Grant) {
	a.mu.Lock()
	at := a.at
	if at == nil || at.AssignmentID != g.AssignmentID || a.busy ||
		(at.Stage != wire.StageReceived && at.Stage != wire.StageDownloading) {
		a.mu.Unlock()
		return
	}
	if err := CheckGrantURL(a.cfg.Origin, a.cfg.AllowInsecure, g.URL); err != nil {
		a.failLocked(CodeGrantRejected, "%v", err)
		a.mu.Unlock()
		return
	}
	if exp, _ := g.Expiry(); !exp.After(a.cfg.Now()) {
		a.askAgainLocked(ctx, "the grant had already expired")
		a.mu.Unlock()
		return
	}
	a.busy = true
	dlCtx, cancel := context.WithCancel(ctx)
	a.cancelDL = cancel
	held := int64(0)
	if fi, err := os.Stat(a.partPath()); err == nil {
		held = fi.Size()
	}
	if err := a.reportLocked(wire.StageDownloading, func(r *wire.Report) {
		r.Progress = &wire.Progress{Bytes: min(held, at.Size), Total: at.Size}
	}); err != nil {
		a.cfg.Log("DOWNLOADING: %v", err)
	}
	a.lastProg = a.cfg.Now()
	a.mu.Unlock()

	ferr := download.Fetch(dlCtx, g.URL, download.Options{
		Client: a.cfg.HTTP, Size: at.Size, Part: a.partPath(),
		OnBytes: func(n int64) { a.progress(at, n) },
	})
	cancel()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.busy = false
	a.cancelDL = nil
	if a.at != at || wire.Terminal(at.Stage) || ctx.Err() != nil {
		return // cancelled, superseded, or shutting down
	}
	switch {
	case ferr == nil:
	case errors.Is(ferr, download.ErrSizeMismatch):
		a.failLocked(CodeSizeMismatch, "%v", ferr)
		return
	default:
		a.cfg.Log("download: %v", ferr)
		a.askAgainLocked(ctx, ferr.Error())
		return
	}

	if err := a.reportLocked(wire.StageDownloaded, nil); err != nil {
		a.cfg.Log("DOWNLOADED: %v", err)
	}
	a.verifyAndInstallLocked(ctx)
}

// askAgainLocked asks for a fresh grant, up to a bound, pausing between tries. It must be called
// with the lock held and returns with it held. It reports DOWNLOAD_FAILED when the bound is hit.
func (a *Agent) askAgainLocked(ctx context.Context, why string) bool {
	at := a.at
	a.asks++
	if a.asks >= a.cfg.GrantAttempts {
		a.failLocked(CodeDownloadFailed, "gave up after %d grants: %s", a.asks, why)
		return false
	}
	a.mu.Unlock()
	a.cfg.Sleep(ctx, a.cfg.RetryDelay)
	a.mu.Lock()
	if a.at != at || wire.Terminal(at.Stage) || ctx.Err() != nil {
		return false
	}
	if err := a.requestGrantLocked(); err != nil {
		a.cfg.Log("download.request: %v", err)
	}
	return true
}

func (a *Agent) progress(at *attempt, held int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.at != at || wire.Terminal(at.Stage) || at.Stage != wire.StageDownloading {
		return
	}
	now := a.cfg.Now()
	if now.Sub(a.lastProg) < a.cfg.ProgressEvery || held >= at.Size {
		return
	}
	a.lastProg = now
	if err := a.reportLocked(wire.StageDownloading, func(r *wire.Report) {
		r.Progress = &wire.Progress{Bytes: held, Total: at.Size}
	}); err != nil {
		a.cfg.Log("progress: %v", err)
	}
}

// verifyAndInstallLocked is the ordered gate described in the package comment.
func (a *Agent) verifyAndInstallLocked(ctx context.Context) {
	at := a.at
	part := a.partPath()

	// 1. integrity. Nothing has been executed.
	if err := verify.CheckDigest(part, at.Digest); err != nil {
		a.failVerifyLocked(err)
		return
	}
	// 2. authenticity, against keys this device already holds. Nothing has been executed.
	if at.Trust == wire.TrustSigned {
		if err := a.cfg.Trust.VerifySignature(at.Digest, at.KeyID, at.Algorithm, at.Signature); err != nil {
			a.failVerifyLocked(err)
			return
		}
	} else if a.cfg.RequireSigned {
		a.failLocked(wire.CodeUnknownSigner, "this device requires a signed image")
		return
	}
	if err := a.reportLocked(wire.StageVerified, nil); err != nil {
		a.cfg.Log("VERIFIED: %v", err)
	}
	// 3. identity. The image is run for the first time, and only because 1 and 2 passed.
	id, err := verify.Describe(ctx, part, a.cfg.PreflightTimeout)
	if err != nil {
		a.failLocked(verify.CodeImageMismatch, "preflight: %v", err)
		return
	}
	if err := verify.CheckIdentity(id, a.cfg.Product, at.Version); err != nil {
		a.failVerifyLocked(err)
		return
	}

	if err := a.reportLocked(wire.StageInstalling, nil); err != nil {
		a.cfg.Log("INSTALLING: %v", err)
	}
	target := boot.ImagePath(a.cfg.Root, a.inactive())
	_ = os.Remove(target) // Windows will not rename over an existing file by every route
	if err := os.Rename(part, target); err != nil {
		a.failLocked(CodeInstallFailed, "%v", err)
		return
	}
	if err := fsutil.SyncDir(filepath.Dir(target)); err != nil {
		a.failLocked(CodeInstallFailed, "%v", err)
		return
	}
	if err := boot.Stage(a.cfg.Root, a.inactive()); err != nil {
		a.failLocked(CodeInstallFailed, "%v", err)
		return
	}
	if err := a.reportLocked(wire.StageRebooting, nil); err != nil {
		a.cfg.Log("REBOOTING: %v", err)
	}
	if a.cfg.Reboot != nil {
		a.cfg.Reboot()
	}
}

// OnCancel abandons an assignment that has not begun installing. Once installation has started an
// update cannot be recalled, and the cancel is ignored.
func (a *Agent) OnCancel(c wire.Cancel) {
	a.mu.Lock()
	defer a.mu.Unlock()
	at := a.at
	if at == nil || at.AssignmentID != c.AssignmentID || (c.AttemptID != "" && c.AttemptID != at.AttemptID) {
		return
	}
	if wire.Terminal(at.Stage) || wire.StageRank(at.Stage) >= wire.StageRank(wire.StageInstalling) {
		return
	}
	if a.cancelDL != nil {
		a.cancelDL()
	}
	a.removePart()
	if err := a.reportLocked(wire.StageAbandoned, nil); err != nil {
		a.cfg.Log("ABANDONED: %v", err)
	}
}
