// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// noSuchSim is the text dcctl's sim-record loader answers a name it has no record of
// with. A destroy of a slot that was never created is the normal case on a first run
// and after the record volume is wiped, and it must not stop the rotation. The text is
// pinned against dcctl's source by TestNoSuchSimMatchesDcctl: a message that changed
// would turn every first night into a failure, or hide a real one.
const noSuchSim = "no sim named"

// Runner runs a command to completion and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// execRunner runs real commands. WaitDelay bounds how long a cancelled command may keep its
// output pipe open.
//
// 🔴 `dcctl sim run` starts dc-simulator as a CHILD that inherits the output pipe. Killing
// dcctl alone leaves the child holding the pipe, and the wait for output waits for the pipe
// to close: without a WaitDelay a cancelled supervisor never returns, and the day-boundary
// exit that hands the next day to a fresh start would hang on yesterday's simulator
// forever. The interrupt asks dcctl to stop its child the way it does for Ctrl+C; where a
// process cannot be sent one (Windows) it is killed; the delay then closes the pipes on
// whatever is left.
type execRunner struct {
	WaitDelay time.Duration
	// Stream, when set, receives the live output of `dcctl sim run` as it is written, so a
	// running simulator is visible in the pod's logs. Other commands are not streamed:
	// `sim create` prints what it minted.
	Stream io.Writer
}

// maxOutput bounds what a command's output keeps in memory. Only the tail is ever used
// (the exit log shows 400 bytes), and a simulator that fails the same way for a day must
// not grow the driver without limit.
const maxOutput = 16 << 10

// tailBuffer is an io.Writer that keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func (r execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Cancel = func() error {
		if err := c.Process.Signal(os.Interrupt); err != nil {
			return c.Process.Kill()
		}
		return nil
	}
	c.WaitDelay = r.WaitDelay
	buf := &tailBuffer{max: maxOutput}
	var w io.Writer = buf
	if r.Stream != nil && len(args) >= 2 && args[0] == "sim" && args[1] == "run" {
		w = io.MultiWriter(buf, r.Stream)
	}
	c.Stdout, c.Stderr = w, w
	err := c.Run()
	return buf.String(), err
}

// Driver performs a rotation and then keeps the current slot's simulators running.
type Driver struct {
	Cfg Config
	// Dcctl is the dcctl binary; Simulator is passed to `dcctl sim run --simulator`.
	Dcctl     string
	Simulator string
	// ReachArgs are appended to every `dcctl sim create` and `dcctl sim destroy`: where
	// the instance is (--server, --instance, --host-header, --tls). CreateArgs are
	// appended to `sim create` only: flags destroy does not have (--ingress,
	// --mqtt-broker), which it refuses as unknown. Both are deployment facts, not
	// policy, so they come from the environment of the pod.
	ReachArgs  []string
	CreateArgs []string
	// ProbeArgs and AdminPassword let the driver ask the platform whether a tenant still
	// exists when dcctl has lost the sim's record (`dcctl tenant-deletion status`: --server,
	// --tls, --email). Without them a missing record is never taken as "the tenant is gone".
	ProbeArgs     []string
	AdminPassword string
	// Days records, per sim, the playground day it was created on, so a slot that comes
	// round again still holding an earlier day's tenant is noticed. Nil records nothing.
	Days *DayLedger
	// BaseControlPort is the first of the control ports; each scenario gets one.
	BaseControlPort int
	Runner          Runner
	Log             io.Writer
	// CheckEvery is how often Run looks at the clock for the end of the day (a minute
	// when zero); RestartPause is the first pause before a dead simulator is restarted
	// (two seconds when zero). Both exist so a test need not wait for them.
	CheckEvery   time.Duration
	RestartPause time.Duration
}

// controlPort gives each scenario its own port. A slot has the same scenarios in the
// same order every day, so a sim has the same port every day.
func (d *Driver) controlPort(i int) int { return d.BaseControlPort + i }

func (d *Driver) logf(format string, a ...any) {
	if d.Log != nil {
		fmt.Fprintf(d.Log, format+"\n", a...)
	}
}

