// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
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
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
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
		// loud and is the only honest answer; the 12-hour hold on a token means destroying
		// it now and creating it again is not available either.
		if was, ok, err := d.Days.Get(name); err != nil {
			return p, err
		} else if ok && was != day {
			return p, fmt.Errorf("%s still holds the tenant of playground day %d (today is day %d): an earlier destroy "+
				"of it failed and nothing since has removed it, so today's slot would not be fresh. Destroy it by hand "+
				"(dcctl sim destroy %s), then wait out the token hold before this slot can be used again", name, was, day, name)
		}
		if out, err := d.Runner.Run(ctx, d.Dcctl, d.createArgs(sc, name, i)...); err != nil {
			return p, fmt.Errorf("create %s: %w\n%s", name, err, out)
		}
		if err := d.Days.Set(name, day); err != nil {
			return p, err
		}
		d.logf("created %s", name)
	}
	var failed []string
	for _, name := range p.Destroy {
		out, err := d.Runner.Run(ctx, d.Dcctl, append([]string{"sim", "destroy", name}, d.ReachArgs...)...)
		switch {
		case err == nil:
			d.logf("destroyed %s", name)
			if err := d.Days.Forget(name); err != nil {
				return p, err
			}
		case strings.Contains(out, noSuchSim):
			// Never created, or already gone: nothing to tear down.
			if err := d.Days.Forget(name); err != nil {
				return p, err
			}
		default:
			d.logf("destroy %s failed, will retry at the next start: %v\n%s", name, err, out)
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		d.logf("%d stale sim(s) could not be destroyed: %s", len(failed), strings.Join(failed, ", "))
	}
	return p, nil
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
