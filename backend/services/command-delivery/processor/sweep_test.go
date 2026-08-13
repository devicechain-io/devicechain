// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// fakeApi is a CommandDeliveryApi that records what the sweep asked it to do and
// lets a test decide whether the sweep lock is available.
type fakeApi struct {
	mu sync.Mutex

	lockAvailable bool
	pending       []*model.Command

	// The reconcile pass's half: its own lock, the withheld set it walks, and what it
	// released. The lock defaults to UNAVAILABLE so the delivery tests above are
	// unaffected by a reconciler they are not about.
	reconcileLockAvailable bool
	reconcileLockAttempts  int
	heldPage               []*model.Command
	heldReads              []uint
	heldReadErr            error
	releasedHolds          []uint

	lockAttempts    int
	pendingReads    int
	expireCalls     int
	markedSent      []uint
	released        []uint
	markSentByToken []string
	held            []uint
	undeliverable   []uint
	failReasons     []string

	// claimFails makes MarkSent report that this caller LOST the claim, and
	// releaseFails makes ReleaseClaim error. Both default to the happy path so
	// existing tests are unaffected.
	claimFails   bool
	releaseFails bool
	// holdLoses and releaseLoses make HoldCommand / ReleaseHold report a zero-row
	// update — the conditional write losing to another writer that moved the row after
	// the scan that selected it.
	holdLoses    bool
	releaseLoses bool
}

func (f *fakeApi) TryReconcileLock(_ context.Context, fn func() error) (bool, error) {
	f.mu.Lock()
	f.reconcileLockAttempts++
	available := f.reconcileLockAvailable
	f.mu.Unlock()
	if !available {
		return false, nil
	}
	return true, fn()
}

// HeldCommands answers from f.heldPage, honouring the cursor so a test can drive the
// walk the way the reconciler does.
func (f *fakeApi) HeldCommands(_ context.Context, afterId uint, limit int) ([]*model.Command, uint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heldReads = append(f.heldReads, afterId)
	if f.heldReadErr != nil {
		return nil, 0, f.heldReadErr
	}
	page := make([]*model.Command, 0, limit)
	for _, cmd := range f.heldPage {
		if cmd.ID > afterId && len(page) < limit {
			page = append(page, cmd)
		}
	}
	if len(page) < limit {
		return page, 0, nil
	}
	return page, page[len(page)-1].ID, nil
}

func (f *fakeApi) ReleaseHold(_ context.Context, id uint) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releasedHolds = append(f.releasedHolds, id)
	return !f.releaseLoses, nil
}

func (f *fakeApi) HoldCommand(_ context.Context, id uint) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = append(f.held, id)
	return !f.holdLoses, nil
}

func (f *fakeApi) MarkUndeliverable(_ context.Context, id uint, reason string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undeliverable = append(f.undeliverable, id)
	f.failReasons = append(f.failReasons, reason)
	return true, nil
}

func (f *fakeApi) TrySweepLock(_ context.Context, fn func() error) (bool, error) {
	f.mu.Lock()
	f.lockAttempts++
	available := f.lockAvailable
	f.mu.Unlock()
	if !available {
		return false, nil
	}
	return true, fn()
}

func (f *fakeApi) PendingCommands(context.Context) ([]*model.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingReads++
	return f.pending, nil
}

func (f *fakeApi) ExpireStale(context.Context, time.Time) (int64, map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireCalls++
	return 0, nil, nil
}

func (f *fakeApi) MarkSentByToken(_ context.Context, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markSentByToken = append(f.markSentByToken, token)
	return true, nil
}

func (f *fakeApi) MarkSent(_ context.Context, id uint) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markedSent = append(f.markedSent, id)
	return !f.claimFails, nil
}

func (f *fakeApi) ReleaseClaim(_ context.Context, id uint) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, id)
	if f.releaseFails {
		return false, errors.New("release failed")
	}
	return true, nil
}

