// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installplain

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/fatih/color"
	"golang.org/x/term"
)

// Responder answers prompts on the terminal the way dcctl always has: the
// question in white with a "[y/N]" suffix, then one line read from In.
//
// 🔴 IT READS In WITHOUT WATCHING ctx, AS THE PROMPT ALWAYS HAS. The first
// interrupt cancels the run's context and the second ends the process; a
// prompt that gave up on the first would turn an interrupted question into a
// refusal, which is a change to what the command does, not to how it is drawn.
// The broker still reports the run as cancelled when the context is done by
// the time the answer arrives.
type Responder struct {
	Out io.Writer
	In  io.Reader
	// ReadHidden reads one line from the terminal with echo off, for a secret
	// question. Nil reads os.Stdin's terminal, as the escrow passphrase prompt always
	// has.
	ReadHidden func() ([]byte, error)
}

// Respond shows one question and reads its answer.
func (r Responder) Respond(_ context.Context, q installflow.Question) (installflow.Response, error) {
	if q.Kind == installflow.PromptSecret {
		return r.respondSecret(q)
	}
	if q.Kind != installflow.PromptConfirm {
		return installflow.Response{}, fmt.Errorf("installplain: %s prompts are not supported", q.Kind)
	}
	text, err := questionText(q)
	if err != nil {
		return installflow.Response{}, err
	}
	fmt.Fprint(r.Out, color.WhiteString("%s [y/N]: ", text))
	line, err := bufio.NewReader(r.In).ReadString('\n')
	if err != nil {
		// No terminal to answer, or nothing typed: today's default, no.
		return installflow.Response{}, installflow.ErrPromptUnavailable
	}
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "y", "yes":
		return installflow.Response{Confirmed: true}, nil
	}
	return installflow.Response{Confirmed: false}, nil
}

// questionText is the wording of each question this presenter can ask. A code
// with no wording is an error, never a question asked with no text.
func questionText(q installflow.Question) (string, error) {
	switch q.Question {
	case installflow.QuestionCreateKindCluster:
		cluster, ok := param(q.Params, "cluster")
		if !ok {
			return "", fmt.Errorf("installplain: question %s needs a cluster", q.Question)
		}
		return fmt.Sprintf("No kind cluster %q found. Create it now?", cluster), nil
	}
	return "", fmt.Errorf("installplain: no wording for question %s", q.Question)
}

func param(ps []installflow.Param, name string) (string, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p.Value.String(), true
		}
	}
	return "", false
}

// respondSecret asks a secret question exactly as `dcctl bootstrap` always asked for the
// escrow passphrase: the purpose in white on a line of its own, then an indented label,
// the answer read with echo off, and a newline (which the terminal did not echo).
//
// 🔴 IT JUDGES NOTHING. Whether the answer is empty, and whether a confirmation matches,
// are the engine's questions, and its errors say so in its own words. This only reads.
// The answer leaves here as an installflow.Secret and is never written anywhere.
func (r Responder) respondSecret(q installflow.Question) (installflow.Response, error) {
	var label string
	switch q.Question {
	case installflow.QuestionEscrowPassphrase:
		instance, ok := param(q.Params, "instance")
		if !ok {
			return installflow.Response{}, fmt.Errorf("installplain: question %s needs an instance", q.Question)
		}
		// The purpose goes through WhiteString as a format, exactly as it always did.
		fmt.Fprintf(r.Out, "%s\n", color.WhiteString(fmt.Sprintf("Passphrase to protect the root-key escrow for instance %q", instance)))
		label = "  Passphrase: "
	case installflow.QuestionEscrowRestorePassphrase:
		artifact, ok := param(q.Params, "artifact")
		if !ok {
			return installflow.Response{}, fmt.Errorf("installplain: question %s needs an artifact", q.Question)
		}
		fmt.Fprintf(r.Out, "%s\n", color.WhiteString(fmt.Sprintf("Passphrase for the root-key escrow %s", artifact)))
		label = "  Passphrase: "
	case installflow.QuestionEscrowPassphraseConfirm:
		label = "  Confirm:    "
	default:
		return installflow.Response{}, fmt.Errorf("installplain: no wording for question %s", q.Question)
	}
	fmt.Fprint(r.Out, label)
	read := r.ReadHidden
	if read == nil {
		read = readTerminalHidden
	}
	raw, err := read()
	fmt.Fprintln(r.Out)
	defer clear(raw)
	if err != nil {
		return installflow.Response{}, fmt.Errorf("reading the passphrase: %w", err)
	}
	return installflow.Response{Secret: installflow.NewSecret(raw)}, nil
}

// readTerminalHidden reads one line from os.Stdin's terminal with echo off.
func readTerminalHidden() ([]byte, error) { return term.ReadPassword(int(os.Stdin.Fd())) }
