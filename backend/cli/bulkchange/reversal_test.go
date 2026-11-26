// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
)

func refusal(code string) any {
	return map[string]any{"errors": []any{map[string]any{"message": "refused " + code, "extensions": map[string]any{"code": code}}}}
}

// stepUpFake is a StepUpClient whose answers are scripted per mutation call.
type stepUpFake struct {
	answers []error
	calls   int
	stepUps int
	stepErr error
	vars    []map[string]any
}

func (f *stepUpFake) Query(_ context.Context, _ string, _ string, vars map[string]any, out any) error {
	f.calls++
	f.vars = append(f.vars, vars)
	if f.calls <= len(f.answers) && f.answers[f.calls-1] != nil {
		return f.answers[f.calls-1]
	}
	*out.(*struct {
		Change *Change `json:"change"`
	}) = struct {
		Change *Change `json:"change"`
	}{&Change{Token: "r", Dispatch: "READY"}}
	return nil
}

func (f *stepUpFake) StepUp(context.Context) error {
	f.stepUps++
	return f.stepErr
}

func gqlRefusal(code string) error {
	return &userclient.GraphQLError{Messages: []string{"refused"}, Codes: []string{code}}
}

func TestStepUpRetriesOnlyAStaleSecondFactorOnce(t *testing.T) {
	resolution := Resolution{Token: "t", AllAmbiguous: true, Verdict: "confirmed", Note: "checked"}
	reversal := ReversalRequest{Original: "o", Token: "r", Name: "r", OverrideChanged: true}
	for _, tc := range []struct {
		name           string
		answers        []error
		stepErr        error
		ok             bool
		calls, stepUps int
		mention        string
	}{
		{"fresh", nil, nil, true, 1, 0, ""},
		{"stale then fresh", []error{gqlRefusal(codeStepUpRequired)}, nil, true, 2, 1, ""},
		{"stale twice", []error{gqlRefusal(codeStepUpRequired), gqlRefusal(codeStepUpRequired)}, nil, false, 2, 1, "recovery code does not count"},
		{"no second factor", []error{gqlRefusal(codeSecondFactorRequired)}, nil, false, 1, 0, "authenticator app"},
		{"sign-in again fails", []error{gqlRefusal(codeStepUpRequired)}, errors.New("no terminal"), false, 1, 1, "no terminal"},
		{"other refusal", []error{gqlRefusal("CONFLICT")}, nil, false, 1, 0, "refused"},
		{"mixed codes", []error{&userclient.GraphQLError{Messages: []string{"a", "b"}, Codes: []string{codeStepUpRequired, ""}}}, nil, false, 1, 0, ""},
	} {
		for _, action := range []string{"resolve", "reverse"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				fake := &stepUpFake{answers: tc.answers, stepErr: tc.stepErr}
				a := API{Client: fake, Endpoint: "test"}
				var err error
				if action == "resolve" {
					_, err = a.Resolve(t.Context(), resolution)
				} else {
					_, err = a.Reverse(t.Context(), reversal)
				}
				if (err == nil) != tc.ok || fake.calls != tc.calls || fake.stepUps != tc.stepUps {
					t.Fatalf("err=%v calls=%d stepUps=%d", err, fake.calls, fake.stepUps)
				}
				if err != nil && !strings.Contains(err.Error(), tc.mention) {
					t.Fatalf("error does not say what to do: %v", err)
				}
				for _, vars := range fake.vars[1:] {
					if vars["request"] == nil || len(fake.vars[0]) != len(vars) {
						t.Fatal("the retry changed the request")
					}
				}
			})
		}
	}
}

func TestStepUpThatCannotSignInAgainKeepsTheRefusal(t *testing.T) {
	calls := 0
	a := testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any {
		calls++
		return refusal(codeStepUpRequired)
	})
	_, err := a.Resolve(t.Context(), Resolution{Token: "t", Ordinals: []int{1}, Verdict: "FAILED", Note: "n"})
	if err == nil || calls != 1 || !userclient.AllHaveCode(err, codeStepUpRequired) {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// The real session client signs in again through a NEW session, so the retry carries a token
// whose second factor was just verified, never a refreshed one.
func TestSessionStepUpSignsInAgainWithANewSession(t *testing.T) {
	mutations := 0
	a := testAPI(t, bulkChangeTestSDL(t), func(query string, _ map[string]any) any {
		mutations++
		if mutations == 1 {
			return refusal(codeStepUpRequired)
		}
		return data("change", Change{Token: "t", Dispatch: "DISPATCHED", Evidence: "OPEN"})
	})
	session := a.Client.(*sessionClient).TenantSession
	fresh := 0
	a.Client = NewStepUpClient(session, func() *userclient.TenantSession {
		fresh++
		return userclient.NewTenantSession(nil, a.Endpoint, "email", "password", "tenant").WithSchemaNegotiation(nil)
	})
	change, err := a.Resolve(t.Context(), Resolution{Token: "t", AllAmbiguous: true, Verdict: "CONFIRMED", Note: "n"})
	if err != nil || change.Token != "t" || mutations != 2 || fresh != 1 {
		t.Fatalf("err=%v mutations=%d fresh=%d", err, mutations, fresh)
	}
	if a.Client.(*sessionClient).TenantSession == session {
		t.Fatal("the retry used the stale session")
	}
	plain := &sessionClient{TenantSession: session}
	if err := plain.StepUp(t.Context()); err == nil {
		t.Fatal("a client that cannot sign in again claimed a step-up")
	}
}

func TestReversalPreviewWritesNothingAndNeedsNoStepUp(t *testing.T) {
	var documents []string
	a := testAPI(t, bulkChangeTestSDL(t), func(query string, vars map[string]any) any {
		documents = append(documents, query)
		request := vars["request"].(map[string]any)
		if request["original"] != "o" || request["token"] != "r" || request["overrideChanged"] != true {
			t.Errorf("preview lost the request: %v", request)
		}
		return data("preview", ReversalPreview{ReversibleCount: 3, ChangedSinceCount: 1, ContractDigest: "server-digest"})
	})
	preview, err := a.PreviewReversal(t.Context(), ReversalRequest{Original: "o", Token: "r", Name: "r", OverrideChanged: true})
	if err != nil || preview.ContractDigest != "server-digest" || preview.Original != "o" || preview.Token != "r" || preview.ChangedSinceCount != 1 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if len(documents) != 1 || documents[0] != reversePreviewQuery {
		t.Fatalf("preview sent %v", documents)
	}
	a = testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any { return data("preview", map[string]any{}) })
	if _, err := a.PreviewReversal(t.Context(), ReversalRequest{Original: "o", Token: "r", Name: "r"}); err == nil {
		t.Fatal("a preview without a contract digest was accepted")
	}
}

