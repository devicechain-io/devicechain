// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installplain

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/fatih/color"
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
}

// Respond shows one question and reads its answer.
func (r Responder) Respond(_ context.Context, q installflow.Question) (installflow.Response, error) {
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
