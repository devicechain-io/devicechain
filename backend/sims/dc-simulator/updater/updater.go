// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package updater is a fake device-side firmware updater for the simulator: the device half of
// the update contract, with the faults a real device has built in so the platform half can be
// proven against them.
//
// It is a test instrument for upcoming OTA support. Nothing here is wired to a broker or to any
// service; the transport is an interface the caller supplies.
//
// # What it does
//
// Given an assignment it walks the update stages — received, downloading, downloaded, verified,
// installing, rebooting, running — emitting one report per step through a Transport. It speaks
// the wire format from its OWN structs (wire.go), never the platform's types, so the golden
// fixtures in the core contract package are a cross-language check on it (mirror_test.go).
//
// It persists its local attempt to a JSON file (fsync + rename) before every send, so a process
// that dies and is reopened on the same file continues the same attempt: same attemptId, the
// sequence number continuing, the download resuming from the bytes it already holds. A report
// that was decided but not confirmed sent is resent byte-for-byte; the platform's duplicate
// handling is what makes that safe, and a test relies on exactly that.
//
// # Faults
//
//   - disconnects: the Transport (or Source) returns an error; the step makes no progress and is
//     retried;
//   - duplicate reports: Faults.DuplicateNext, and a crash after a send but before the device
//     recorded it;
//   - failed verification: Faults.FailVerification, or a Source that serves corrupt bytes;
//   - a reboot that never confirms: Faults.NoConfirmAfterReboot (device boots, says nothing), and
//     Faults.BootOldImage (device boots its previous image);
//   - restarts: Faults.Crash kills the updater at a named point; Open on the same file is the
//     restart;
//   - replays of stale attempts: ReplayPrevious resends the last report of the attempt before the
//     current one.
//
// The updater never learns a verdict. The reports go up one way, like the real device-reports
// channel, so a test that wants to know what the platform decided asks the platform.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"sync"
)

// Errors the updater returns.
var (
	// ErrCrashed means a Faults.Crash hook killed this instance. Reopen the state file to
	// continue; this instance does nothing further.
	ErrCrashed = errors.New("updater: crashed (injected)")
	// ErrAttemptInProgress is returned by Assign when a different attempt is still running.
	ErrAttemptInProgress = errors.New("updater: another attempt is still in progress")
	// ErrPastRecall is returned by Abandon once installation has begun.
	ErrPastRecall = errors.New("updater: update is beyond recall")
	// ErrNoAttempt is returned when there is no attempt to act on.
	ErrNoAttempt = errors.New("updater: no attempt")
	// ErrFinished is returned by Abandon on an attempt that has already ended.
	ErrFinished = errors.New("updater: attempt already ended")
)

// Transport carries one report toward the platform. A nil return means the report was handed
// over; the updater learns nothing about what the platform made of it.
type Transport interface {
	Send(ctx context.Context, payload []byte) error
}

// Config configures an Updater.
type Config struct {
	// StatePath is the device-local state file.
	StatePath string
	// Component is the one component this updater manages (e.g. "firmware").
	Component string
	// Transport carries the reports.
	Transport Transport
	// Source serves the artifact bytes; it is read in ChunkSize pieces at the persisted offset,
	// which is what makes a download resumable.
	Source io.ReaderAt
	// ChunkSize is the bytes fetched per step. Default 64 KiB.
	ChunkSize int64
	// InitialVersion is the version running on a fresh device (no state file yet).
	InitialVersion string
}

// Assignment is what the platform tells the device to install. Its wire form belongs to a later
// slice of the OTA work; this is the stand-in the simulator needs to be driven, and it carries
// exactly the facts the reports must echo back.
type Assignment struct {
	AttemptID      string
	AssignmentID   string
	Component      string
	ArtifactDigest string
	Version        string
	Size           int64
	RequiresReboot bool
}

// CrashPoint names where in a step a Faults.Crash hook may kill the updater.
type CrashPoint string

const (
	// CrashBeforeSend: the state, including the pending report, is on disk; nothing was sent.
	CrashBeforeSend CrashPoint = "BEFORE_SEND"
	// CrashAfterSend: the report was sent, but the device had not yet recorded that.
	CrashAfterSend CrashPoint = "AFTER_SEND"
)

// Snapshot is a read-only view of the updater's persisted position.
type Snapshot struct {
	AttemptID      string
	Stage          string
	Seq            uint64
	BytesHave      int64
	BootID         string
	RunningVersion string
	Pending        bool
}

