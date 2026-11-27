// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"reflect"
	"strings"
	"testing"
)

// ps51Native is what dcctl.exe receives when Windows PowerShell 5.1 runs it with argv
// (the arguments as PowerShell itself parsed them: every single-quoted literal already
// decoded). It applies 5.1's legacy native-argument rules to build the process's
// command line, then splits that line as the Microsoft C runtime does (the
// CommandLineToArgvW rules Go's os.Args follows on Windows).
//
// 5.1 builds the command line:
//   - an empty argument is dropped;
//   - an argument containing a space or a tab is wrapped in double quotes;
//   - nothing is escaped: an embedded double quote, or backslashes before the closing
//     quote it added, reach the program raw.
//
// The C runtime splits it:
//   - whitespace outside quotes separates arguments;
//   - a double quote toggles quoting and is not kept; inside quotes, two double
//     quotes in a row are one literal quote;
//   - 2n backslashes before a quote are n backslashes and the quote does its work;
//     2n+1 are n backslashes and a literal quote; backslashes not before a quote
//     are literal.
func ps51Native(argv []string) []string {
	var parts []string
	for _, a := range argv {
		switch {
		case a == "":
		case strings.ContainsAny(a, " \t"):
			parts = append(parts, `"`+a+`"`)
		default:
			parts = append(parts, a)
		}
	}
	return msvcrtSplit(strings.Join(parts, " "))
}

func msvcrtSplit(line string) []string {
	var out []string
	var cur strings.Builder
	inArg, quoted := false, false
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == '\\':
			n := 0
			for i < len(line) && line[i] == '\\' {
				n++
				i++
			}
			if i < len(line) && line[i] == '"' {
				cur.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					cur.WriteByte('"')
					i++
				}
			} else {
				cur.WriteString(strings.Repeat(`\`, n))
			}
			inArg = true
		case c == '"':
			if quoted && i+1 < len(line) && line[i+1] == '"' {
				cur.WriteByte('"')
				i += 2
			} else {
				quoted = !quoted
				i++
			}
			inArg = true
		case (c == ' ' || c == '\t') && !quoted:
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
			i++
		default:
			cur.WriteByte(c)
			inArg = true
			i++
		}
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out
}

// The model of 5.1 above reproduces its documented failures: the reason a value that
// holds a double quote, or a wrapped one ending in a backslash, is never rendered as a
// runnable PowerShell command.
func TestPS51ModelReproducesTheKnownFailures(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want []string
	}{
		{[]string{"plain"}, []string{"plain"}},
		{[]string{"with space"}, []string{"with space"}},
		{[]string{`--alert-email="Ops, Team" <ops@example.test>`}, []string{"--alert-email=Ops,", "Team <ops@example.test>"}},
		{[]string{`--alert-email=""`}, []string{"--alert-email="}},
		{[]string{`C:\dir with space\`, "next"}, []string{`C:\dir with space" next`}},
		{[]string{"", "after"}, []string{"after"}},
	} {
		if got := ps51Native(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ps51Native(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