func (f *fakeApi) CreateCommand(context.Context, *model.CommandCreateRequest) (*model.Command, error) {
	return nil, nil
}
func (f *fakeApi) MarkResponse(context.Context, string, bool, *string, *string) (*model.Command, error) {
	return nil, nil
}
func (f *fakeApi) CancelCommand(context.Context, string) (*model.Command, error) { return nil, nil }
func (f *fakeApi) CommandsById(context.Context, []uint) ([]*model.Command, error) {
	return nil, nil
}
func (f *fakeApi) CommandsByToken(context.Context, []string) ([]*model.Command, error) {
	return nil, nil
}
func (f *fakeApi) Commands(context.Context, model.CommandSearchCriteria) (*model.CommandSearchResults, error) {
	return nil, nil
}

// recordingWriter counts publishes — the physical actuation the lock protects.
type recordingWriter struct {
	mu       sync.Mutex
	messages []messaging.Message
	devices  []string
	// err, when set, makes WriteToDevice fail — the publish-failure path, which is
	// where a claim leaks if it is not released.
	err error
}

func (w *recordingWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messages = append(w.messages, msgs...)
	return nil
}

// WriteToDevice is the path commands actually take. It records the target device
// alongside the message so a test can assert a command went to ONE device rather
// than merely that something was published.
func (w *recordingWriter) WriteToDevice(_ context.Context, deviceToken string, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.devices = append(w.devices, deviceToken)
	w.messages = append(w.messages, msgs...)
	return nil
}
func (w *recordingWriter) HandleResponse(error) {}

func (w *recordingWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.messages)
}

func queued(id uint, token string) *model.Command {
	cmd := &model.Command{DeviceToken: "dev-" + token, Name: "reboot"}
	cmd.ID = id
	cmd.Token = token
	cmd.TenantId = "acme"
	return cmd
}

// procWith builds a processor wired to the fake API and a recording writer, without
// the microservice/lifecycle machinery the sweep does not touch.
func procWith(api model.CommandDeliveryApi, writer messaging.MessageWriter) *CommandDeliveryProcessor {
	return &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer}
}

// The defect this pins: the sweep used to run on every replica with no lock, so an
// instance running N replicas published every queued command N times. A command is a
// physical actuation — a device told twice to dispense or unlock does it twice — and
// it was reachable by following our own deployment guidance, which recommends
// replicas:2 for zero-downtime rollouts.
func TestSweepPublishesNothingWithoutTheLock(t *testing.T) {
	api := &fakeApi{lockAvailable: false, pending: []*model.Command{queued(1, "c1"), queued(2, "c2")}}
	writer := &recordingWriter{}

	procWith(api, writer).sweepLocked(context.Background())

	if api.lockAttempts != 1 {
		t.Fatalf("expected one lock attempt, got %d", api.lockAttempts)
	}
	if writer.count() != 0 {
		t.Fatalf("a replica without the lock must publish nothing, got %d messages", writer.count())
	}
	// Not merely "published nothing" — it must not have read or expired either. A
	// sweep that does the work and skips only the publish would still race its peer.
	if api.pendingReads != 0 || api.expireCalls != 0 {
		t.Fatalf("a replica without the lock must not sweep at all: reads=%d expires=%d",
			api.pendingReads, api.expireCalls)
	}
}

func TestSweepDeliversWhenItHoldsTheLock(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1"), queued(2, "c2")}}
	writer := &recordingWriter{}

	procWith(api, writer).sweepLocked(context.Background())

	if writer.count() != 2 {
		t.Fatalf("expected both queued commands published, got %d", writer.count())
	}
	if api.expireCalls != 1 {
		t.Fatalf("expiry must run inside the same locked pass, got %d calls", api.expireCalls)
	}
	if len(api.markedSent) != 2 {
		t.Fatalf("every published command must be marked sent, got %v", api.markedSent)
	}
	// Each command must be addressed to ITS OWN device. Publishing to a tenant-wide
	// subject would also produce two messages here, so counting messages alone would
	// not have caught the isolation regression this addressing exists to prevent.
	if len(writer.devices) != 2 || writer.devices[0] != "dev-c1" || writer.devices[1] != "dev-c2" {
		t.Fatalf("commands must be published per-device, got %v", writer.devices)
	}
}

