// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"go/parser"
	"go/token"
	"testing"
)

// scanReadFixture runs the scan over one in-line source file holding whole function
// declarations. Driving the scanner rather than the repository assertion is what lets
// these tests read what it FOUND; an assertion-only harness can show a guard failing but
// not show it failing for the right reason — and, more to the point here, cannot show a
// guard staying quiet for the WRONG one.
func scanReadFixture(t *testing.T, decls string) []UnpacedReadLoop {
	t.Helper()
	src := "package fixture\n\n" + decls + "\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v\n%s", err, src)
	}
	return unpacedReadLoopsIn(fset, f)
}

// only asserts a single finding and returns it, so each test below reads as one claim.
func only(t *testing.T, found []UnpacedReadLoop, why string) UnpacedReadLoop {
	t.Helper()
	if len(found) != 1 {
		t.Fatalf("the guard found %d unpaced read loops, want 1 — %s: %+v", len(found), why, found)
	}
	return found[0]
}

func quiet(t *testing.T, found []UnpacedReadLoop, why string) {
	t.Helper()
	if len(found) != 0 {
		t.Fatalf("the guard flagged %d read loops, want 0 — %s: %+v", len(found), why, found)
	}
}

// 🔴 THE NEGATIVE CONTROL, and it is the only reason the rest of this file means
// anything: a check is worth nothing until it has been shown to fail. This is the exact
// shape that stands in the tree eight times today — log the error, wait a fixed interval,
// go round again, forever.
func TestTheGuardFiresOnALoopThatRetriesAReadErrorForever(t *testing.T) {
	found := scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rp.Reader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.ctx.Done():
				return
			}
			continue
		}
		rp.handle(msg)
	}
}`)
	u := only(t, found, "this is the unbounded retry the pacer exists to end")
	if u.Function != "runConsumer" {
		t.Errorf("reported function %q, want runConsumer", u.Function)
	}
	if u.Shape != "loop" {
		t.Errorf("reported shape %q, want loop — the retry is in this function's own for", u.Shape)
	}
}

// The same loop, paced. The counterweight to the control above: flagging every read loop
// would also "catch" all eight, and would make the guard impossible to satisfy.
func TestTheGuardIsQuietOnceThatLoopIsPaced(t *testing.T) {
	quiet(t, scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rp.Reader.HandleResponse(err)
			if rp.pacer().PauseAfterError(rp.ctx, err) {
				return
			}
			continue
		}
		rp.handle(msg)
	}
}`), "a paced loop is the fixed form, and the guard must accept it")
}

// 🔴 THE SHAPE THAT DEFEATED THE FIRST RULE I WROTE FOR THIS GUARD. An earlier version
// asked whether the loop body held a `continue`, which is how seven of the eight are
// written — and ResolvedEventsProcessor.readPump is the eighth. It retries by falling off
// the end of the body instead, so a continue-based rule reported it CLEAN while it held
// exactly the defect being scanned for.
//
// It is kept as a test rather than a note because the failure was a clean result, which is
// the failure mode a scanner has.
func TestTheGuardFiresOnALoopThatRetriesByFallingThroughWithNoContinue(t *testing.T) {
	found := scanReadFixture(t, `
func readPump(rp *P, items chan<- item) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		items <- item{msg: msg, err: err}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.ctx.Done():
				return
			}
		}
	}
}`)
	if u := only(t, found, "a loop with no continue still comes back for another read"); u.Function != "readPump" {
		t.Errorf("reported function %q, want readPump", u.Function)
	}
}

// 🔴 THE ONE THAT IS NOT A DEFECT, and the reason this guard cannot simply require a pacer
// at every read. ResolvedEventsProcessor.drainFactToHead returns its read error to a
// caller that aborts the catch-up, so no error ever leads back to another read. There is
// no run of failures to bound, and "fixing" it would mean ADDING a retry in order to
// bound it.
func TestTheGuardIsQuietOnALoopThatFailsClosedOnAReadError(t *testing.T) {
	quiet(t, scanReadFixture(t, `
func drainToHead(rp *P, reader messaging.MessageReader) error {
	for {
		msg, err := reader.ReadMessage(rp.ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return fmt.Errorf("catch-up read: %w", err)
			}
			break
		}
		rp.apply(msg)
	}
	return nil
}`), "a loop that leaves on every read error retries nothing")
}

// 🔴 WHY THE CATCH-ALL GUARD IS REQUIRED AND NOT INFERRED. A loop carrying only an
// io.EOF guard leaves on EOF and falls PAST it on everything else, straight into the
// handler with a zero-value message, at whatever rate the reader returns the error. That
// is the worst case this guard exists to catch — and a rule that only asked "does every
// guard exit?" would have called it clean, because the one guard present does exit.
func TestTheGuardFiresWhenOnlyEOFIsHandledAndEveryOtherErrorFallsThrough(t *testing.T) {
	found := scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		if errors.Is(err, io.EOF) {
			return
		}
		rp.handle(msg)
	}
}`)
	only(t, found, "an unhandled non-EOF error is a hot spin, not an exit")
}

// A read whose error is discarded cannot be shown to leave the loop, so it is reported.
// The fail-closed direction is deliberate: silence here would be a guard declining to
// answer about the least readable loop in the tree.
func TestTheGuardFiresWhenTheReadErrorIsDiscarded(t *testing.T) {
	only(t, scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, _ := rp.Reader.ReadMessage(rp.ctx)
		rp.handle(msg)
	}
}`), "an unnamed error cannot be shown to exit")
}

