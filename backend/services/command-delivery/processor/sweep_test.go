// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
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

	lockAttempts int
	pendingReads int
	expireCalls  int
	markedSent   []uint
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

func (f *fakeApi) ExpireStale(context.Context, time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireCalls++
	return 0, nil
}

func (f *fakeApi) MarkSent(_ context.Context, id uint) (*model.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markedSent = append(f.markedSent, id)
	return nil, nil
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
// procWith builds the processor by struct literal and assigns TenantDeleted directly, so
// all three tests above measure the FIELD. The shipped binary reaches it only through
// NewCommandDeliveryProcessor — and with nothing covering that, deleting the constructor's
// `TenantDeleted: tenantDeleted` line disabled the gate in production while leaving this
// entire suite green. A gate is not wired because its logic is right; it is wired because
// the constructor carries it.
//
// It calls the real constructor once. Once is the limit: NewProcessorMetrics registers
// into prometheus' default registry via promauto, so a second construction in this
// package would panic on duplicate registration — which is also why the other tests use
// the fixture, and why this one is the single place the constructor is exercised.
func TestConstructorWiresTheLifecycleGate(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "commanddelivery"}
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}

	proc := NewCommandDeliveryProcessor(ms, nil, writer, core.NewNoOpLifecycleCallbacks(), api,
		func(tenant string) bool { return tenant == "acme" })

	// Drive the real sweep rather than reading proc.TenantDeleted back: the field being
	// set proves assignment, not that the delivery path consults what was assigned.
	proc.sweepLocked(context.Background())

	if writer.count() != 0 {
		t.Fatalf("the gate passed to the constructor must reach the sweep; published %d", writer.count())
	}
}