func TestReverseAndResolveRefuseBadInputBeforeSending(t *testing.T) {
	calls := 0
	a := testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any {
		calls++
		return data("change", Change{Token: "t"})
	})
	long := strings.Repeat("x", 129)
	for _, r := range []ReversalRequest{
		{Token: "r", Name: "r"},
		{Original: "o", Name: "r"},
		{Original: "o", Token: long, Name: "r"},
		{Original: "o", Token: "r", Name: long},
		{Original: "o", Token: "r", Name: "r", Ordinals: []int{}},
		{Original: "o", Token: "r", Name: "r", Ordinals: []int{0}},
		{Original: "o", Token: "r", Name: "r", OverrideOrdinals: []int{-1}},
		{Original: "o", Token: "r", Name: "r", OverrideChanged: true, OverrideOrdinals: []int{1}},
		{Original: "o", Token: "r", Name: "r", Ordinals: []int{1, 2}, OverrideOrdinals: []int{3}},
		{Original: "o", Token: "r", Name: "r", Ordinals: make([]int, 10001)},
	} {
		if _, err := a.Reverse(t.Context(), r); err == nil {
			t.Errorf("accepted reversal %+v", r)
		}
		if _, err := a.PreviewReversal(t.Context(), r); err == nil {
			t.Errorf("previewed reversal %+v", r)
		}
	}
	for _, r := range []Resolution{
		{Ordinals: []int{1}, Verdict: "FAILED", Note: "n"},
		{Token: "t", Verdict: "FAILED", Note: "n"},
		{Token: "t", Ordinals: []int{1}, AllAmbiguous: true, Verdict: "FAILED", Note: "n"},
		{Token: "t", Ordinals: []int{0}, Verdict: "FAILED", Note: "n"},
		{Token: "t", AllAmbiguous: true, Verdict: "UNKNOWN", Note: "n"},
		{Token: "t", AllAmbiguous: true, Verdict: "FAILED", Note: " \n"},
		{Token: "t", AllAmbiguous: true, Verdict: "FAILED", Note: strings.Repeat("é", MaxNoteRunes+1)},
		{Token: "t", AllAmbiguous: true, Verdict: "FAILED", Note: "\xff"},
	} {
		if _, err := a.Resolve(t.Context(), r); err == nil {
			t.Errorf("accepted resolution %+v", r)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input reached the server %d times", calls)
	}
	ok := Resolution{Token: "t", AllAmbiguous: true, Verdict: " failed ", Note: strings.Repeat("é", MaxNoteRunes)}
	if err := ok.Validate(); err != nil || ok.Verdict != "FAILED" {
		t.Fatalf("a valid resolution was refused: %v %q", err, ok.Verdict)
	}
}

func TestUnknownFieldNeverRetriesReverseOrResolve(t *testing.T) {
	for _, action := range []string{"reverse", "resolve", "reverse preview"} {
		t.Run(action, func(t *testing.T) {
			sent, probes := 0, 0
			a := testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any {
				sent++
				return map[string]any{"errors": []any{map[string]any{"message": "unknown field"}}}
			}, func() string {
				probes++
				return bulkChangeTestSDL(t)
			})
			var err error
			switch action {
			case "reverse":
				_, err = a.Reverse(t.Context(), ReversalRequest{Original: "o", Token: "r", Name: "r"})
			case "reverse preview":
				_, err = a.PreviewReversal(t.Context(), ReversalRequest{Original: "o", Token: "r", Name: "r"})
			default:
				_, err = a.Resolve(t.Context(), Resolution{Token: "t", AllAmbiguous: true, Verdict: "FAILED", Note: "n"})
			}
			if err == nil || err.Error() != "still upgrading; retry" || sent != 1 || probes != 1 {
				t.Fatalf("retried or probe skipped: %v sent=%d probes=%d", err, sent, probes)
			}
		})
	}
}
