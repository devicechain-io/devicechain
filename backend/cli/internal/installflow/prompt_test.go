// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

func startedFixture(t *testing.T) *fixture {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	return f
}

func finishAndWait(t *testing.T, f *fixture) []installflow.Envelope {
	t.Helper()
	s := okSummary()
	s.Outcome = installflow.RunFailed
	_ = f.hub.Finish(s)
	envs := f.wait(t)
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatal(err)
	}
	return envs
}

func dispositions(envs []installflow.Envelope) []installflow.PromptDisposition {
	var out []installflow.PromptDisposition
	for _, e := range envs {
		if p, ok := e.Payload.(installflow.PromptResolved); ok {
			out = append(out, p.Disposition)
		}
	}
	return out
}

func TestYesAnswersOnlyTheConfirmationsItControls(t *testing.T) {
	f := startedFixture(t)
	r := testkit.NewScriptedResponder()
	b := installflow.NewBroker(f.hub, r, true)
	ctx := context.Background()

	ans, err := b.Ask(ctx, installflow.Prompt{Kind: installflow.PromptConfirm, Question: "q.create", YesResolvable: true})
	if err != nil || ans.Disposition != installflow.PromptAutoConfirmed || !ans.Confirmed {
		t.Fatalf("%+v %v", ans, err)
	}
	if len(r.Questions()) != 0 {
		t.Fatal("--yes asked the responder anyway")
	}
	// Not a confirmation --yes controls today: asked, and here nobody answers.
	if _, err := b.Ask(ctx, installflow.Prompt{Kind: installflow.PromptConfirm, Question: "q.other"}); !errors.Is(err, installflow.ErrPromptUnavailable) {
		t.Fatalf("unrelated confirm: %v", err)
	}
	// --yes never supplies a secret, even one marked resolvable.
	if _, err := b.Ask(ctx, installflow.Prompt{Kind: installflow.PromptSecret, Question: "q.passphrase", YesResolvable: true}); !errors.Is(err, installflow.ErrPromptUnavailable) {
		t.Fatalf("secret: %v", err)
	}
	got := dispositions(finishAndWait(t, f))
	want := []installflow.PromptDisposition{installflow.PromptAutoConfirmed, installflow.PromptUnavailable, installflow.PromptUnavailable}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("dispositions %v", got)
	}
}

func TestResolutionCarriesNoAnswer(t *testing.T) {
	f := startedFixture(t)
	secret := "correct horse battery staple"
	r := testkit.NewScriptedResponder(
		testkit.Reply{Response: installflow.Response{Secret: installflow.NewSecret([]byte(secret))}},
		testkit.Reply{Response: installflow.Response{Choice: "compact"}},
		testkit.Reply{Response: installflow.Response{Confirmed: false}},
	)
	b := installflow.NewBroker(f.hub, r, false)
	ctx := context.Background()
	ans, err := b.Ask(ctx, installflow.Prompt{Kind: installflow.PromptSecret, Question: "q.passphrase"})
	if err != nil || ans.Disposition != installflow.PromptProvided || string(ans.Secret.Expose()) != secret {
		t.Fatalf("secret answer %v %v", ans.Disposition, err)
	}
	ans, err = b.Ask(ctx, installflow.Prompt{Kind: installflow.PromptChoice, Question: "q.footprint", Choices: []string{"full", "compact"}})
	if err != nil || ans.Choice != "compact" {
		t.Fatalf("choice %+v %v", ans, err)
	}
	if _, err := b.Ask(ctx, installflow.Prompt{Kind: installflow.PromptConfirm, Question: "q.go"}); !errors.Is(err, installflow.ErrPromptDeclined) {
		t.Fatalf("declined: %v", err)
	}
	// The secret answer is now redacted from anything the run prints.
	must(t, f.hub.Emit(installflow.Event{Payload: installflow.LogLine{Level: installflow.LogInfo, Source: "tool",
		Text: installflow.Text("echo " + secret)}}))
	envs := finishAndWait(t, f)
	for _, e := range envs {
		raw, err := json.Marshal(e)
		must(t, err)
		if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "compact") {
			t.Fatalf("an answer reached the stream: %s", raw)
		}
		if e.Kind == installflow.KindPromptResolved {
			var p map[string]any
			must(t, json.Unmarshal(raw, &struct{ Payload *map[string]any }{&p}))
			if len(p) != 2 || p["id"] == nil || p["disposition"] == nil {
				t.Fatalf("prompt.resolved carries more than id and disposition: %s", raw)
			}
		}
	}
	ans.Secret.Zero()
}

func TestInvalidAnswersAreRefused(t *testing.T) {
	f := startedFixture(t)
	r := testkit.NewScriptedResponder(
		testkit.Reply{Response: installflow.Response{Choice: "huge"}},
		testkit.Reply{Response: installflow.Response{}},
	)
	b := installflow.NewBroker(f.hub, r, false)
	if _, err := b.Ask(context.Background(), installflow.Prompt{Kind: installflow.PromptChoice, Question: "q", Choices: []string{"a"}}); !errors.Is(err, installflow.ErrInvalidAnswer) {
		t.Fatalf("off-list choice: %v", err)
	}
	if _, err := b.Ask(context.Background(), installflow.Prompt{Kind: installflow.PromptSecret, Question: "q"}); !errors.Is(err, installflow.ErrInvalidAnswer) {
		t.Fatalf("empty secret: %v", err)
	}
	finishAndWait(t, f)
}

func TestCancelWhileAPromptIsOpen(t *testing.T) {
	f := startedFixture(t)
	r := testkit.NewScriptedResponder(testkit.Reply{WaitForCancel: true})
	b := installflow.NewBroker(f.hub, r, false)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-r.Asked(); cancel() }()
	ans, err := b.Ask(ctx, installflow.Prompt{Kind: installflow.PromptSecret, Question: "q.passphrase"})
	if !errors.Is(err, context.Canceled) || ans.Disposition != installflow.PromptCancelled || !ans.Secret.IsEmpty() {
		t.Fatalf("%v %v", ans.Disposition, err)
	}
	if got := dispositions(finishAndWait(t, f)); len(got) != 1 || got[0] != installflow.PromptCancelled {
		t.Fatalf("%v", got)
	}
}

// TestOnePromptAtATime: concurrent askers are serialised, so a responder is
// never asked two questions at once.
func TestOnePromptAtATime(t *testing.T) {
	f := startedFixture(t)
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	resp := respondFunc(func(ctx context.Context, q installflow.Question) (installflow.Response, error) {
		mu.Lock()
		inFlight++
		maxInFlight = max(maxInFlight, inFlight)
		mu.Unlock()
		defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
		return installflow.Response{Confirmed: true}, nil
	})
	b := installflow.NewBroker(f.hub, resp, false)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Ask(context.Background(), installflow.Prompt{Kind: installflow.PromptConfirm, Question: "q"})
		}()
	}
	wg.Wait()
	envs := finishAndWait(t, f)
	if maxInFlight != 1 {
		t.Fatalf("%d prompts were open at once", maxInFlight)
	}
	open := 0
	for _, e := range envs {
		switch e.Kind {
		case installflow.KindPromptRequested:
			open++
			if open > 1 {
				t.Fatal("two prompts outstanding in the stream")
			}
		case installflow.KindPromptResolved:
			open--
		}
	}
}

type respondFunc func(context.Context, installflow.Question) (installflow.Response, error)

func (f respondFunc) Respond(ctx context.Context, q installflow.Question) (installflow.Response, error) {
	return f(ctx, q)
}
