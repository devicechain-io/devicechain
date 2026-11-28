// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/spf13/cobra"
)

// dcctl bootstrap local acme --ui=tui on a real pseudo-terminal: the real command, its
// real flags, presenter selection, instance screens, review, passphrase form and screens
// after the run, with fakes standing in only for the cluster work (the engine's Target
// and the pipeline's steps). The escrow plan is the engine's own: its passphrase is asked
// on the form, twice, and must reach the run exactly and never the terminal.

const bootstrapUIPTYChildEnv = "DCCTL_BOOTSTRAP_UI_PTY_CHILD"

// bootstrapUIPTYMarkerEnv names the file the child's fakes record what they saw in. They
// never record the passphrase itself, only whether it arrived.
const bootstrapUIPTYMarkerEnv = "DCCTL_BOOTSTRAP_UI_PTY_MARKER"

// TestBootstrapUIPTYChild is dcctl in the child: the bootstrap command, with fakes for the
// cluster work.
func TestBootstrapUIPTYChild(t *testing.T) {
	n := os.Getenv(bootstrapUIPTYChildEnv)
	if n == "" {
		return
	}
	var args []string
	for i := 0; ; i++ {
		a, ok := os.LookupEnv(fmt.Sprintf("%s_%d", bootstrapUIPTYChildEnv, i))
		if !ok {
			break
		}
		args = append(args, a)
	}
	marker := os.Getenv(bootstrapUIPTYMarkerEnv)
	mark := func(s string) {
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(f, s)
			_ = f.Close()
		}
	}
	pass := bootstrapCanaryPassphrase()
	Version = "v1.11.0"
	bootstrapRunFor = func(c *cobra.Command, a []string, observe func(*bootstrap.State, error)) (*bootstrap.BootstrapEngine, installflow.PreflightRunner, error) {
		e, pf, err := bootstrapRun(c, a, observe)
		if err != nil {
			return nil, nil, err
		}
		e.Target = func(_ context.Context, _ bootstrap.Console, plan bootstrap.EscrowPlan) (*bootstrap.State, error) {
			if plan.Passphrase == pass {
				mark("the passphrase reached the run")
			}
			mark("target " + e.Instance)
			// EscrowOutcome stands in for what the render step records once it has written
			// the artifact and opened it back; the stubbed pipeline below writes nothing.
			return &bootstrap.State{Instance: e.Instance, KubeContext: "kind-devicechain", Escrow: plan,
				EscrowOutcome: "written",
				Values:        map[string]string{"ingressHost": "devicechain.local", "scheme": "https"}}, nil
		}
		return e, pf, nil
	}
	bootstrapPipeline = func() bootstrap.Pipeline {
		var p bootstrap.Pipeline
		for _, s := range bootstrap.NewDefaultPipeline().Steps {
			p.Steps = append(p.Steps, bootstrap.Step{Name: s.Name, Run: func(context.Context, *bootstrap.State) error { return nil }})
		}
		p.Steps[0].Run = func(ctx context.Context, _ *bootstrap.State) error {
			// The journey test holds the fake run until it observes the progress
			// table. An instantaneous fake can otherwise finish between frames.
			for {
				_, err := os.Stat(marker + ".await-progress")
				if os.IsNotExist(err) {
					break
				}
				if err != nil {
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
			mark("pipeline ran")
			return nil
		}
		return p
	}
	rootCmd.SetArgs(args)
	Execute() // exits with the command's own status on an error
	os.Exit(0)
}

type bootstrapPTYRun struct {
	t      *testing.T
	m      *os.File
	cmd    *exec.Cmd
	marker string
	mu     sync.Mutex
	out    bytes.Buffer
	exited chan struct{}
	err    error
}

func startBootstrapUIPTY(t *testing.T, args ...string) *bootstrapPTYRun {
	t.Helper()
	m, s := openCmdPTY(t)
	r := &bootstrapPTYRun{t: t, m: m, marker: filepath.Join(t.TempDir(), "marker"), exited: make(chan struct{})}
	r.cmd = exec.Command(os.Args[0], "-test.run=^TestBootstrapUIPTYChild$", "-test.count=1")
	r.cmd.Env = []string{bootstrapUIPTYChildEnv + "=1", bootstrapUIPTYMarkerEnv + "=" + r.marker,
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TERM=xterm-256color", "LANG=C.UTF-8"}
	for i, a := range args {
		r.cmd.Env = append(r.cmd.Env, fmt.Sprintf("%s_%d=%s", bootstrapUIPTYChildEnv, i, a))
	}
	if d := os.Getenv("GOCOVERDIR"); d != "" {
		r.cmd.Env = append(r.cmd.Env, "GOCOVERDIR="+d)
	}
	r.cmd.Stdin, r.cmd.Stdout, r.cmd.Stderr = s, s, s
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := m.Read(buf)
			r.mu.Lock()
			r.out.Write(buf[:n])
			r.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() { r.err = r.cmd.Wait(); close(r.exited) }()
	t.Cleanup(func() {
		_ = r.cmd.Process.Kill()
		<-r.exited
	})
	return r
}

func (r *bootstrapPTYRun) screen() string { r.mu.Lock(); defer r.mu.Unlock(); return r.out.String() }

func (r *bootstrapPTYRun) markers() string { b, _ := os.ReadFile(r.marker); return string(b) }

func (r *bootstrapPTYRun) press(s string) {
	r.t.Helper()
	if _, err := r.m.Write([]byte(s)); err != nil {
		r.t.Fatal(err)
	}
}

// waitShows waits for the screen to have drawn s since the stream began.
func (r *bootstrapPTYRun) waitShows(what, s string) {
	r.t.Helper()
	r.waitUntil(what, func() bool { return strings.Contains(stripCSI(r.screen()), s) })
}

func (r *bootstrapPTYRun) waitUntil(what string, ok func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		select {
		case <-r.exited:
			if !ok() {
				r.t.Fatalf("dcctl exited (%v) while waiting for %s; terminal:\n%q", r.err, what, r.screen())
			}
		default:
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("waited for %s; terminal:\n%q", what, r.screen())
		}
	}
}

func (r *bootstrapPTYRun) exitCode() int {
	r.t.Helper()
	select {
	case <-r.exited:
	case <-time.After(30 * time.Second):
		r.t.Fatalf("dcctl did not exit; terminal:\n%q", r.screen())
	}
	var ee *exec.ExitError
	switch {
	case r.err == nil:
		return 0
	case errors.As(r.err, &ee):
		return ee.ExitCode()
	}
	r.t.Fatal(r.err)
	return -1
}

// stripCSI drops the terminal's control sequences, so text split by cursor moves and
// colours can be looked for whole.
func stripCSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// passphraseFormWords are the escrow passphrase form's first words.
const passphraseFormWords = "Choose the passphrase that protects the root-key escrow"

// settle waits out a review's quiet period: keys typed sooner are dropped by design.
func settle() { time.Sleep(900 * time.Millisecond) }

// 🔴 THE WHOLE INSTANCE JOURNEY UNDER dcctl bootstrap --ui=tui, ON A REAL TERMINAL. The
// review's approval, given only once it has settled, is what starts the run; a key typed
// the moment it appears approves nothing. The passphrase is asked on the form during the
// run, twice, reaches the run exactly, and never appears on the terminal. Exit 0, with the
// terminal handed back.
func TestBootstrapUIPTYTheInstanceJourney(t *testing.T) {
	r := startBootstrapUIPTY(t, "bootstrap", "local", "acme", "--ui=tui", "--skip-preflight")
	if err := os.WriteFile(r.marker+".await-progress", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.waitShows("the instance screen", "Choose the instance to create")
	if !strings.Contains(stripCSI(r.screen()), "(from the command line)") {
		t.Fatalf("the instance screen is not seeded with the command line's name:\n%q", r.screen())
	}
	r.press(strings.Repeat("\t", 7) + "\r") // name .. Continue
	r.waitShows("the escrow screen", "Root-key escrow")
	r.press(strings.Repeat("\t", 4) + "\r") // write .. Continue
	r.waitShows("the instance review", "Approve and create the instance")
	// Typed the moment the review appears: Tab to Approve, Enter. Neither may act.
	r.press("\t\r")
	time.Sleep(300 * time.Millisecond)
	// The run's first question is the passphrase: had the keys approved the review, the
	// form would be up by now.
	if m := r.markers(); m != "" || strings.Contains(stripCSI(r.screen()), passphraseFormWords) {
		t.Fatalf("a key typed before the review settled started the run: %q; terminal: %q", m, r.screen())
	}
	settle()
	r.press("\t\r") // Back -> Approve
	r.waitShows("the passphrase form", passphraseFormWords)
	pass := bootstrapCanaryPassphrase()
	r.press(pass)
	time.Sleep(300 * time.Millisecond) // drawn, if the form ever drew what was typed
	r.press("\r")
	r.waitShows("the confirmation", "Type the same passphrase again.")
	r.press(pass)
	time.Sleep(300 * time.Millisecond)
	r.press("\r")
	r.waitShows("the bootstrap stage table", "Creating the instance")
	if err := os.Remove(r.marker + ".await-progress"); err != nil {
		t.Fatal(err)
	}
	r.waitShows("the escrow receipt", "Root-key escrow written and checked")
	r.press("\r")
	r.waitShows("the instance success", "Instance acme is ready.")
	r.press("\r")
	if code := r.exitCode(); code != 0 {
		t.Fatalf("exit %d; terminal:\n%q", code, r.screen())
	}
	if m := r.markers(); m != "the passphrase reached the run\ntarget acme\npipeline ran\n" {
		t.Fatalf("the run saw %q", m)
	}
	out := r.screen()
	testkit.AssertNoCanary(t, "the terminal", []byte(out))
	if !strings.Contains(out, "Creating the instance") {
		t.Errorf("the progress screen never showed the bootstrap's stage table:\n%q", out)
	}
	end := strings.LastIndex(out, "dcctl bootstrap: succeeded")
	if end < 0 {
		t.Fatalf("no exit summary:\n%q", out)
	}
	if before := out[:end]; strings.LastIndex(before, "\x1b[?1049l") < strings.LastIndex(before, "\x1b[?1049h") {
		t.Errorf("the alternate screen was not left before the exit summary:\n%q", out)
	}
}

// Exit on the instance review starts nothing and changes nothing: the bootstrap's exit
// status for a run that did not happen is 1, as for any bootstrap that did not finish.
func TestBootstrapUIPTYExitOnTheReviewStartsNothing(t *testing.T) {
	r := startBootstrapUIPTY(t, "bootstrap", "local", "acme", "--ui=tui", "--skip-preflight", "--no-escrow")
	r.waitShows("the instance screen", "Choose the instance to create")
	r.press(strings.Repeat("\t", 7) + "\r")
	r.waitShows("the escrow screen", "Root-key escrow")
	r.press(strings.Repeat("\t", 3) + "\r") // none .. Continue
	r.waitShows("the instance review", "Approve and create the instance")
	settle()
	r.press("\t\t\r") // Back -> Approve -> Exit
	if code := r.exitCode(); code != 1 {
		t.Fatalf("exit %d; terminal:\n%q", code, r.screen())
	}
	if m := r.markers(); m != "" {
		t.Fatalf("the run started: %q", m)
	}
	if !strings.Contains(r.screen(), "the instance was not created") {
		t.Fatalf("leaving did not say nothing was created:\n%q", r.screen())
	}
}

// --ui=tui --yes is the progress-only screen for the command line's own run: no choices,
// no review, the passphrase still asked on the form and never drawn.
func TestBootstrapUIPTYProgressOnly(t *testing.T) {
	r := startBootstrapUIPTY(t, "bootstrap", "local", "acme", "--ui=tui", "--yes", "--skip-preflight")
	r.waitShows("the passphrase form", passphraseFormWords)
	if strings.Contains(stripCSI(r.screen()), "Choose the instance to create") {
		t.Fatal("a progress-only run showed the instance choices")
	}
	pass := bootstrapCanaryPassphrase()
	r.press(pass + "\r")
	r.waitShows("the confirmation", "Type the same passphrase again.")
	r.press(pass + "\r")
	if code := r.exitCode(); code != 0 {
		t.Fatalf("exit %d; terminal:\n%q", code, r.screen())
	}
	if m := r.markers(); m != "the passphrase reached the run\ntarget acme\npipeline ran\n" {
		t.Fatalf("the run saw %q", m)
	}
	testkit.AssertNoCanary(t, "the terminal", []byte(r.screen()))
	if !strings.Contains(r.screen(), "dcctl bootstrap: succeeded") {
		t.Fatalf("no exit summary:\n%q", r.screen())
	}
}
