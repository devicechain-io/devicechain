// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// stdoutCap bounds what is held in memory from a command. `show -json` of a plan is the
	// largest legitimate output; anything beyond the cap fails the command rather than truncating
	// JSON into something that parses wrong.
	stdoutCap = 32 << 20
	// tailCap is the one diagnostic returned from a failed command: the end of stderr.
	tailCap = 2048
)

// minimalEnv is the part of the parent environment a child may inherit: where to find programs and
// a temp dir. Nothing else crosses: not TF_*, not any cloud credential, not the manager's own.
func minimalEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "SYSTEMROOT", "TEMP", "TMP", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// ExitError is a tofu command that ended non-zero. Its message carries a scrubbed tail of stderr
// and never stdout (which may hold plan or state JSON).
type ExitError struct {
	Command string // the subcommand, no arguments
	Code    int
	Tail    string
}

func (e *ExitError) Error() string {
	if e.Tail == "" {
		return fmt.Sprintf("tofurun: tofu %s exited %d", e.Command, e.Code)
	}
	return fmt.Sprintf("tofurun: tofu %s exited %d: %s", e.Command, e.Code, e.Tail)
}

type capBuffer struct {
	buf      bytes.Buffer
	cap      int
	overflow bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.cap {
		c.overflow = true
		return len(p), nil // keep draining so the child never blocks on a full pipe
	}
	return c.buf.Write(p)
}

type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 8*tailCap {
		t.b = t.b[len(t.b)-8*tailCap:]
	}
	return len(p), nil
}

// scrub replaces every secret value (4+ bytes) in s.
func scrub(s string, secrets []string) string {
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// exec runs tofu with the session's environment. okCodes are exit codes treated as success
// (`plan -detailed-exitcode` answers 2 for "changes").
func (s *Session) exec(ctx context.Context, sub string, args []string, capture bool, okCodes ...int) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, s.bin, append([]string{sub}, args...)...)
	cmd.Dir = s.moduleDir
	cmd.Env = s.env
	stdout := &capBuffer{cap: 4096}
	if capture {
		stdout.cap = stdoutCap
	}
	stderr := &tailBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// A cancelled context kills tofu; a provider or provisioner it spawned may still hold the
	// output pipes, and without a bound Run would wait on them for as long as they live.
	cmd.WaitDelay = 15 * time.Second
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return nil, -1, fmt.Errorf("tofurun: start tofu %s: %w", sub, scrubErr(err, s.secrets))
		}
		code = ee.ExitCode()
		ok := false
		for _, c := range okCodes {
			if c == code {
				ok = true
			}
		}
		if !ok {
			tail := string(stderr.b)
			if len(tail) > tailCap {
				tail = tail[len(tail)-tailCap:]
			}
			return nil, code, &ExitError{Command: sub, Code: code, Tail: strings.TrimSpace(scrub(tail, s.secrets))}
		}
	}
	if stdout.overflow {
		return nil, code, fmt.Errorf("tofurun: tofu %s produced more than the output cap", sub)
	}
	if !capture {
		return nil, code, nil
	}
	return stdout.buf.Bytes(), code, nil
}

func scrubErr(err error, secrets []string) error {
	return errors.New(scrub(err.Error(), secrets))
}
