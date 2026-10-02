// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// tofuExec is the one way dcctl builds a tofu runner. It embeds terraform-exec's
// Terraform and overrides only the commands whose stdout an operator wants to watch:
// Init, Apply, Destroy and StateRm stream it for their own duration; everything else
// — Show, Output, Import, Version — is promoted unchanged and prints nothing.
//
// 🔴 A READ MUST NEVER STREAM STDOUT. terraform-exec copies the stdout of EVERY
// command to the writer SetStdout names, and a read's stdout is its result: `show
// -json` is the whole state and `output -json` every output, verbatim — sensitive
// attributes included, on the operator's terminal and in CI logs. So stdout goes to
// io.Discard by default and is pointed at the terminal for the duration of one
// progress command, never left on. Stderr stays streamed for everything; it carries
// tofu's diagnostics.
//
// Not safe for concurrent use: the destination is one per runner, not one per
// command. Every root runs its commands one after another.
type tofuExec struct {
	*tfexec.Terraform
	// stdout is what terraform-exec was handed at construction and never after, so the
	// per-command switch below is a change of DESTINATION rather than a write to a
	// field terraform-exec reads from another goroutine. See streamSwitch.
	stdout *streamSwitch
	// abandonBudget is how long an INTERRUPTED progress command is waited for before
	// runUntilAbandoned gives up on it. A field rather than the constant so a test can
	// exercise the giving-up without waiting out a real budget; newTofuExec is the only
	// thing that sets it in production.
	abandonBudget time.Duration
}

// streamSwitch is the one writer terraform-exec is ever given for stdout. Pointing a
// command at the terminal moves the DESTINATION behind this writer instead of calling
// SetStdout again.
//
// 🔴 SetStdout AFTER A COMMAND HAS STARTED IS A DATA RACE, and runUntilAbandoned is
// what turned that from theory into fact: terraform-exec reads tf.stdout when it
// starts a command, and an abandoned command is still inside terraform-exec when
// streaming returns and restores the quiet default from the OTHER goroutine. The race
// detector sees it, and the memory model means the parked command could go on writing
// to a destination nobody chose. Moving the target behind a mutex the writer itself
// holds makes the restore safe while a parked command is still holding the writer —
// and its writes land in io.Discard, which is where an abandoned command's output
// belongs.
type streamSwitch struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *streamSwitch) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *streamSwitch) to(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w = w
}

// tofuOutputsHeader is the line OpenTofu and terraform print, at column 0, above the
// output values a plan changes. hack/check-tofu-destroy-outputs.sh reads this constant
// out of this file and fails CI if the pinned OpenTofu stops printing exactly this line
// on a destroy, which is the one way the filter below can quietly stop working.
const tofuOutputsHeader = "Changes to Outputs:"

// destroyOutputsNotShown replaces that block in a destroy's output.
const destroyOutputsNotShown = "Changes to Outputs: not shown. A destroy works these values out " +
	"without this instance's settings, so several of the values OpenTofu would print here " +
	"are the configuration's defaults, not this instance's."

// outputsBlockFilter passes a destroy's stdout through line by line, except the
// "Changes to Outputs:" block, which it replaces with destroyOutputsNotShown.
//
// 🔴 WHY THE BLOCK IS FALSE, NOT MERELY NOISY. A destroy first refreshes in normal mode,
// and that works every output out again from the configuration and the variables it was
// given. destroyOpenedInstanceRoot passes two, so every output that depends on anything
// else is printed at the configuration's default: one broker server for a three-server
// instance, the default backup path for a restored one. The resource lines above the
// block come from state and are true, which is why only the block goes. It goes whole
// rather than line by line because a default can equal the instance's value by chance,
// so no single line can be told apart as true.
//
// The block is the header line and every line after it that starts with a space,
// including a multi-line value's continuation lines. The first line that does not start
// with a space ends it and is printed; a blank line ends it too. An outputs-only destroy
// (a resumed one whose resources are already gone) follows the block with OpenTofu's
// "You can apply this plan to save these new output values" paragraph, which is kept
// although the list it refers to is not shown: cosmetic, and matching more of a third
// party's prose would be the more brittle trade.
//
// Write reports len(p) unless writing to the terminal fails: dropping a line is not a
// short write.
type outputsBlockFilter struct {
	w       io.Writer
	pending []byte // an incomplete last line, held until its newline or Flush
	inBlock bool
}

func newOutputsBlockFilter(w io.Writer) *outputsBlockFilter { return &outputsBlockFilter{w: w} }

