// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"
)

// commandsWithExamples lists the commands whose --help must carry real, copy-pasteable
// examples. It grows one slice at a time; a command added here without an Example, or with
// one that names a flag the command does not accept, fails the test.
//
// 🔴 AN EXAMPLE IS ONLY WORTH PRINTING IF IT RUNS. Every `dcctl` line is checked by the same
// gate the published docs go through (checkDcctl): the command resolves, every flag exists
// and the arguments match the command's arity. It does not prove a value parses.
var commandsWithExamples = []string{
	"dcctl attr set",
	"dcctl attr get",
	"dcctl attr unset",
	"dcctl dead-letters list",
	"dcctl tenant-deletion status",
}

func TestCommandsListedHaveRealExamples(t *testing.T) {
	for _, path := range commandsWithExamples {
		c, rest, err := rootCmd.Find(strings.Fields(strings.TrimPrefix(path, "dcctl ")))
		if err != nil || len(rest) != 0 || c.CommandPath() != path {
			t.Errorf("%s: not a command in the tree (found %v, rest %v, err %v)", path, c, rest, err)
			continue
		}
		if strings.TrimSpace(c.Example) == "" {
			t.Errorf("%s: no Example", path)
			continue
		}
		// Join backslash-continued lines, as a shell would.
		joined := strings.ReplaceAll(c.Example, "\\\n", " ")
		checked := 0
		for _, line := range strings.Split(joined, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			for _, seg := range splitSegments(line) {
				words := commandWords(seg)
				if len(words) == 0 || !isDcctl(words[0]) {
					continue
				}
				if !strings.HasPrefix(strings.Join(words, " "), path) {
					t.Errorf("%s: Example line is for another command: %s", path, line)
				}
				problem, skipped := checkDcctl(rootCmd, words[1:])
				if problem != "" {
					t.Errorf("%s: Example %q does not match the command: %s", path, line, problem)
				}
				if !skipped {
					checked++
				}
			}
		}
		if checked == 0 {
			t.Errorf("%s: Example holds no checkable dcctl invocation", path)
		}
	}
}

// The check must be able to fail: a flag the command lacks is reported, so a passing run is
// not the absence of a check.
func TestExampleCheckRejectsAnInventedFlag(t *testing.T) {
	problem, _ := checkDcctl(rootCmd, strings.Fields("attr get device d1 --no-such-flag"))
	if problem == "" {
		t.Fatal("an invented flag was accepted; the example check cannot fail")
	}
}
