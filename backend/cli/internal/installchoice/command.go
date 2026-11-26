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

// Command contains only display-safe arguments. Template means credentials must
// be supplied privately before execution; its Argv never contains those values.
type Command struct {
	Argv     []string
	Line     string
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
			result.Argv = append(result.Argv, "--"+f.name+"="+value)
		}
	}
	// Provider is also input. It is currently an identifier, but keep the safety
	// rule at every rendered input boundary rather than trusting future callers.
	if secretFlagValue("provider", provider) {
		result.Argv[2] = privateValue
		result.Template = true
	}
	quoted := make([]string, len(result.Argv))
	for i, arg := range result.Argv {
		quoted[i] = quote(arg, shell)
	}
	result.Line = strings.Join(quoted, " ")
	if shell == PowerShell {
		// PowerShell requires the call operator for a quoted executable.
		result.Line = "& " + result.Line
	}
	return result, nil
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