// Concurrent sweeps model N replicas ticking at once. Exactly one may publish; the
// rest must skip. The fake's lock is a real mutex-guarded flag, so this exercises the
// call site's use of the lock rather than the lock itself.
func TestConcurrentSweepsPublishOnce(t *testing.T) {
	api := &fakeApi{pending: []*model.Command{queued(1, "c1")}}
	// Exactly one holder: the first caller takes the flag, the rest find it gone.
	api.lockAvailable = true
	var once sync.Mutex
	take := func() bool {
		once.Lock()
		defer once.Unlock()
		if api.lockAvailable {
			api.lockAvailable = false
			return true
		}
		return false
	}
	gated := &gatedApi{fakeApi: api, take: take}
	writer := &recordingWriter{}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			procWith(gated, writer).sweepLocked(context.Background())
		}()
	}
	wg.Wait()

	if writer.count() != 1 {
		t.Fatalf("one command across 8 concurrent replicas must be published once, got %d",
			writer.count())
	}
}

// gatedApi overrides TrySweepLock with a single-holder gate.
type gatedApi struct {
	*fakeApi
	take func() bool
}

func (g *gatedApi) TrySweepLock(_ context.Context, fn func() error) (bool, error) {
	if !g.take() {
		return false, nil
	}
	return true, fn()
}

// queuedFor is queued() for a named tenant, so a sweep can carry commands for more than
// one — which is what the sweep actually loads (PendingCommands reads cross-tenant under
// a system context).
func queuedFor(id uint, token, tenant string) *model.Command {
	cmd := queued(id, token)
	cmd.TenantId = tenant
	return cmd
}

// 🔴 ADR-077: a queued command must not fire into a tenant that has been deleted.
//
// A command is a PHYSICAL ACTUATION. The sweep loads QUEUED rows cross-tenant under a
// system context, and a command enqueued before an operator deleted the tenant is still
// queued after it — so without this gate the next sweep tick opens a valve or trips a
// relay on an offboarded customer's hardware. Worse, the publish happens BEFORE MarkSent,
// so once the tenant's rows are swept the actuation has already happened by the time
// MarkSent fails to find its row: the device acts, and the platform's only record is an
// error log about a command it can no longer describe.
//
// The three assertions are the three things that have to be true together: nothing is
// published for the deleted tenant, the OTHER tenant in the same sweep is unaffected
// (a gate that stopped the batch would be an outage, not a fix), and the refused row is
// left QUEUED rather than marked sent.
func TestSweepRefusesToDeliverIntoADeletedTenant(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{
		queuedFor(1, "c1", "acme"),
		queuedFor(2, "c2", "other"),
	}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.TenantDeleted = func(tenant string) bool { return tenant == "acme" }

	proc.sweepLocked(context.Background())

	if got := writer.devices; len(got) != 1 || got[0] != "dev-c2" {
		t.Fatalf("expected only the live tenant's command to be published, got %v", got)
	}
	if len(api.markedSent) != 1 || api.markedSent[0] != 2 {
		t.Fatalf("the refused command must be left QUEUED; marked sent = %v", api.markedSent)
	}
}

// The negative control: with the gate wired and answering "live", both commands still
// deliver. Without it, a gate that refused everything would satisfy the test above while
// silently stopping every command on the instance.
func TestSweepStillDeliversWhenTheGateSaysLive(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{
		queuedFor(1, "c1", "acme"),
		queuedFor(2, "c2", "other"),
	}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.TenantDeleted = func(string) bool { return false }

	proc.sweepLocked(context.Background())

	if writer.count() != 2 {
		t.Fatalf("a live tenant's commands must still deliver, published %d", writer.count())
	}
}

// An UNWIRED gate must deliver, not panic. The struct's fields are exported and it is
// built by literal (procWith above, and anywhere else that skips the constructor), so a
// nil-normalizing constructor alone would leave a nil call on the delivery path. This is
// the test that makes "read it through the accessor" load-bearing rather than a style
// note — it is how the panic was found.
func TestSweepDeliversWithNoLifecycleGateWired(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}

	procWith(api, writer).sweepLocked(context.Background()) // TenantDeleted is nil

	if writer.count() != 1 {
		t.Fatalf("an unwired gate must leave delivery working, published %d", writer.count())
	}
}

