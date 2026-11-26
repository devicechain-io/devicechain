// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/fatih/color"
)

// console is where a run's human-readable output goes: the doing/done lines, the
// dry-run plan, the headings, and the streamed output of the tools it runs.
//
// 🔴 IT IS HANDED TO THE CODE THAT PRINTS, NOT REACHED THROUGH os.Stdout. An install
// run gets it from its presenter (installflow.Runtime.Console) and carries it in its
// context, so a presenter that draws its own screen can take the same words as log
// lines instead of having them scribbled over its frame. Paths not yet run through an
// engine — bootstrap, upgrade, destroy — use stdConsole, which resolves os.Stdout and
// os.Stderr at each write, exactly as the fmt.Print calls it replaced did.
type console struct {
	out io.Writer // nil: os.Stdout, resolved at write time
	err io.Writer // nil: os.Stderr, resolved at write time

	children func() (io.Writer, io.Writer)
}

// stdConsole is the process's own streams.
var stdConsole = console{}

func consoleOf(c installflow.Console) console {
	return console{out: c.Stdout, err: c.Stderr, children: c.ChildStreams}
}

func (c console) commandStreams() (io.Writer, io.Writer) {
	if c.children != nil {
		return c.children()
	}
	return c.stdout(), c.stderr()
}

func (c console) stdout() io.Writer {
	if c.out == nil {
		return os.Stdout
	}
	return c.out
}

func (c console) stderr() io.Writer {
	if c.err == nil {
		return os.Stderr
	}
	return c.err
}

func (c console) println(a ...any)               { fmt.Fprintln(c.stdout(), a...) }
func (c console) printf(format string, a ...any) { fmt.Fprintf(c.stdout(), format, a...) }
func (c console) print(a ...any)                 { fmt.Fprint(c.stdout(), a...) }
func (c console) doing(msg string)               { c.print(color.WhiteString("%s... ", msg)) }
func (c console) done()                          { c.println(color.GreenString("done.")) }
func (c console) wouldDo(msg string)             { c.println(color.YellowString("[dry-run] would %s", msg)) }
func (c console) fail(what string, err error) error {
	c.println(color.RedString("failed."))
	return fmt.Errorf("%s: %w", what, err)
}

// runStreamed frames a step whose tool streams its own progress: a heading, the
// work, then an indented status line.
func (c console) runStreamed(heading, label string, work func() error) error {
	c.println(color.WhiteString("%s:", heading))
	if err := work(); err != nil {
		return c.fail(label, err)
	}
	c.print("  ")
	c.doing(label)
	c.done()
	return nil
}

type runKey struct{}

// withRun carries an engine run, and with it the run's console, prompts and stage
// reporting, into the code below the engine. Install's callees are shared with
// bootstrap and upgrade and nearly all take a context; the run rides on it rather
// than on a parameter threaded through every one of them.
func withRun(ctx context.Context, r *installRun) context.Context {
	return context.WithValue(ctx, runKey{}, r)
}

// runFrom returns the engine run ctx carries, or nil outside one. Every method of
// *installRun is safe on nil, so code shared with the paths that run no engine
// calls it unconditionally.
func runFrom(ctx context.Context) *installRun {
	r, _ := ctx.Value(runKey{}).(*installRun)
	return r
}

// out is the console for ctx: the engine run's, or the process's own streams.
func out(ctx context.Context) console {
	if r := runFrom(ctx); r != nil {
		return r.con
	}
	return stdConsole
}