// Faults are the injectable misbehaviours. The zero value injects nothing.
type Faults struct {
	// Crash is consulted at each CrashPoint; returning true kills this instance there.
	Crash func(CrashPoint, Snapshot) bool
	// DuplicateNext sends the next report twice in a row.
	DuplicateNext bool
	// FailVerification makes the verification step fail whatever the bytes hash to.
	FailVerification bool
	// NoConfirmAfterReboot boots the new image but never reports it: the device is silent after
	// REBOOTING until the fault is cleared or it reports an inventory.
	NoConfirmAfterReboot bool
	// BootOldImage makes the reboot come up on the previous image (the new slot was rejected).
	BootOldImage bool
}

// Updater is the fake device-side updater. Methods are safe for concurrent use but run one at a
// time.
type Updater struct {
	cfg    Config
	mu     sync.Mutex
	st     state
	h      hash.Hash
	faults Faults
	dead   bool
}

// Open loads the state file (or starts fresh when there is none) and returns the updater. A file
// that exists but cannot be trusted is an error, never a fresh start.
func Open(cfg Config) (*Updater, error) {
	switch {
	case cfg.StatePath == "":
		return nil, errors.New("updater: StatePath is required")
	case cfg.Component == "":
		return nil, errors.New("updater: Component is required")
	case cfg.Transport == nil:
		return nil, errors.New("updater: Transport is required")
	case cfg.Source == nil:
		return nil, errors.New("updater: Source is required")
	case cfg.ChunkSize < 0:
		return nil, errors.New("updater: ChunkSize must not be negative")
	}
	if cfg.ChunkSize == 0 {
		cfg.ChunkSize = 64 << 10
	}
	st, existed, err := loadState(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	if existed && st.Component != cfg.Component {
		return nil, fmt.Errorf("%w: %s manages %q, not %q", ErrCorruptState, cfg.StatePath, st.Component, cfg.Component)
	}
	if !existed {
		if cfg.InitialVersion == "" {
			return nil, errors.New("updater: InitialVersion is required for a fresh device")
		}
		st = state{Component: cfg.Component, BootSeq: 1, BootID: bootID(1), RunningVersion: cfg.InitialVersion}
	}
	u := &Updater{cfg: cfg, st: st, h: sha256.New()}
	if a := u.st.Attempt; a != nil && len(a.HashState) > 0 {
		if err := u.h.(encoding.BinaryUnmarshaler).UnmarshalBinary(a.HashState); err != nil {
			return nil, fmt.Errorf("%w: %s: hash state: %v", ErrCorruptState, cfg.StatePath, err)
		}
	}
	if !existed {
		if err := u.save(); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func bootID(n int) string { return "boot-" + strconv.Itoa(n) }

// SetFaults replaces the injected faults.
func (u *Updater) SetFaults(f Faults) {
	u.lock()
	defer u.unlock()
	u.faults = f
}

func (u *Updater) lock()   { u.mu.Lock() }
func (u *Updater) unlock() { u.mu.Unlock() }

// Snapshot returns the persisted position.
func (u *Updater) Snapshot() Snapshot {
	u.lock()
	defer u.unlock()
	return u.snapshot()
}

func (u *Updater) snapshot() Snapshot {
	s := Snapshot{BootID: u.st.BootID, RunningVersion: u.st.RunningVersion}
	if a := u.st.Attempt; a != nil {
		s.AttemptID, s.Stage, s.Seq, s.BytesHave, s.Pending = a.AttemptID, a.Stage, a.Seq, a.BytesHave, len(a.Pending) > 0
	}
	return s
}

func (u *Updater) save() error {
	if a := u.st.Attempt; a != nil && a.BytesHave > 0 {
		hs, err := u.h.(encoding.BinaryMarshaler).MarshalBinary()
		if err != nil {
			return fmt.Errorf("updater: hash state: %w", err)
		}
		a.HashState = hs
	}
	return saveState(u.cfg.StatePath, u.st)
}

func ended(stage string) bool {
	return stage == stageRunning || stage == stageFailed || stage == stageAbandoned
}

// recallable reports whether a stage is early enough to abandon.
func recallable(stage string) bool {
	switch stage {
	case "", stageReceived, stageDownloading, stageDownloaded, stageVerified:
		return true
	}
	return false
}

// Assign hands the device an update. Re-delivering the assignment it is already working on is a
// no-op (the platform may repeat it after a reconnect); a different attempt while one is running
// is refused.
func (u *Updater) Assign(a Assignment) error {
	u.lock()
	defer u.unlock()
	if u.dead {
		return ErrCrashed
	}
	switch {
	case a.AttemptID == "" || a.AssignmentID == "" || a.ArtifactDigest == "" || a.Version == "":
		return errors.New("updater: assignment needs attemptId, assignmentId, artifactDigest and version")
	case a.Size <= 0:
		return errors.New("updater: assignment size must be positive")
	case a.Component != u.cfg.Component:
		return fmt.Errorf("updater: assignment is for component %q, this updater manages %q", a.Component, u.cfg.Component)
	}
	if cur := u.st.Attempt; cur != nil {
		if cur.AttemptID == a.AttemptID {
			if cur.AssignmentID == a.AssignmentID && cur.Digest == a.ArtifactDigest && cur.Version == a.Version &&
				cur.Size == a.Size && cur.RequiresReboot == a.RequiresReboot {
				return nil
			}
			return fmt.Errorf("updater: attempt %q is already assigned with different content", a.AttemptID)
		}
		if !ended(cur.Stage) || len(cur.Pending) > 0 {
			return ErrAttemptInProgress
		}
		u.st.Previous = cur.LastSent
	}
	u.st.Attempt = &attemptState{
		AttemptID: a.AttemptID, AssignmentID: a.AssignmentID, Digest: a.ArtifactDigest,
		Version: a.Version, Size: a.Size, RequiresReboot: a.RequiresReboot,
	}
	u.h = sha256.New()
	return u.save()
}

// Run steps until the updater has nothing more to do, an error occurs, or maxSteps is reached.
// It returns the steps taken and whether it finished (as opposed to stopping on maxSteps).
func (u *Updater) Run(ctx context.Context, maxSteps int) (steps int, done bool, err error) {
	for steps < maxSteps {
		done, err = u.Step(ctx)
		if err != nil {
			return steps, false, err
		}
		if done {
			return steps, true, nil
		}
		steps++
	}
	return steps, false, nil
}

// Step does one unit of work: it resends a report left pending, or else performs the next action
// (one download chunk, a verification, an install, a boot) and sends its report. done is true
// when there is nothing further to do — the attempt ended, or the device is deliberately silent
// after a reboot. A non-nil error means no progress was recorded beyond what the state file
// already holds; call Step again to retry.
func (u *Updater) Step(ctx context.Context) (done bool, err error) {
	u.lock()
	defer u.unlock()
	if u.dead {
		return false, ErrCrashed
	}
	a := u.st.Attempt
	if a == nil {
		return true, nil
	}
	if len(a.Pending) > 0 {
		return false, u.flush(ctx)
	}
	if ended(a.Stage) {
		return true, nil
	}
	if done, err = u.advance(a); err != nil || done {
		return done, err
	}
	return false, u.flush(ctx)
}

// advance performs the action that follows the last generated stage and leaves its report
// pending (persisted). done is true when the action is "wait".
func (u *Updater) advance(a *attemptState) (done bool, err error) {
	switch a.Stage {
	case "":
		return false, u.report(a, wireReport{Stage: stageReceived})

	case stageReceived, stageDownloading:
		if a.BytesHave < a.Size {
			return false, u.downloadChunk(a)
		}
		return false, u.report(a, wireReport{Stage: stageDownloaded})

	case stageDownloaded:
		got := "sha256:" + hex.EncodeToString(u.h.Sum(nil))
		switch {
		case u.faults.FailVerification:
			return false, u.fail(a, "VERIFICATION_FAILED", "verification failed (injected)")
		case got != a.Digest:
			return false, u.fail(a, "VERIFICATION_FAILED", fmt.Sprintf("digest mismatch: downloaded %s, expected %s", got, a.Digest))
		}
		return false, u.report(a, wireReport{Stage: stageVerified})

	case stageVerified:
		return false, u.report(a, wireReport{Stage: stageInstalling})

	case stageInstalling:
		// Writing the inactive slot. Nothing is running the new image yet.
		u.st.SlotVersion = a.Version
		if a.RequiresReboot {
			return false, u.report(a, wireReport{Stage: stageRebooting})
		}
		u.st.RunningVersion = a.Version
		return false, u.report(a, u.runningReport(a))

	case stageRebooting:
		if !a.Booted {
			u.st.BootSeq++
			u.st.BootID = bootID(u.st.BootSeq)
			if !u.faults.BootOldImage {
				u.st.RunningVersion = u.st.SlotVersion
			}
			a.Booted = true
			if err := u.save(); err != nil {
				return false, err
			}
		}
		if u.faults.NoConfirmAfterReboot {
			return true, nil
		}
		if u.st.RunningVersion != a.Version {
			return false, u.fail(a, "ROLLED_BACK", fmt.Sprintf("booted %s, expected %s", u.st.RunningVersion, a.Version))
		}
		return false, u.report(a, u.runningReport(a))
	}
	return false, fmt.Errorf("updater: attempt %q is in unknown stage %q", a.AttemptID, a.Stage)
}

func (u *Updater) runningReport(a *attemptState) wireReport {
	return wireReport{Stage: stageRunning, Running: &wireRunning{Version: u.st.RunningVersion, Digest: a.Digest}}
}

func (u *Updater) fail(a *attemptState, code, detail string) error {
	return u.report(a, wireReport{Stage: stageFailed, Result: &wireResult{Code: code, Detail: truncateDetail(detail)}})
}

// downloadChunk fetches the next chunk at the persisted offset. A read failure changes nothing,
// so the same chunk is simply asked for again.
func (u *Updater) downloadChunk(a *attemptState) error {
	n := min(u.cfg.ChunkSize, a.Size-a.BytesHave)
	buf := make([]byte, n)
	got, err := u.cfg.Source.ReadAt(buf, a.BytesHave)
	if int64(got) < n {
		if err == nil || errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("updater: download at offset %d: %w", a.BytesHave, err)
	}
	u.h.Write(buf)
	a.BytesHave += n
	return u.report(a, wireReport{Stage: stageDownloading, Progress: &wireProgress{Bytes: a.BytesHave, Total: a.Size}})
}

// report fills in the identity fields, advances the sequence, and persists the encoded report as
// pending. It does not send.
func (u *Updater) report(a *attemptState, r wireReport) error {
	r.V, r.Kind = wireVersion, kindProgress
	r.AttemptID, r.AssignmentID, r.Component = a.AttemptID, a.AssignmentID, u.cfg.Component
	r.ArtifactDigest = a.Digest
	r.Seq = a.Seq + 1
	r.BootID = u.st.BootID
	raw, err := r.encode()
	if err != nil {
		return err
	}
	a.Seq, a.Stage, a.Pending = r.Seq, r.Stage, raw
	return u.save()
}

// flush sends the pending report. On a send error the report stays pending and the error is
// returned; the next Step resends the same bytes.
func (u *Updater) flush(ctx context.Context) error {
	a := u.st.Attempt
	if u.crashAt(CrashBeforeSend) {
		return ErrCrashed
	}
	copies := 1
	if u.faults.DuplicateNext {
		u.faults.DuplicateNext = false
		copies = 2
	}
	for i := 0; i < copies; i++ {
		if err := u.cfg.Transport.Send(ctx, a.Pending); err != nil {
			return fmt.Errorf("updater: send seq %d: %w", a.Seq, err)
		}
	}
	if u.crashAt(CrashAfterSend) {
		return ErrCrashed
	}
	a.LastSent, a.Pending = a.Pending, nil
	return u.save()
}

func (u *Updater) crashAt(p CrashPoint) bool {
	if u.faults.Crash != nil && u.faults.Crash(p, u.snapshot()) {
		u.dead = true
		return true
	}
	return false
}

// Abandon sends ABANDONED for the current attempt. It is the device's half of a platform
// cancel, and is refused once installation has begun.
func (u *Updater) Abandon(ctx context.Context) error {
	u.lock()
	defer u.unlock()
	if u.dead {
		return ErrCrashed
	}
	a := u.st.Attempt
	switch {
	case a == nil:
		return ErrNoAttempt
	case len(a.Pending) == 0 && ended(a.Stage):
		return ErrFinished
	case !recallable(a.Stage):
		return ErrPastRecall
	}
	if len(a.Pending) > 0 {
		// Settle the report already in flight first, so ABANDONED takes the next sequence number.
		if err := u.flush(ctx); err != nil {
			return err
		}
		if ended(a.Stage) {
			return ErrFinished
		}
	}
	if err := u.report(a, wireReport{Stage: stageAbandoned}); err != nil {
		return err
	}
	return u.flush(ctx)
}

// ReportInventory sends what the device is running now, independent of any attempt. It is how a
// device that rebooted without confirming lets the platform find out.
func (u *Updater) ReportInventory(ctx context.Context) error {
	u.lock()
	defer u.unlock()
	if u.dead {
		return ErrCrashed
	}
	raw, err := wireReport{
		V: wireVersion, Kind: kindInventory, Component: u.cfg.Component,
		Running: &wireRunning{Version: u.st.RunningVersion}, BootID: u.st.BootID,
	}.encode()
	if err != nil {
		return err
	}
	return u.cfg.Transport.Send(ctx, raw)
}

// ReplayPrevious resends the last report of the attempt before the current one: a stale report
// arriving after the platform has moved on.
func (u *Updater) ReplayPrevious(ctx context.Context) error {
	u.lock()
	defer u.unlock()
	if u.dead {
		return ErrCrashed
	}
	if len(u.st.Previous) == 0 {
		return ErrNoAttempt
	}
	return u.cfg.Transport.Send(ctx, u.st.Previous)
}