func (f *outputsBlockFilter) Write(p []byte) (int, error) {
	f.pending = append(f.pending, p...)
	for {
		i := bytes.IndexByte(f.pending, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := f.pending[:i+1]
		f.pending = f.pending[i+1:]
		if err := f.line(line); err != nil {
			return 0, err
		}
	}
}

// Flush handles a last line that has no newline. Call it only once nothing can write
// any more; see Destroy.
func (f *outputsBlockFilter) Flush() error {
	if len(f.pending) == 0 {
		return nil
	}
	line := f.pending
	f.pending = nil
	return f.line(line)
}

func (f *outputsBlockFilter) line(line []byte) error {
	text := strings.TrimRight(string(line), "\r\n")
	if f.inBlock {
		if strings.HasPrefix(text, " ") {
			return nil
		}
		f.inBlock = false
	}
	if text == tofuOutputsHeader {
		f.inBlock = true
		_, err := io.WriteString(f.w, destroyOutputsNotShown+"\n")
		return err
	}
	_, err := f.w.Write(line)
	return err
}

// newTofuExec builds the runner for one root with stdout quiet and stderr streamed.
func newTofuExec(rootdir, tofuBin string) (*tofuExec, error) {
	tf, err := tfexec.NewTerraform(rootdir, tofuBin)
	if err != nil {
		return nil, err
	}
	stdout := &streamSwitch{w: io.Discard}
	tf.SetStdout(stdout)
	tf.SetStderr(os.Stderr)
	return &tofuExec{Terraform: tf, stdout: stdout, abandonBudget: tofuAbandonBudget}, nil
}

// tofuAbandonBudget is how long dcctl goes on waiting for an INTERRUPTED tofu command
// before it stops waiting and says so. It is armed only once the context is cancelled
// and never while a run is live, so a long legitimate apply is not on a clock.
//
// 🔴 IT MUST EXCEED tofuGracefulStopBudget, and not by a rounding error. Until that
// budget expires the interrupted tofu is legitimately still working — finishing the
// helm_release in flight and writing its state — and terraform-exec has not yet
// escalated to SIGKILL. A budget shorter than that would report a graceful stop still
// in progress as abandoned, which is the same wrong answer pointing the other way: it
// would tell an operator their infrastructure is in an unknown state while tofu was
// about to tell them exactly what it did. The extra minute is for the pipe to reach
// EOF after the SIGKILL lands.
const tofuAbandonBudget = tofuGracefulStopBudget + time.Minute

// errTofuAbandoned is what a cancelled progress command becomes when it never
// returned. It carries the operator sentence itself, because "apply failed" and
// "apply was left mid-flight" call for different next moves and only one of them is
// true here: dcctl stopped LOOKING, it did not stop the work.
var errTofuAbandoned = errors.New(
	"dcctl stopped waiting for the interrupted OpenTofu command. OpenTofu itself has " +
		"gone, but something it started — a provider plugin, most likely — outlived it and " +
		"is still holding the pipe dcctl reads its output from, so dcctl cannot see the " +
		"command end. It was asked to stop gracefully and to write its state before this " +
		"point and very probably did, but nothing here witnessed that: treat this " +
		"instance's infrastructure as PARTIALLY APPLIED rather than untouched. Re-run the " +
		"same command — the apply is idempotent and reconciles whatever was left half " +
		"done. Assuming nothing happened is the one reading that is not safe")

// runUntilAbandoned runs a tofu command and, if the context has been cancelled, stops
// waiting for it after budget.
//
// 🔴 THE TIMER IS ARMED ONLY AFTER ctx IS DONE. A budget armed at entry is a kill
// switch on every long apply, which is the opposite of what this is for: an apply that
// nobody interrupted is waited for forever, exactly as before.
//
// The abandoned goroutine is LEAKED on purpose. It is blocked in terraform-exec's
// ReadBytes on a pipe a third process holds open, so there is nothing to cancel and no
// way to unblock it; dcctl is a short-lived CLI on its way to exit, and one parked
// goroutine costs it nothing. The result channel is buffered for the same reason — a
// leaked goroutine that eventually DOES finish must be able to send and go away rather
// than block on a send nobody will ever receive.
func runUntilAbandoned(ctx context.Context, budget time.Duration, run func() error) error {
	res := make(chan error, 1)
	go func() { res <- run() }()

	select {
	case err := <-res:
		return err
	case <-ctx.Done():
	}

	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case err := <-res:
		return err
	case <-timer.C:
		// A result that arrived in the same instant the budget expired wins. Go picks
		// a ready select case at random, so without this a command that finished
		// exactly on the boundary would sometimes be reported as abandoned — telling
		// an operator their infrastructure is in an unknown state when the command had
		// just said what it did.
		select {
		case err := <-res:
			return err
		default:
		}
		return fmt.Errorf("%w (dcctl waited %s after the interrupt)", errTofuAbandoned, budget)
	}
}

