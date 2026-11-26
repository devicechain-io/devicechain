// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// bcReached says whether a change has reached a named point. A PAUSED change is an error at any point:
// nothing in the drill should trip a threshold or a dependency, and a paused change would otherwise only
// time out, far from the reason.
func bcReached(c bcChange, until string) (bool, error) {
	if c.Dispatch == "PAUSED" {
		return false, fmt.Errorf("bulk change %s PAUSED (reason %s, hold %s, last error %s)",
			c.Token, strOr(c.PauseReason), strOr(c.HoldCode), strOr(c.LastErrorCode))
	}
	switch until {
	case "half-posted":
		return c.TargetCount > 0 && c.Progress.Posted*2 >= c.TargetCount, nil
	case "dispatched":
		return c.Dispatch == "DISPATCHED", nil
	case "closed":
		return c.Dispatch == "DISPATCHED" && c.Evidence == "CLOSED", nil
	}
	return false, fmt.Errorf("unknown point %q", until)
}

func bcWaitCmd(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange wait")
	var c connection
	c.bind(fs)
	var receipt, url, token, until string
	var timeout, every time.Duration
	bindSession(fs, &receipt, &url)
	fs.StringVar(&token, "token", "", "token of the bulk change (required)")
	fs.StringVar(&until, "until", "", "half-posted | dispatched | closed (required)")
	fs.DurationVar(&timeout, "timeout", 20*time.Minute, "give up after this long")
	fs.DurationVar(&every, "every", 3*time.Second, "poll interval")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if _, err := bcReached(bcChange{}, until); err != nil {
		return failWith(exitSetup, "--until: %w", err)
	}
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		var out struct {
			Change *bcChange `json:"change"`
		}
		if err := s.query(ctx, bcStatusDoc, map[string]any{"token": token}, &out); err != nil {
			return failWith(exitRefused, "bulk change %s: %w", token, err)
		}
		if out.Change == nil {
			return failWith(exitMissing, "no bulk change %s", token)
		}
		done, err := bcReached(*out.Change, until)
		if err != nil {
			return failWith(exitMismatch, "%w", err)
		}
		if done {
			return json.NewEncoder(stdout).Encode(out.Change)
		}
		if time.Now().After(deadline) {
			body, _ := json.Marshal(out.Change)
			return failWith(exitMismatch, "bulk change %s did not reach %s within %s; last read %s", token, until, timeout, body)
		}
		select {
		case <-ctx.Done():
			return failWith(exitSetup, "%w", ctx.Err())
		case <-time.After(every):
		}
	}
}

// bcRedactionCmd waits on the redaction of the given lines.
//
//	redacted    poll until every one is redacted; fail when the bound passes first.
//	unredacted  the repair control: poll for the WHOLE bound; fail the moment any one is redacted, and pass
//	            only when the bound passes with none redacted.
func bcRedactionCmd(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange redaction")
	var c connection
	c.bind(fs)
	var receipt, url, token, ordinals, expect, out string
	var within, every time.Duration
	bindSession(fs, &receipt, &url)
	fs.StringVar(&token, "token", "", "token of the bulk change (required)")
	fs.StringVar(&ordinals, "ordinals", "", "comma-separated ordinals whose devices were deleted (required)")
	fs.StringVar(&expect, "expect", "redacted", "redacted | unredacted")
	fs.StringVar(&out, "out", "", "write the last lines read here (required)")
	fs.DurationVar(&within, "within", 150*time.Second, "the bound")
	fs.DurationVar(&every, "every", 5*time.Second, "poll interval")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	ords, err := parseOrdinals(ordinals)
	if err != nil || len(ords) == 0 || out == "" || (expect != "redacted" && expect != "unredacted") {
		return failWith(exitSetup, "--ordinals (at least one), --out and --expect redacted|unredacted are required (%v)", err)
	}
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	started := time.Now()
	deadline := started.Add(within)
	for {
		lines, err := bcAllLines(ctx, s, token)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(lines)
		if werr := os.WriteFile(out, body, 0o600); werr != nil {
			return failWith(exitSetup, "write %s: %w", out, werr)
		}
		problems := judgeRedaction(lines, ords)
		switch {
		case expect == "redacted" && len(problems) == 0:
			fmt.Fprintf(stdout, "bulkchange redaction: line(s) %v redacted %.0fs after the wait began\n", ords, time.Since(started).Seconds())
			return nil
		case expect == "unredacted" && len(problems) != len(ords):
			return failWith(exitMismatch, "line(s) of %v were redacted %.0fs into a bound of %s, so the check would not have failed",
				ords, time.Since(started).Seconds(), within)
		}
		if time.Now().After(deadline) {
			if expect == "unredacted" {
				fmt.Fprintf(stdout, "bulkchange redaction: line(s) %v are still NOT redacted after %s, so the check fails here\n", ords, within)
				return nil
			}
			return failWith(exitMismatch, "not redacted within %s: %s", within, strings.Join(problems, "; "))
		}
		select {
		case <-ctx.Done():
			return failWith(exitSetup, "%w", ctx.Err())
		case <-time.After(every):
		}
	}
}
