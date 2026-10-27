// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ExitReboot is the exit status by which the payload asks to be started again at once, the way a
// device asks to reboot (EX_TEMPFAIL).
const ExitReboot = 75

// Runner runs one slot's image to completion and returns its exit status. confirmed reports
// whether the slot has confirmed itself (so a watchdog can tell a healthy child from a hung one).
type Runner func(ctx context.Context, slot string, confirmed func() bool) (exitCode int, err error)

// Supervisor is the launcher: it decides which slot to run, runs it, and decides again when it exits.
type Supervisor struct {
	Root string
	Run  Runner
	Log  func(format string, args ...any)

	// Backoff bounds the pause before restarting a child that exited with an ordinary failure.
	// A reboot request and a pending slot's rollback decision are never delayed.
	MinBackoff, MaxBackoff time.Duration
	Sleep                  func(context.Context, time.Duration)
}

// Result of a supervised run.
type Result struct {
	// Launches lists the slots started, in order.
	Launches []string
}

// Init creates boot.json for a root that has none, with slot a confirmed. It refuses to if there
// is no image in slot a: a launcher with nothing to launch should say so, not loop.
func Init(root string) error {
	if _, _, err := Load(root); err == nil {
		return nil
	}
	if _, err := os.Stat(StatePath(root)); err == nil {
		return errors.New("boot.json exists but is unusable and there is no good copy; refusing to guess a slot")
	}
	if _, err := os.Stat(ImagePath(root, SlotA)); err != nil {
		return fmt.Errorf("no boot state and no image in slot a: %w", err)
	}
	return Save(root, State{Confirmed: SlotA})
}

// Loop runs until the context ends, the payload exits 0, or an unrecoverable state error occurs.
func (s *Supervisor) Loop(ctx context.Context, maxLaunches int) (Result, error) {
	var res Result
	backoff := s.MinBackoff
	if s.Log == nil {
		s.Log = func(string, ...any) {}
	}
	sleep := s.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	for ctx.Err() == nil && (maxLaunches <= 0 || len(res.Launches) < maxLaunches) {
		st, recovered, err := Load(s.Root)
		if err != nil {
			return res, err
		}
		if recovered {
			s.Log("boot.json was unreadable; continuing from the last good copy, confirmed slot %s", st.Confirmed)
		}
		d := Decide(st)
		if d.Rollback != nil {
			s.Log("slot %s did not confirm itself; rolling back to slot %s", d.Rollback.From, d.Rollback.To)
			if err := WriteRollback(s.Root, *d.Rollback); err != nil {
				return res, err
			}
		}
		if err := Save(s.Root, d.State); err != nil {
			return res, err
		}
		res.Launches = append(res.Launches, d.Launch)
		s.Log("starting slot %s", d.Launch)
		started := time.Now()
		code, err := s.Run(ctx, d.Launch, func() bool {
			cur, _, e := Load(s.Root)
			return e == nil && cur.Pending == ""
		})
		if ctx.Err() != nil {
			return res, nil
		}
		if err != nil {
			s.Log("slot %s could not be run: %v", d.Launch, err)
		}
		switch {
		case err == nil && code == 0:
			return res, nil
		case err == nil && code == ExitReboot:
			backoff = s.MinBackoff
			continue
		}
		// An ordinary failure. A pending slot is judged on the next turn of the loop without delay;
		// anything else backs off so a crash-looping payload does not spin.
		cur, _, e := Load(s.Root)
		if e == nil && cur.Pending != "" {
			continue
		}
		if time.Since(started) > 30*time.Second {
			backoff = s.MinBackoff
		}
		sleep(ctx, backoff)
		if backoff *= 2; backoff > s.MaxBackoff {
			backoff = s.MaxBackoff
		}
	}
	return res, nil
}

// ProcessRunner runs the slot's image as a child process. confirmTimeout bounds how long a
// freshly installed slot may run without confirming itself before it is killed; that is the
// watchdog that turns a hang into a rollback.
func ProcessRunner(root string, args []string, confirmTimeout time.Duration, extraEnv []string) Runner {
	return func(ctx context.Context, slot string, confirmed func() bool) (int, error) {
		st, _, _ := Load(root)
		watch := st.Pending == slot
		cmd := exec.CommandContext(ctx, ImagePath(root, slot), args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), append([]string{"OTA_SLOT=" + slot, "OTA_ROOT=" + root}, extraEnv...)...)
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		done := make(chan struct{})
		if watch && confirmTimeout > 0 {
			go func() {
				t := time.NewTimer(confirmTimeout)
				defer t.Stop()
				select {
				case <-done:
				case <-t.C:
					if !confirmed() {
						_ = cmd.Process.Kill()
					}
				}
			}()
		}
		err := cmd.Wait()
		close(done)
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
}
