// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installplain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/fatih/color"
)

// The escrow questions are asked in the words and layout the terminal prompt always
// used, and the answer is never written anywhere.
func TestSecretQuestionsAreAskedAsTheTerminalAskedThem(t *testing.T) {
	noColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = noColor }()
	for _, tc := range []struct {
		q    installflow.Question
		want string
	}{
		{installflow.Question{Kind: installflow.PromptSecret, Question: installflow.QuestionEscrowPassphrase,
			Params: []installflow.Param{{Name: "instance", Value: installflow.Text("acme")}}},
			"Passphrase to protect the root-key escrow for instance \"acme\"\n  Passphrase: \n"},
		{installflow.Question{Kind: installflow.PromptSecret, Question: "bootstrap.escrow-existing-passphrase",
			Params: []installflow.Param{{Name: "artifact", Value: installflow.Text("/k/acme.escrow")}}},
			"Passphrase of the existing escrow at /k/acme.escrow\n  Passphrase: \n"},
		{installflow.Question{Kind: installflow.PromptSecret, Question: installflow.QuestionEscrowPassphraseConfirm},
			"  Confirm:    \n"},
		{installflow.Question{Kind: installflow.PromptSecret, Question: installflow.QuestionEscrowRestorePassphrase,
			Params: []installflow.Param{{Name: "artifact", Value: installflow.Text("/k/acme.escrow")}}},
			"Passphrase for the root-key escrow /k/acme.escrow\n  Passphrase: \n"},
	} {
		var out bytes.Buffer
		resp, err := Responder{Out: &out, ReadHidden: func() ([]byte, error) { return []byte("typed-secret"), nil }}.
			Respond(context.Background(), tc.q)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != tc.want {
			t.Errorf("%s: wrote %q, want %q", tc.q.Question, out.String(), tc.want)
		}
		if string(resp.Secret.Expose()) != "typed-secret" {
			t.Errorf("%s: answer not returned", tc.q.Question)
		}
	}
}

// A read that fails says so as the terminal prompt did, after the newline it printed.
func TestASecretReadThatFailsSaysSo(t *testing.T) {
	var out bytes.Buffer
	_, err := Responder{Out: &out, ReadHidden: func() ([]byte, error) { return nil, errors.New("not a terminal") }}.
		Respond(context.Background(), installflow.Question{Kind: installflow.PromptSecret,
			Question: installflow.QuestionEscrowPassphraseConfirm})
	if err == nil || err.Error() != "reading the passphrase: not a terminal" {
		t.Fatalf("err = %v", err)
	}
	if out.String() != "  Confirm:    \n" {
		t.Fatalf("wrote %q", out.String())
	}
}

// The plain reveal sink shows the value at once, through the engine's own line, and
// spends the capability. An offer with no plain line is discarded, never shown.
func TestThePlainRevealSinkShowsAtOnceAndSpends(t *testing.T) {
	var out bytes.Buffer
	r := installflow.NewReveal([]byte("seed-value"))
	Reveals{W: &out}.Offer(installflow.RevealOffer{Reveal: r, PlainLine: func(w io.Writer, v []byte) {
		fmt.Fprintf(w, "password: %s\n", v)
	}})
	if out.String() != "password: seed-value\n" || !r.Consumed() {
		t.Fatalf("wrote %q, consumed %t", out.String(), r.Consumed())
	}
	bare := installflow.NewReveal([]byte("seed-value"))
	Reveals{W: &out}.Offer(installflow.RevealOffer{Reveal: bare})
	if !bare.Consumed() || out.Len() != len("password: seed-value\n") {
		t.Fatal("an offer with no plain line was shown or left unspent")
	}
}
