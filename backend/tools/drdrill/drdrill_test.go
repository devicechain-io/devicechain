// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/secrets"
)

// The drill's real verification is the rig: a live cluster, a real dump, and a
// negative control. What is worth pinning here is the handful of decisions that
// would quietly turn a failed drill into a passing one.

func TestAnUnclassifiedErrorIsNotAVerdict(t *testing.T) {
	// Every exit code except 1 is a claim about the DATA. An error that carries no
	// explicit verdict must never be reported as one, or a connection failure in
	// the negative control would read as "the decrypt failed" and the control
	// would hold for the wrong reason.
	if got := codeOf(errors.New("connection refused")); got != exitSetup {
		t.Fatalf("an unclassified error mapped to exit %d, want %d (setup/inconclusive)", got, exitSetup)
	}
	if got := codeOf(nil); got != exitOK {
		t.Fatalf("no error mapped to exit %d, want %d", got, exitOK)
	}
}

func TestAVerdictSurvivesBeingWrapped(t *testing.T) {
	// The decrypt verdict is produced deep in runVerify and travels back through
	// main; if wrapping lost the code, the negative control would exit 1 and the
	// rig would call the run inconclusive forever.
	inner := failWith(exitDecryptFailed, "cannot open the envelope")
	wrapped := fmt.Errorf("verifying the drill secret: %w", inner)
	if got := codeOf(wrapped); got != exitDecryptFailed {
		t.Fatalf("a wrapped verdict mapped to exit %d, want %d", got, exitDecryptFailed)
	}
}

func TestTheExitCodesAreAllDistinct(t *testing.T) {
	// The rig asserts on exact codes. Two outcomes sharing one would make the
	// negative control accept an outcome it must reject.
	seen := map[int]string{}
	for name, code := range map[string]int{
		"ok": exitOK, "setup": exitSetup, "decryptFailed": exitDecryptFailed,
		"notFound": exitNotFound, "mismatch": exitMismatch,
	} {
		if other, dup := seen[code]; dup {
			t.Fatalf("exit code %d is used by both %q and %q", code, other, name)
		}
		seen[code] = name
	}
}

func TestAReceiptRoundTripsThroughAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	want := sampleReceipt()
	if err := WriteReceipt(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadReceipt(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != want {
		t.Fatalf("round trip changed the receipt:\n got %+v\nwant %+v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The receipt holds the drill's plaintext secret; world-readable is wrong even
	// for throwaway material, and a mode regression is invisible in a passing run.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("receipt written with mode %o, want 600", perm)
	}
}

func TestAReceiptWithNoExpectedSecretIsRefused(t *testing.T) {
	// This is the one field whose absence could produce a FALSE PASS rather than a
	// failure: an empty expected value compared against an empty decrypt matches.
	// So it is refused on write and on read, not merely at comparison time.
	r := sampleReceipt()
	r.Secret = ""
	if err := WriteReceipt(filepath.Join(t.TempDir(), "r.json"), r); err == nil {
		t.Fatal("wrote a receipt with no expected secret")
	}

	path := filepath.Join(t.TempDir(), "hand-edited.json")
	if err := os.WriteFile(path, []byte(`{"instance":"i","tenant":"t","schema":"s","secretName":"n","channelId":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadReceipt(path)
	if err == nil {
		t.Fatal("read a receipt with no expected secret")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Fatalf("the error does not name the missing field: %v", err)
	}
}

func TestAReceiptNamesEveryFieldItIsMissing(t *testing.T) {
	err := Receipt{Secret: "x"}.Validate()
	if err == nil {
		t.Fatal("an empty receipt validated")
	}
	for _, field := range []string{"channelId", "instance", "schema", "secretName", "tenant"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("missing field %q is not named in %q", field, err)
		}
	}
}

func TestAnAbsentRootKeyIsRefusedRatherThanTreatedAsAKey(t *testing.T) {
	// An empty or missing key must not reach the KEK provider. A zero-length key
	// fails the decrypt, which is precisely the negative control's expected
	// outcome — so an operator who simply forgot to export it would record a
	// control that "held" while testing nothing.
	t.Setenv(RootKeyEnv, "")
	if _, err := loadRootKey(""); err == nil {
		t.Fatal("an absent root key was accepted")
	}

	blank := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(blank, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRootKey(blank); err == nil {
		t.Fatal("a whitespace-only root key file was accepted")
	}
}

func TestARootKeyIsReadFromEitherSource(t *testing.T) {
	const encoded = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	t.Setenv(RootKeyEnv, encoded)
	fromEnv, err := loadRootKey("")
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if len(fromEnv) != 32 {
		t.Fatalf("decoded %d bytes from the environment, want 32", len(fromEnv))
	}

	path := filepath.Join(t.TempDir(), "key")
	// Trailing newline is what `kubectl ... | base64 -d > file` and every editor
	// leaves behind; it must not become part of the key.
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := loadRootKey(path)
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if string(fromFile) != string(fromEnv) {
		t.Fatal("the same key read from a file and the environment decoded differently")
	}
}

func TestAMalformedRootKeyIsRejected(t *testing.T) {
	// Without this the base64 decode could stop reporting its error and the tests
	// above would stay green, because they only ever feed it valid input.
	t.Setenv(RootKeyEnv, "not!valid!base64!")
	if _, err := loadRootKey(""); err == nil {
		t.Fatal("a root key that is not base64 was accepted")
	}
}

func TestARandomTokenIsFreshEachTime(t *testing.T) {
	// The drill secret is random per run so that a leftover row from an earlier
	// run cannot satisfy this one's comparison.
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := randomToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("randomToken repeated a value")
		}
		seen[tok] = true

		// Non-repeating is not the property that matters — a counter is also
		// non-repeating. The value has to carry real width, or a "random" secret
		// becomes guessable without any test noticing.
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("randomToken produced %q, which is not raw-url base64: %v", tok, err)
		}
		if len(raw) != 32 {
			t.Fatalf("randomToken produced %d bytes of entropy, want 32", len(raw))
		}
	}
}

func TestASchemaNameIsQuotedForTheSearchPath(t *testing.T) {
	// Functional areas carry hyphens, which is exactly why the search path is set
	// with a quoted identifier rather than passed through the DSN.
	if got := quoteIdentifier(areaNotification); got != `"notification-management"` {
		t.Fatalf("quoteIdentifier(%q) = %s", areaNotification, got)
	}
	if got := quoteIdentifier(`we"ird`); got != `"we""ird"` {
		t.Fatalf("an embedded quote was not doubled: %s", got)
	}
}

func sampleReceipt() Receipt {
	return Receipt{
		Instance:     "drdrill",
		Tenant:       "drdrill",
		Schema:       areaNotification,
		SecretName:   "channel/7/secret",
		ChannelToken: "drdrill-channel",
		ChannelID:    7,
		Identity:     "drdrill@drdrill.invalid",
		Password:     "pw",
		Secret:       "the-expected-plaintext",
	}
}

// ---------------------------------------------------------------------------
// classify — the verdict itself
// ---------------------------------------------------------------------------
//
// This is the only logic in the tool that decides what the drill REPORTS, and
// until it was extracted it had no test at all: inverting the plaintext
// comparison, or returning exitOK instead of exitDecryptFailed, left every test
// in this file green while the drill reported a pass exactly when it should have
// reported a failure.

// goodEnvelope is a well-formed row: the shape a real sealed secret has.
func goodEnvelope() storedEnvelope {
	return storedEnvelope{
		Alg: "AES-256-GCM", KEKVersion: expectedKEKVersion,
		CiphertextLen: 64, NonceLen: 12, WrappedDEKLen: 44,
	}
}

func TestClassifyPassesOnlyWhenThePlaintextMatches(t *testing.T) {
	ok := resolveOutcome{Plaintext: []byte("s3cret"), Expected: "s3cret", Envelope: goodEnvelope()}
	if err := classify(ok); err != nil {
		t.Fatalf("a correct decrypt was not a pass: %v", err)
	}

	// The inversion that would otherwise be silent.
	bad := resolveOutcome{Plaintext: []byte("something-else"), Expected: "s3cret", Envelope: goodEnvelope()}
	if got := codeOf(classify(bad)); got != exitMismatch {
		t.Fatalf("a decrypt that returned the WRONG plaintext exited %d, want %d", got, exitMismatch)
	}

	// Length-equal but different, so the comparison cannot be passing on length.
	sameLen := resolveOutcome{Plaintext: []byte("aaaaaa"), Expected: "s3cret", Envelope: goodEnvelope()}
	if got := codeOf(classify(sameLen)); got != exitMismatch {
		t.Fatalf("a same-length wrong plaintext exited %d, want %d", got, exitMismatch)
	}
}

func TestClassifyReportsADecryptFailureOnlyWhenNothingElseExplainsIt(t *testing.T) {
	// The one shape that earns exit 3: the resolve failed, the run was not
	// interrupted, the identical re-read still works, and the envelope is sound.
	wrongKey := resolveOutcome{
		Err:      errors.New("unwrap dek: cipher: message authentication failed"),
		Envelope: goodEnvelope(),
		ReRead:   ptr(goodEnvelope()),
	}
	if got := codeOf(classify(wrongKey)); got != exitDecryptFailed {
		t.Fatalf("a genuine wrong-key failure exited %d, want %d — the negative control "+
			"can no longer distinguish the escrow from anything else", got, exitDecryptFailed)
	}
}

func TestClassifyRefusesToBlameTheKeyForAnythingElse(t *testing.T) {
	// Each of these reaches store.Resolve's error return and would have been
	// reported as "the secret CANNOT be decrypted with this instance's root key",
	// i.e. as a negative control that held.
	resolveFailed := errors.New("some resolve error")

	// wantMsg is asserted as well as the code, because several of these share exit
	// 1 and the code alone cannot tell them apart — a mutation that deleted the
	// "database stopped answering" branch entirely survived a code-only assertion,
	// because the outcome fell through to another branch with the same code. The
	// message IS the deliverable for an inconclusive run: it is what an operator
	// reads to decide what to fix.
	cases := map[string]struct {
		outcome resolveOutcome
		want    int
		wantMsg string
	}{
		"the run was interrupted": {
			outcome: resolveOutcome{Err: resolveFailed, CtxErr: context.Canceled,
				Envelope: goodEnvelope(), ReRead: ptr(goodEnvelope())},
			want:    exitSetup,
			wantMsg: "interrupted",
		},
		"the database stopped answering": {
			outcome: resolveOutcome{Err: resolveFailed, ReReadErr: errors.New("connection refused"),
				Envelope: goodEnvelope()},
			want:    exitSetup,
			wantMsg: "re-reading the same row failed",
		},
		"the row vanished mid-run": {
			outcome: resolveOutcome{Err: resolveFailed, Envelope: goodEnvelope(), ReRead: nil},
			want:    exitSetup,
			wantMsg: "disappeared",
		},
		"the envelope records an unknown algorithm": {
			outcome: resolveOutcome{Err: resolveFailed, Envelope: goodEnvelope(),
				ReRead: mutate(goodEnvelope(), func(e *storedEnvelope) { e.Alg = "" })},
			want:    exitMismatch,
			wantMsg: "value algorithm",
		},
		"the envelope records an unknown KEK version": {
			outcome: resolveOutcome{Err: resolveFailed, Envelope: goodEnvelope(),
				ReRead: mutate(goodEnvelope(), func(e *storedEnvelope) { e.KEKVersion = 99 })},
			want:    exitMismatch,
			wantMsg: "KEK version",
		},
		"the ciphertext restored empty": {
			outcome: resolveOutcome{Err: resolveFailed, Envelope: goodEnvelope(),
				ReRead: mutate(goodEnvelope(), func(e *storedEnvelope) { e.CiphertextLen = 0 })},
			want:    exitMismatch,
			wantMsg: "ciphertext is empty",
		},
		"the wrapped DEK restored empty": {
			outcome: resolveOutcome{Err: resolveFailed, Envelope: goodEnvelope(),
				ReRead: mutate(goodEnvelope(), func(e *storedEnvelope) { e.WrappedDEKLen = 0 })},
			want:    exitMismatch,
			wantMsg: "wrapped DEK is empty",
		},
		"the store did not match the handle": {
			outcome: resolveOutcome{Err: secrets.ErrSecretNotFound, Envelope: goodEnvelope()},
			want:    exitNotFound,
			wantMsg: "scope or tenant mismatch",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			verdict := classify(tc.outcome)
			got := codeOf(verdict)
			if got == exitDecryptFailed {
				t.Fatalf("%s was reported as a failed decrypt — the negative control would "+
					"HOLD on it, claiming the escrow was proved when it was not", name)
			}
			if got != tc.want {
				t.Fatalf("%s exited %d, want %d", name, got, tc.want)
			}
			if verdict == nil || !strings.Contains(verdict.Error(), tc.wantMsg) {
				t.Fatalf("%s did not say %q, so the operator is told the wrong thing to fix:\n%v",
					name, tc.wantMsg, verdict)
			}
		})
	}
}

func ptr(e storedEnvelope) *storedEnvelope { return &e }

func mutate(e storedEnvelope, f func(*storedEnvelope)) *storedEnvelope {
	f(&e)
	return &e
}