// streaming runs a progress command with stdout on the terminal, so a long apply is
// not a silent wait.
//
// 🔴 THE VERSION IS READ FIRST, WHILE STDOUT IS STILL QUIET. terraform-exec runs
// `version -json` inside Init (and inside other commands for some options) the first
// time it needs the version, and that command's stdout is merged into the same
// writer — so without this the first Init prints a JSON document. The result is
// cached on the runner, so this costs one call per root, not one per command.
//
// 🔴 IT IS ALSO WHERE AN INTERRUPTED COMMAND IS GIVEN UP ON. See runUntilAbandoned
// and the WaitDelay paragraph in applyInfra for why terraform-exec cannot be relied
// on to return here at all. The cover is deliberately the four PROGRESS commands and
// not every tofu call: those are the ones that run for minutes, so those are the ones
// an operator interrupts, and they are the ones that leave a provider plugin running
// long enough to orphan the pipe. The version read above is outside it on purpose —
// `version -json` starts no providers, so it has nothing to orphan the pipe TO.
func (t *tofuExec) streaming(ctx context.Context, run func() error) error {
	return t.streamingTo(ctx, os.Stdout, run)
}

// streamingTo is streaming with stdout sent to w instead of straight to the terminal.
func (t *tofuExec) streamingTo(ctx context.Context, w io.Writer, run func() error) error {
	if _, _, err := t.Version(ctx, false); err != nil {
		return err
	}
	t.stdout.to(w)
	// Restored on the way out even when the command below is abandoned, so a read that
	// follows an abandoned apply still cannot print the state — and safely, because the
	// parked command holds the same writer rather than a field this is overwriting.
	defer t.stdout.to(io.Discard)
	return runUntilAbandoned(ctx, t.abandonBudget, run)
}

// Init initialises the root and moves its lock file onto the provider versions the
// embedded configuration pins.
//
// 🔴 -upgrade IS NOT OPTIONAL HERE, AND THERE IS NO WAY TO PASS AN OPTION THAT TURNS
// IT OFF. The lock file is machine-local (nothing ships one), so without -upgrade it
// freezes whatever the FIRST init on this machine resolved, and a dcctl that needs a
// newer provider then fails every command on every existing cluster with "locked
// provider ... does not match configured version constraint" -- install, upgrade and
// destroy alike. That was not hypothetical: the kubernetes provider a cluster first
// resolved (2.38.0) is the one that makes a failed install unrecoverable, so the
// clusters the newer pin exists for are exactly the ones whose lock holds the old one.
// The versions.tf pins are exact, so -upgrade selects exactly them and moves nothing
// the shipped configuration did not move.
//
// The cost is that every init asks the provider registry (or a configured mirror)
// which versions exist, including a destroy whose providers are already cached.
func (t *tofuExec) Init(ctx context.Context) error {
	return t.streaming(ctx, func() error { return t.Terraform.Init(ctx, tfexec.Upgrade(true)) })
}

func (t *tofuExec) Apply(ctx context.Context, opts ...tfexec.ApplyOption) error {
	return t.streaming(ctx, func() error { return t.Terraform.Apply(ctx, opts...) })
}

// Destroy streams like every progress command, except the plan's list of output
// values: see outputsBlockFilter. Apply keeps its list, because bootstrap and upgrade
// apply with the instance's own variables, so the values it prints are the instance's.
//
// 🔴 FLUSH ONLY AFTER streamingTo RETURNS. By then it has pointed the switch back at
// io.Discard under the switch's own mutex, so even an abandoned command still holding
// the writer can no longer reach the filter, and Flush is the filter's only user.
func (t *tofuExec) Destroy(ctx context.Context, opts ...tfexec.DestroyOption) error {
	f := newOutputsBlockFilter(os.Stdout)
	err := t.streamingTo(ctx, f, func() error { return t.Terraform.Destroy(ctx, opts...) })
	if ferr := f.Flush(); ferr != nil && err == nil {
		err = ferr
	}
	return err
}

func (t *tofuExec) StateRm(ctx context.Context, address string, opts ...tfexec.StateRmCmdOption) error {
	return t.streaming(ctx, func() error { return t.Terraform.StateRm(ctx, address, opts...) })
}
