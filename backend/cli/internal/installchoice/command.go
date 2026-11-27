// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installchoice

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/url"
	"strings"
)

type Shell uint8

const (
	POSIX Shell = iota
	PowerShell
)

const privateValue = "<redacted: supply privately>"

// powerShellValue stands for a value Windows PowerShell 5.1 cannot hand a native
// program exactly (see nativeSafe). The command is then a template.
const powerShellValue = "<value PowerShell cannot pass exactly: supply it yourself>"

// nativeSafe reports whether Windows PowerShell 5.1 passes arg to a native
// program (dcctl.exe) exactly. 5.1 builds the program's command line from its
// parsed arguments by its legacy rules, and the program splits that line by the
// Microsoft C runtime's:
//
//   - an argument containing whitespace is wrapped in double quotes, and nothing
//     inside it is escaped;
//   - an embedded double quote is passed through raw, so the runtime reads it as
//     the start or end of a quoted span and drops it (or splits the argument);
//   - backslashes are passed through, and a run of them before the closing quote
//     the wrapping added escapes that quote;
//   - an empty argument is dropped altogether.
//
// So an argument is passed exactly when it is not empty, holds no double quote,
// and, when it is wrapped, does not end in a backslash. PowerShell 7.3 and later
// escape correctly, but the command must be right in 5.1 too, so anything else is
// never rendered as runnable.
func nativeSafe(arg string) bool {
	if arg == "" || strings.Contains(arg, `"`) {
		return false
	}
	return !(strings.ContainsAny(arg, " \t") && strings.HasSuffix(arg, `\`))
}

// Command contains only display-safe arguments. Template means credentials must
// be supplied privately before execution; its Argv never contains those values.
type Command struct {
	Argv []string
	Line string
	// Words are Line's words exactly as they appear in it, quotes and all, in
	// order: joined with single spaces they are Line. A screen that wraps the
	// command breaks it only between them.
	Words    []string
	Template bool
}

// Command reproduces the plan's choices and explicitness. approved adds --yes
// to reproduce review approval (overriding an explicit --yes=false). Without
// approval it preserves --yes exactly, for a lossless CLI round trip. UI flags
// are never included. Flags have stable alphabetical order; repeat order stays
// unchanged. Credential-bearing values produce a labelled template instead.
func (p Plan) Command(shell Shell, approved bool) (Command, error) {
	if shell != POSIX && shell != PowerShell {
		return Command{}, fmt.Errorf("unsupported command shell")
	}
	c := p.choices.Clone()
	if approved {
		c.AssumeYes = true
		c.Given["yes"] = true
	}
	provider := c.Provider
	if provider == "" {
		provider = "local"
	}
	result := Command{Argv: []string{"dcctl", "install", provider}}
	for _, f := range c.flagValues() {
		if !c.Given.Has(f.name) {
			continue
		}
		values := f.values
		emptySlice := f.slice && len(values) == 0
		if emptySlice {
			values = []string{""}
		}
		for _, value := range values {
			if secretFlagValue(f.name, value) {
				value = privateValue
				result.Template = true
			}
			if f.slice && !emptySlice {
				// pflag.StringSlice uses CSV, even for a single repeated value.
				var b bytes.Buffer
				w := csv.NewWriter(&b)
				if err := w.Write([]string{value}); err != nil {
					return Command{}, fmt.Errorf("cannot encode install choice")
				}
				w.Flush()
				value = strings.TrimSuffix(b.String(), "\n")
				if value == "" {
					value = `""`
				}
			}
			arg := "--" + f.name + "=" + value
			if shell == PowerShell && !nativeSafe(arg) {
				arg = "--" + f.name + "=" + powerShellValue
				result.Template = true
			}
			result.Argv = append(result.Argv, arg)
		}
	}
	// Provider is also input. It is currently an identifier, but keep the safety
	// rule at every rendered input boundary rather than trusting future callers.
	if secretFlagValue("provider", provider) {
		result.Argv[2] = privateValue
		result.Template = true
	}
	quoted := make([]string, 0, len(result.Argv)+1)
	if shell == PowerShell {
		// PowerShell requires the call operator for a quoted executable.
		quoted = append(quoted, "&")
	}
	for _, arg := range result.Argv {
		quoted = append(quoted, quote(arg, shell))
	}
	result.Words = quoted
	result.Line = strings.Join(quoted, " ")
	return result, nil
}

// Command is Plan.Command for a request settled only as far as Prepare takes it:
// the same choices and explicitness, without reading any alert file. A screen
// that reviews a plan before its run shows this.
func (p Prepared) Command(shell Shell, approved bool) (Command, error) {
	return Plan{choices: p.choices}.Command(shell, approved)
}

func quote(value string, shell Shell) string {
	if shell == PowerShell {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func secretFlagValue(name, value string) bool {
	if value == "" {
		return false
	}
	switch name {
	case "alert-webhook", "alert-watchdog-webhook":
		return true
	case "alert-config", "alert-smtp-password-file", "backup-credentials-file":
		// These are protected-file REFERENCES. Never open them in a preview.
		return false
	}
	// Redact the whole URL, including path tokens, whenever it carries userinfo,
	// query parameters or a fragment. Query keys need not be called "token".
	for _, word := range strings.Fields(value) {
		u, err := url.Parse(word)
		if err != nil && strings.Contains(word, "://") {
			return true
		}
		if err == nil && (u.Scheme != "" || u.Host != "") &&
			(u.User != nil || u.RawQuery != "" || u.Fragment != "") {
			return true
		}
	}
	return false
}