// 🔴 THE PLUMBING, which every test above skips.
//
// procWith builds the processor by struct literal and assigns the gates directly, so all
// three tests above measure the FIELD. The shipped binary reaches them only through
// NewCommandDeliveryProcessor — and with nothing covering that, deleting the constructor's
// `TenantDeleted: tenantDeleted` line disabled the gate in production while leaving this
// entire suite green. A gate is not wired because its logic is right; it is wired because
// the constructor carries it. (The same defect shipped an entirely inert tier carriage
// elsewhere in the platform, one missing assignment, every test passing.)
//
// It calls the real constructor ONCE, and that is a hard limit rather than a preference:
// NewProcessorMetrics registers into prometheus' default registry via promauto, so a
// second construction in this package panics on duplicate registration. Both gates are
// therefore proven in this one pass, which is why it carries two tenants:
//
//   - acme is DELETED, so its command must not publish — the ADR-077 gate arrived.
//   - other is live but its device is authoritatively ABSENT, so its command must be
//     HELD rather than published — the presence gate arrived.
//
// The second assertion is the one that cannot be faked by a broken sweep. "Published
// nothing" is also what a completely wedged sweep looks like; "held exactly command 2"
// requires the constructor's presence reader to have been consulted and to have answered.
func TestConstructorWiresBothDeliveryGates(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "commanddelivery"}
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{
		queuedFor(1, "c1", "acme"),
		queuedFor(2, "c2", "other"),
	}}
	writer := &recordingWriter{}

	proc := NewCommandDeliveryProcessor(ms, nil, writer, core.NewNoOpLifecycleCallbacks(), api,
		func(tenant string) bool { return tenant == "acme" },
		absentReader{"dev-c2"})

	// Drive the real sweep rather than reading the fields back: a field being set proves
	// assignment, not that the delivery path consults what was assigned.
	proc.sweepLocked(context.Background())

	if writer.count() != 0 {
		t.Fatalf("both gates passed to the constructor must reach the sweep; published %d", writer.count())
	}
	if len(api.held) != 1 || api.held[0] != 2 {
		t.Fatalf("the presence reader passed to the constructor must reach the sweep — the live "+
			"tenant's absent device should have its command HELD; held=%v", api.held)
	}
}

// TestALostClaimDoesNotPublish is the point of claiming BEFORE publishing.
//
// 🔴 THE ORDER IS THE DEFECT THIS CLOSES. Publishing first left a window between the
// publish and the mark in which another dispatcher — the LwM2M wake drain, or a release
// path — could claim the same row and actuate the device a SECOND time. A command is a
// physical movement of real hardware, so a duplicate is not a bookkeeping wrinkle.
func TestALostClaimDoesNotPublish(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "cmd-1")}}
	api.claimFails = true
	writer := &recordingWriter{}

	procWith(api, writer).sweepLocked(context.Background())

	if len(api.markedSent) != 1 {
		t.Fatalf("the sweep must attempt the claim, got %d attempts", len(api.markedSent))
	}
	if writer.count() != 0 {
		t.Fatalf("a command whose claim was LOST must not be published; another dispatcher already "+
			"has it and the device would actuate twice (published %d)", writer.count())
	}
	if len(api.released) != 0 {
		t.Fatalf("a lost claim must not be released — the release would steal the row back from "+
			"the dispatcher that won it (released %v)", api.released)
	}
}

// TestAFailedPublishReleasesTheClaim is the other half: the claim must not leak.
//
// A row claimed and then not published reads SENT with a sent_time, is invisible to every
// dispatcher (SENT is neither claimable nor swept), and dies TIMEOUT — "delivered, and the
// device never answered" — for a command that never went out. That is exactly the lie the
// command-state vocabulary exists to stop telling.
func TestAFailedPublishReleasesTheClaim(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "cmd-1")}}
	writer := &recordingWriter{err: errors.New("broker unavailable")}

	procWith(api, writer).sweepLocked(context.Background())

	if len(api.markedSent) != 1 {
		t.Fatalf("the sweep must claim before publishing, got %d claims", len(api.markedSent))
	}
	if len(api.released) != 1 || api.released[0] != 1 {
		t.Fatalf("a command whose publish failed must have its claim released back to QUEUED, "+
			"else it reads SENT forever and expires as TIMEOUT; released=%v", api.released)
	}
}
