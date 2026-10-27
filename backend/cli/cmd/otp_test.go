// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// withOTPSeams fakes the terminal for one test and restores it after.
func withOTPSeams(t *testing.T, isTTY bool, typed string) *int {
	t.Helper()
	prompts := 0
	origTTY, origPrompt := stdinIsTerminal, promptOTP
	stdinIsTerminal = func() bool { return isTTY }
	promptOTP = func() (string, error) { prompts++; return typed, nil }
	t.Cleanup(func() { stdinIsTerminal, promptOTP = origTTY, origPrompt })
	return &prompts
}

var someChallenge = &userclient.MfaRequiredError{Challenge: "c"}

// A code works once: --otp answers the first sign-in, and a second one (the token expired) never
// replays it. With no terminal it stops and names the flag; with one it prompts.
func TestOTPFlagIsSingleUse(t *testing.T) {
	prompts := withOTPSeams(t, false, "")
	f := otpFunc("123456")
	got, err := f(context.Background(), someChallenge)
	if err != nil || got != "123456" {
		t.Fatalf("first call: %q %v", got, err)
	}
	_, err = f(context.Background(), someChallenge)
	if err == nil || !strings.Contains(err.Error(), "--otp") || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("second call must refuse and name --otp, got %v", err)
	}
	var mfa *userclient.MfaRequiredError
	if !errors.As(err, &mfa) {
		t.Errorf("the refusal must stay a typed MfaRequiredError: %v", err)
	}
	if *prompts != 0 {
		t.Errorf("prompted %d time(s) with no terminal", *prompts)
	}
}

func TestOTPWithNoFlagAndNoTerminalNamesTheFlag(t *testing.T) {
	prompts := withOTPSeams(t, false, "")
	_, err := otpFunc("")(context.Background(), someChallenge)
	if err == nil || !strings.Contains(err.Error(), "--otp") {
		t.Fatalf("got %v", err)
	}
	if *prompts != 0 {
		t.Errorf("prompted %d time(s) with no terminal; that is the hang", *prompts)
	}
}

func TestOTPPromptsOnATerminalEachTimeItIsNeeded(t *testing.T) {
	prompts := withOTPSeams(t, true, "654321")
	f := otpFunc("")
	for i := 0; i < 2; i++ {
		got, err := f(context.Background(), someChallenge)
		if err != nil || got != "654321" {
			t.Fatalf("call %d: %q %v", i, got, err)
		}
	}
	if *prompts != 2 {
		t.Errorf("prompted %d time(s), want 2 (a re-login asks again)", *prompts)
	}
	// --otp is used first and the prompt takes over for the re-login.
	*prompts = 0
	g := otpFunc("111111")
	if got, _ := g(context.Background(), someChallenge); got != "111111" || *prompts != 0 {
		t.Errorf("the flag must answer the first sign-in without prompting: %q (%d prompts)", got, *prompts)
	}
	if got, _ := g(context.Background(), someChallenge); got != "654321" || *prompts != 1 {
		t.Errorf("the second sign-in must prompt: %q (%d prompts)", got, *prompts)
	}
}

// mfaServer is a user-management that challenges every login and accepts one code.
type mfaServer struct {
	mu       sync.Mutex
	logins   int
	verified []string
}

func (s *mfaServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(body.Query, "verifyMfa("):
		s.verified = append(s.verified, fmt.Sprint(body.Variables["code"]))
		fmt.Fprintf(w, `{"data":{"verifyMfa":{"identityToken":"tok","expiresAt":%q,"superuser":true,"memberships":[]}}}`,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	case strings.Contains(body.Query, "login("):
		s.logins++
		fmt.Fprintf(w, `{"errors":[{"message":"mfa","extensions":{"code":"MFA_CHALLENGE","challenge":"chal","expiresAt":%q,"methods":["TOTP"]}}]}`,
			time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339))
	default:
		fmt.Fprint(w, `{"data":{}}`)
	}
}

func deadLettersCommand(server, otp string) *cobra.Command {
	c := &cobra.Command{}
	c.Flags().String("server", server, "")
	c.Flags().Bool("tls", false, "")
	c.Flags().String("otp", otp, "")
	return c
}

// T16 (dcctl). `deadletters list` against an account with a second factor and no --otp stops in ONE
// sign-in with an error that names --otp. With --otp the code reaches verifyMfa.
func TestDeadLettersListHandlesTheSecondFactor(t *testing.T) {
	withOTPSeams(t, false, "")
	s := &mfaServer{}
	srv := httptest.NewServer(s)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	dlEmail, dlPassword, dlPageSize, dlPages = "root@example.com", "pw", 10, 1
	dlTenant, dlKind, dlSource, dlSince = "", "", "", ""

	err := runDeadLettersList(deadLettersCommand(host, ""), nil)
	if err == nil || !strings.Contains(err.Error(), "--otp") {
		t.Fatalf("want an error naming --otp, got %v", err)
	}
	if s.logins != 1 || len(s.verified) != 0 {
		t.Errorf("logins=%d verifies=%v; one challenge, nothing verified", s.logins, s.verified)
	}

	s.logins = 0
	_ = runDeadLettersList(deadLettersCommand(host, "987654"), nil)
	if len(s.verified) != 1 || s.verified[0] != "987654" {
		t.Errorf("the --otp code did not reach verifyMfa: %v", s.verified)
	}
}

func TestEveryCommandThatTakesAPasswordTakesOTP(t *testing.T) {
	for name, c := range map[string]*cobra.Command{"deadletters": deadLettersCmd, "presence": presenceCmd, "sim": simCmd} {
		if c.PersistentFlags().Lookup("otp") == nil {
			t.Errorf("dcctl %s takes a password but not --otp", name)
		}
	}
}