// createArgs is the `dcctl sim create` command line for scenario i of the slot.
func (d *Driver) createArgs(scenario, name string, i int) []string {
	args := []string{"sim", "create", name,
		"--manifest", scenario,
		"--seed", strconv.FormatInt(d.Cfg.Seed, 10),
		"--tenant-name", "Playground " + scenario,
		"--control-addr", fmt.Sprintf("http://localhost:%d", d.controlPort(i)),
	}
	args = append(args, d.ReachArgs...)
	return append(args, d.CreateArgs...)
}

// Rotate carries out the plan for now. The current slot is created FIRST and a failure
// there is fatal: a playground with nothing in it is the failure that matters, and the
// 12-hour hold on a token means a create that was refused is not worth retrying in a
// loop. Destroys come second and only log: a slot that could not be torn down tonight is
// torn down again at the next start, and the slot after it is still free.
func (d *Driver) Rotate(ctx context.Context, now time.Time) (Plan, error) {
	if err := d.Cfg.Validate(); err != nil {
		return Plan{}, err
	}
	p := PlanRotation(now, d.Cfg)
	day := Day(now, d.Cfg.ResetHour)
	for i, sc := range d.Cfg.Scenarios {
		name := SimName(sc, p.Slot)
		if !ValidSimName(name) {
			return p, fmt.Errorf("sim name %q is not valid", name)
		}
		// 🔴 A SLOT THAT IS STILL HOLDING AN EARLIER DAY'S TENANT IS NOT FRESH. `dcctl sim
		// create` is idempotent: against a tenant that survived (its destroy failed on each
		// of the nights since, because a member was added to it, say) it succeeds and keeps
		// whatever a visitor left there, and the reset would report success. Refusing is
		// loud. The tenant is destroyed here, so the slot heals by itself: the platform
		// then holds its token for twelve hours, `create` is refused (loudly, in the pod's
		// log) until the hold ends, and the first create after it makes a fresh tenant.
		if was, ok, err := d.Days.Get(name); err != nil {
			return p, err
		} else if ok && was != day {
			if err := d.destroy(ctx, name); err != nil {
				return p, fmt.Errorf("%s still holds the tenant of playground day %d (today is day %d) and destroying it failed: %w",
					name, was, day, err)
			}
			return p, fmt.Errorf("%s still held the tenant of playground day %d (today is day %d), left by a destroy that "+
				"failed; it has been destroyed now, and the slot is unavailable until the platform releases the token "+
				"(about twelve hours), after which this driver creates it fresh", name, was, day)
		}
		// Recorded BEFORE the create, not after: a driver stopped between a successful create
		// and the record would leave a tenant the ledger cannot see, and a visit by the same
		// slot a cycle later would reuse it unnoticed. A create that then fails leaves the
		// entry, which only costs a destroy-and-wait at the next day's visit.
		if err := d.Days.Set(name, day); err != nil {
			return p, err
		}
		if out, err := d.Runner.Run(ctx, d.Dcctl, d.createArgs(sc, name, i)...); err != nil {
			return p, fmt.Errorf("create %s: %w\n%s", name, err, out)
		}
		d.logf("created %s", name)
	}
	// Sims this driver created that the configuration no longer names (a scenario taken off
	// the list, or fewer slots) would otherwise survive every reset with their tenants and
	// credentials. The ledger knows them.
	orphans, err := d.orphans(p)
	if err != nil {
		return p, err
	}
	var failed []string
	for _, name := range append(append([]string(nil), p.Destroy...), orphans...) {
		if err := d.destroy(ctx, name); err != nil {
			d.logf("destroy %s failed, will retry at the next start: %v", name, err)
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		d.logf("%d stale sim(s) could not be destroyed: %s", len(failed), strings.Join(failed, ", "))
	}
	return p, nil
}

// orphans are the ledger's sims that the plan neither creates nor destroys.
func (d *Driver) orphans(p Plan) ([]string, error) {
	known := map[string]bool{}
	for _, n := range p.Create {
		known[n] = true
	}
	for _, n := range p.Destroy {
		known[n] = true
	}
	names, err := d.Days.Names()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if !known[n] && strings.HasPrefix(n, "pg-") && ValidSimName(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// destroy tears a sim down and drops it from the ledger. A sim dcctl has no record of was
// never created or is already gone, which is nothing to tear down and not a failure, but
// only once the platform confirms its tenant is not there.
//
// 🔴 A MISSING LOCAL RECORD IS NOT EVIDENCE THE TENANT IS GONE. dcctl creates the tenant
// before it saves the record, so a create that died between the two (or a record volume
// that was wiped) leaves a live tenant dcctl can no longer destroy. Forgetting the ledger
// entry then would let the slot's next visit adopt that tenant and report a clean reset
// over a visitor's leftovers. When the ledger says this driver created the sim, the entry
// is cleared only after the platform answers that the tenant does not exist; any other
// answer keeps it and fails the destroy.
func (d *Driver) destroy(ctx context.Context, name string) error {
	out, err := d.Runner.Run(ctx, d.Dcctl, append([]string{"sim", "destroy", name}, d.ReachArgs...)...)
	switch {
	case err == nil:
		d.logf("destroyed %s", name)
	case strings.Contains(out, noSuchSim):
		if _, recorded, err := d.Days.Get(name); err != nil {
			return err
		} else if recorded {
			if err := d.confirmTenantGone(ctx, name); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w\n%s", err, out)
	}
	return d.Days.Forget(name)
}

// tenantActive is what `dcctl tenant-deletion status` says of a tenant that exists and is
// not being deleted; noDeletionRecord is what it says of a token with no tenant and no
// completed deletion. TestDcctlSourceStillMatchesWhatTheDriverAssumes pins both against
// dcctl's source.
const (
	tenantActive     = "the tenant is active and is not being deleted"
	noDeletionRecord = "no deletion record for tenant"
)

// confirmTenantGone asks the platform whether the sim's tenant still exists and returns nil
// only when it does not. Anything it cannot tell is an error.
func (d *Driver) confirmTenantGone(ctx context.Context, name string) error {
	if len(d.ProbeArgs) == 0 || d.AdminPassword == "" {
		return fmt.Errorf("%s has no sim record, and the driver cannot ask the platform whether its tenant still exists "+
			"(no probe is configured); the ledger entry is kept", name)
	}
	token := "sim-" + name
	args := append([]string{"tenant-deletion", "status", token}, d.ProbeArgs...)
	out, err := d.Runner.Run(ctx, d.Dcctl, append(args, "--password", d.AdminPassword)...)
	switch {
	case strings.Contains(out, tenantActive):
		return fmt.Errorf("%s has no sim record but its tenant %s is still live; dcctl cannot destroy it without the record, "+
			"so a superuser must delete the tenant by hand. The ledger entry is kept", name, token)
	case err == nil, strings.Contains(out, noDeletionRecord):
		return nil
	default:
		return fmt.Errorf("%s has no sim record, and whether tenant %s still exists could not be confirmed (the ledger entry is kept): %w\n%s",
			name, token, err, tail(out, 400))
	}
}

// Run rotates and then supervises one `dcctl sim run` per scenario of the current slot.
// It returns nil when ctx is cancelled, and an error when the slot's day ends with the
// pod still up: the exit is what hands the next day to a fresh start, which rotates.
func (d *Driver) Run(ctx context.Context, now func() time.Time) error {
	p, err := d.Rotate(ctx, now())
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	for _, sc := range d.Cfg.Scenarios {
		name := SimName(sc, p.Slot)
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.supervise(runCtx, name)
		}()
	}
	every := d.CheckEvery
	if every <= 0 {
		every = time.Minute
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			stop()
			wg.Wait()
			return nil
		case <-tick.C:
			if Slot(now(), d.Cfg) != p.Slot {
				stop()
				wg.Wait()
				return fmt.Errorf("the playground day ended (slot %d is over); exiting so the next start rotates", p.Slot)
			}
		}
	}
}

// supervise keeps one simulator running, restarting it with a growing pause.
func (d *Driver) supervise(ctx context.Context, name string) {
	base := d.RestartPause
	if base <= 0 {
		base = 2 * time.Second
	}
	pause := base
	for ctx.Err() == nil {
		started := time.Now()
		out, err := d.Runner.Run(ctx, d.Dcctl, "sim", "run", name, "--simulator", d.Simulator)
		if ctx.Err() != nil {
			return
		}
		d.logf("simulator for %s exited (%v), restarting in %s\n%s", name, err, pause, tail(out, 400))
		if time.Since(started) > time.Minute {
			pause = base
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		if pause < time.Minute {
			pause *= 2
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var errNotInCluster = errors.New("not running in a cluster: KUBERNETES_SERVICE_HOST is unset")