// 🔴 A BARE break LEAVES WHATEVER IS INNERMOST, and inside a select that is the select.
// Reading it as an exit would exempt a loop that is still retrying — and an exemption is
// the one kind of mistake a guard cannot report, because its output is silence.
//
// The pair is the point: the SAME guard, the SAME break, differing only in whether a
// select sits between it and the loop.
func TestABreakCountsAsAnExitOnlyWhereNoSelectOrSwitchIntervenes(t *testing.T) {
	t.Run("at the loop's own level it leaves the loop", func(t *testing.T) {
		quiet(t, scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		if err != nil {
			rp.Reader.HandleResponse(err)
			break
		}
		rp.handle(msg)
	}
}`), "a break at the loop's own level ends the loop")
	})

	t.Run("inside a select it leaves only the select", func(t *testing.T) {
		only(t, scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		select {
		case <-rp.ctx.Done():
			return
		default:
			if err != nil {
				rp.Reader.HandleResponse(err)
				break
			}
		}
		rp.handle(msg)
	}
}`), "this break ends the select and the loop reads again")
	})
}

// A labelled break names its loop, so it leaves it wherever it is written.
func TestALabelledBreakCountsAsAnExit(t *testing.T) {
	quiet(t, scanReadFixture(t, `
func runConsumer(rp *P) {
reading:
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		select {
		case <-rp.ctx.Done():
			return
		default:
			if err != nil {
				break reading
			}
		}
		rp.handle(msg)
	}
}`), "a labelled break names the loop it ends")
}

// 🔴 THE PACER MUST BE IN THE LOOP THAT READS, not merely somewhere in the file's
// function. A processor that grew a second read loop beside a paced one would otherwise
// read as clean — the first loop's call answering for the second's absence — which is how
// a guard ends up certifying the very site that was added after it.
func TestASecondUnpacedLoopIsNotCoveredByAPacedOneInTheSameFunction(t *testing.T) {
	found := scanReadFixture(t, `
func runBoth(rp *P) {
	go func() {
		for {
			msg, err := rp.Primary.ReadMessage(rp.ctx)
			if err != nil {
				if rp.pacer().PauseAfterError(rp.ctx, err) {
					return
				}
				continue
			}
			rp.handle(msg)
		}
	}()
	for {
		msg, err := rp.Secondary.ReadMessage(rp.ctx)
		if err != nil {
			continue
		}
		rp.handle(msg)
	}
}`)
	only(t, found, "the unpaced second loop must be reported on its own")
}

// 🔴 THE HELPER SHAPE, which is six of the eleven paced readers in the tree: the read
// lives in a function with no loop of its own and the caller loops over it. The retry
// decision is therefore in code this scan cannot see, so the pacer is required
// unconditionally.
func TestAReaderWithNoLoopOfItsOwnMustPaceBecauseItsCallerLoops(t *testing.T) {
	t.Run("unpaced", func(t *testing.T) {
		u := only(t, scanReadFixture(t, `
func (sp *P) ProcessMessage(ctx context.Context) bool {
	msg, err := sp.Reader.ReadMessage(ctx)
	if errors.Is(err, io.EOF) {
		return true
	}
	if err != nil {
		sp.Reader.HandleResponse(err)
		return false
	}
	return sp.handle(msg)
}`), "the loop is in a caller, so the bound has to be here")
		if u.Shape != "helper" {
			t.Errorf("reported shape %q, want helper", u.Shape)
		}
	})

	t.Run("paced", func(t *testing.T) {
		quiet(t, scanReadFixture(t, `
func (sp *P) ProcessMessage(ctx context.Context) bool {
	msg, err := sp.Reader.ReadMessage(ctx)
	if errors.Is(err, io.EOF) {
		return true
	}
	if err != nil {
		sp.Reader.HandleResponse(err)
		return sp.pacer().PauseAfterError(ctx, err)
	}
	return sp.handle(msg)
}`), "this is the shape all six paced helpers use")
	})
}

// A loop that ends the process instead of pacing is bounded, by the bluntest means
// available. This is not a shape the tree uses at a read, and it is accepted because
// refusing it would push someone toward the silent alternative.
func TestALoopThatEndsTheProcessOnAReadErrorIsAccepted(t *testing.T) {
	quiet(t, scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("unreadable")
		}
		rp.handle(msg)
	}
}`), "a process that exits is not retrying")
}

// 🔴 AN EXIT ON ONE KIND OF ERROR IS NOT AN EXIT. A guard whose body only re-tests the
// error and leaves on SOME of it still comes back round on the rest — here a non-EOF
// error falls past both ifs into the handler with a zero-value message, forever. The
// nested if therefore counts as leaving the loop only when it has an else that leaves it
// too.
//
// This shape had no test until a mutation that deleted the else requirement survived.
func TestANestedGuardWithNoElseDoesNotCountAsLeavingTheLoop(t *testing.T) {
	only(t, scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
		}
		rp.handle(msg)
	}
}`), "every other error falls through to the handler and round again")

	quiet(t, scanReadFixture(t, `
func runConsumer(rp *P) {
	for {
		msg, err := rp.Reader.ReadMessage(rp.ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			} else {
				return
			}
		}
		rp.handle(msg)
	}
}`), "with both arms leaving, no error leads back to another read")
}
