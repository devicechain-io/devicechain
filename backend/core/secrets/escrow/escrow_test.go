// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package escrow

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// testParams are deliberately cheap. Production cost is asserted separately by
// TestDefaultParamsAreExpensive and exercised end-to-end once by
// TestRoundTripAtProductionCost — running every case at 64 MiB would buy nothing
// and make the suite slow enough that someone would eventually skip it.
func testParams() KDFParams {
	return KDFParams{Alg: KDFArgon2id, Time: 1, Memory: 8, Threads: 1}
}

func testKey() []byte {
	k := make([]byte, RootKeySize)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// otherKey is a second, different root key — for cases that must distinguish "the
// right key came back" from "a key came back".
func otherKey() []byte {
	k := make([]byte, RootKeySize)
	for i := range k {
		k[i] = byte(0xa0 + i)
	}
	return k
}

var testTime = time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)

func mustWrap(t *testing.T, key []byte, pass, instance string) *Artifact {
	t.Helper()
	a, err := Wrap(key, pass, instance, testParams(), testTime)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return a
}

// --- the happy path ---------------------------------------------------------

func TestRoundTrip(t *testing.T) {
	key := testKey()
	a := mustWrap(t, key, "correct horse battery staple", "prod")

	got, err := Open(a, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("round-tripped key = %x, want %x", got, key)
	}
}

// The artifact has to survive being written to a file and read back, because that
// is the only way it is ever actually used.
func TestRoundTripThroughEncodedFile(t *testing.T) {
	key := testKey()
	a := mustWrap(t, key, "pass", "prod")

	raw, err := a.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got, err := Open(back, "pass")
	if err != nil {
		t.Fatalf("Open after decode: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("key did not survive the file round trip")
	}
}

func TestRoundTripAtProductionCost(t *testing.T) {
	key := testKey()
	a, err := Wrap(key, "pass", "prod", DefaultKDFParams(), testTime)
	if err != nil {
		t.Fatalf("Wrap at production cost: %v", err)
	}
	got, err := Open(a, "pass")
	if err != nil {
		t.Fatalf("Open at production cost: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("production-cost round trip returned the wrong key")
	}
}

// --- the negative controls --------------------------------------------------
//
// Each of these is a way the artifact could fail to protect anything. A round-trip
// test passes just as happily against an implementation that ignores the passphrase
// entirely.

func TestWrongPassphraseFails(t *testing.T) {
	a := mustWrap(t, testKey(), "right", "prod")
	if _, err := Open(a, "wrong"); err == nil {
		t.Fatal("Open accepted the wrong passphrase")
	}
}

func TestEmptyPassphraseIsRefusedOnWrap(t *testing.T) {
	if _, err := Wrap(testKey(), "", "prod", testParams(), testTime); err == nil {
		t.Fatal("Wrap accepted an empty passphrase, producing a file that is encrypted in form only")
	}
}

func TestEmptyInstanceIsRefused(t *testing.T) {
	if _, err := Wrap(testKey(), "pass", "", testParams(), testTime); err == nil {
		t.Fatal("Wrap accepted an empty instance name")
	}
}

func TestWrongKeySizeIsRefused(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := Wrap(make([]byte, n), "pass", "prod", testParams(), testTime); err == nil {
			t.Fatalf("Wrap accepted a %d-byte root key", n)
		}
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	a := mustWrap(t, testKey(), "pass", "prod")
	a.ciphertext[0] ^= 0xff
	if _, err := Open(a, "pass"); err == nil {
		t.Fatal("Open accepted a tampered ciphertext")
	}
}

func TestTamperedNonceFails(t *testing.T) {
	a := mustWrap(t, testKey(), "pass", "prod")
	a.nonce[0] ^= 0xff
	if _, err := Open(a, "pass"); err == nil {
		t.Fatal("Open accepted a tampered nonce")
	}
}

// Retargeting the instance name is the tamper an attacker would actually attempt:
// it is the one field that reads as pure metadata. It is authenticated.
//
// `instance` and `created` are also the only two fields that ISOLATE the AAD
// binding. Everything else in the header feeds either a validity guard or the key
// derivation, so altering it would fail for a second reason and the test could not
// tell you which barrier you had just deleted. These two touch nothing but the AAD.
func TestRetargetedInstanceFails(t *testing.T) {
	raw := tamperHeader(t, mustWrap(t, testKey(), "pass", "prod"), func(h map[string]any) {
		h["instance"] = "staging"
	})
	a, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if a.Instance != "staging" {
		t.Fatalf("the tamper did not take: instance is %q", a.Instance)
	}
	if _, err := Open(a, "pass"); err == nil {
		t.Fatal("Open accepted an artifact whose recorded instance had been changed")
	}
}

func TestAlteredCreatedTimeFails(t *testing.T) {
	raw := tamperHeader(t, mustWrap(t, testKey(), "pass", "prod"), func(h map[string]any) {
		h["created"] = "2030-01-01T00:00:00Z"
	})
	a, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, err := Open(a, "pass"); err == nil {
		t.Fatal("Open accepted an artifact whose recorded creation time had been changed")
	}
}

// The KDF parameters travel INSIDE the file, so an attacker holding it could try to
// rewrite them down to something cheap to brute-force. Both cases below must be
// refused — but note they are NOT independent barriers, and an earlier version of
// this test claimed they were.
//
// Any change to a cost parameter also changes the derived key, so the AEAD would
// reject it regardless of whether the floor exists. That makes "it returned an
// error" worthless as evidence for the floor specifically. The below-floor case
// therefore asserts the MESSAGE — the same technique the salt guard needed, and for
// the same reason: the guard's whole job is to say what is actually wrong instead of
// blaming a passphrase that is fine. (Found by mutation: deleting the open-side
// validateParams call left the original version of this test green.)
func TestDowngradedKDFCostFails(t *testing.T) {
	// (1) Above the floor, so the floor cannot be what refuses it.
	t.Run("an altered but legal cost is still refused", func(t *testing.T) {
		raw := tamperHeader(t, mustWrap(t, testKey(), "pass", "prod"), func(h map[string]any) {
			kdf := h["kdf"].(map[string]any)
			kdf["m"] = 8 // legal (== the floor), but not what was sealed
			kdf["t"] = 1
		})
		a, err := Decode(raw)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if err := validateParams(a.KDF); err != nil {
			t.Fatalf("these params must clear the floor, or this case tests the wrong barrier: %v", err)
		}
		if _, err := Open(a, "pass"); err == nil {
			t.Fatal("Open accepted an artifact whose KDF cost had been downgraded")
		}
	})

	// (2) Below the floor: refused before any derivation, and it must SAY so.
	t.Run("a below-floor cost is named, not reported as a bad passphrase", func(t *testing.T) {
		a := mustWrap(t, testKey(), "pass", "prod")
		a.KDF.Memory = 1

		_, err := Open(a, "pass")
		if err == nil {
			t.Fatal("Open accepted an artifact whose KDF cost was below the floor")
		}
		if !strings.Contains(err.Error(), "memory cost") {
			t.Fatalf("error %q does not name the cost; without the floor the AEAD would fail here anyway "+
				"and this case would prove nothing", err)
		}
		if strings.Contains(err.Error(), "passphrase") {
			t.Fatalf("error %q blames the passphrase for a malformed artifact", err)
		}
	})

	// (3) The ceiling. One corrupted byte here is the difference between a legible
	// error and a multi-terabyte allocation in the middle of a recovery.
	t.Run("an absurd cost is refused rather than attempted", func(t *testing.T) {
		a := mustWrap(t, testKey(), "pass", "prod")
		a.KDF.Memory = 4294967295

		_, err := Open(a, "pass")
		if err == nil {
			t.Fatal("Open attempted a 4 TiB argon2 derivation")
		}
		if !strings.Contains(err.Error(), "ceiling") {
			t.Fatalf("error %q does not name the ceiling", err)
		}
	})
}

func TestBelowFloorKDFParamsAreRefused(t *testing.T) {
	cases := map[string]KDFParams{
		"unknown alg":  {Alg: "pbkdf2", Time: 3, Memory: 65536, Threads: 4},
		"zero time":    {Alg: KDFArgon2id, Time: 0, Memory: 65536, Threads: 4},
		"tiny memory":  {Alg: KDFArgon2id, Time: 3, Memory: 4, Threads: 4},
		"zero threads": {Alg: KDFArgon2id, Time: 3, Memory: 65536, Threads: 0},
	}
	for name, p := range cases {
		if _, err := Wrap(testKey(), "pass", "prod", p, testTime); err == nil {
			t.Fatalf("Wrap accepted %s", name)
		}
	}
}

// A truncated salt fails EITHER WAY — without the explicit check, argon2 derives a
// different key and the AEAD rejects it. So "it returned an error" proves nothing
// here; the assertion has to be on the MESSAGE. That is the entire value of the
// guard: without it, a malformed file reports "wrong passphrase, or the artifact has
// been altered", and an operator mid-restore goes hunting for a passphrase that was
// never the problem.
//
// (Found by mutation: deleting the guard left the original version of this test
// green.)
func TestShortSaltIsRefusedWithAnHonestError(t *testing.T) {
	a := mustWrap(t, testKey(), "pass", "prod")
	a.KDF.Salt = a.KDF.Salt[:4]

	_, err := Open(a, "pass")
	if err == nil {
		t.Fatal("Open accepted an artifact with a truncated salt")
	}
	if !strings.Contains(err.Error(), "salt") {
		t.Fatalf("error %q does not name the salt; a malformed artifact is being reported as a bad passphrase", err)
	}
	if strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("error %q blames the passphrase for a malformed artifact", err)
	}
}

// The other half of the same gap: an artifact carrying NO salt at all. Wrapping
// treats an absent salt as "generate one", so only the open side can call this what
// it is.
func TestMissingSaltIsRefusedWithAnHonestError(t *testing.T) {
	a := mustWrap(t, testKey(), "pass", "prod")
	a.KDF.Salt = nil

	_, err := Open(a, "pass")
	if err == nil {
		t.Fatal("Open accepted an artifact with no salt")
	}
	if !strings.Contains(err.Error(), "salt") {
		t.Fatalf("error %q does not name the salt", err)
	}
}

// And on the wrap side, where a short salt is a caller bug rather than a malformed
// file. Only tests pass a salt — which is exactly the risk: a test that quietly
// weakened every artifact it then certified.
func TestShortCallerSuppliedSaltIsRefusedOnWrap(t *testing.T) {
	p := testParams()
	p.Salt = []byte{1, 2, 3, 4}
	if _, err := Wrap(testKey(), "pass", "prod", p, testTime); err == nil {
		t.Fatal("Wrap accepted a 4-byte caller-supplied salt")
	}
}

// --- what makes each artifact distinct --------------------------------------

// Without a fresh salt per artifact, two instances escrowed under the same
// passphrase would share a derived key, and one cracked passphrase would open every
// escrow in the estate.
func TestSaltIsFreshPerArtifact(t *testing.T) {
	a := mustWrap(t, testKey(), "pass", "prod")
	b := mustWrap(t, testKey(), "pass", "prod")
	if bytes.Equal(a.KDF.Salt, b.KDF.Salt) {
		t.Fatal("two artifacts share a salt; a single cracked passphrase would open both")
	}
	if bytes.Equal(a.ciphertext, b.ciphertext) {
		t.Fatal("two artifacts of the same key under the same passphrase are byte-identical")
	}
}

func TestCallerSuppliedSaltIsHonoured(t *testing.T) {
	p := testParams()
	p.Salt = bytes.Repeat([]byte{7}, saltSize)
	a, err := Wrap(testKey(), "pass", "prod", p, testTime)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !bytes.Equal(a.KDF.Salt, p.Salt) {
		t.Fatalf("salt = %x, want the caller's %x", a.KDF.Salt, p.Salt)
	}
}

// --- the fingerprint --------------------------------------------------------

func TestFingerprintIdentifiesTheKeyWithoutThePassphrase(t *testing.T) {
	key := testKey()
	a := mustWrap(t, key, "pass", "prod")

	if !a.Matches(key) {
		t.Fatal("Matches said the artifact does not escrow the key it escrows")
	}
	other := testKey()
	other[0] ^= 0xff
	if a.Matches(other) {
		t.Fatal("Matches accepted a different key — a stale escrow would read as current")
	}
}

func TestFingerprintIsDomainSeparated(t *testing.T) {
	// The fingerprint must not be a bare SHA-256 of the key: a bare digest could be
	// produced — or replayed — by any other component that happens to hash the same
	// bytes for an unrelated purpose.
	key := testKey()
	if Fingerprint(key) == hashHex(key) {
		t.Fatal("Fingerprint is a bare SHA-256 of the root key; it must be domain-separated")
	}
}

func TestMatchesRejectsWrongSizedInput(t *testing.T) {
	a := mustWrap(t, testKey(), "pass", "prod")
	if a.Matches(make([]byte, 16)) {
		t.Fatal("Matches accepted a 16-byte key")
	}
	if (*Artifact)(nil).Matches(testKey()) {
		t.Fatal("Matches on a nil artifact returned true")
	}
}

// --- the file format --------------------------------------------------------

// A future-format artifact must say so. If it instead failed at the AEAD, the error
// would read as a wrong passphrase and send the operator after the one thing that
// was not wrong.
func TestFutureVersionIsRejectedWithAClearError(t *testing.T) {
	raw := tamperHeader(t, mustWrap(t, testKey(), "pass", "prod"), func(h map[string]any) {
		h["v"] = Version + 1
	})
	_, err := Decode(raw)
	if err == nil {
		t.Fatal("Decode accepted a future format version")
	}
	if !strings.Contains(err.Error(), "format version") {
		t.Fatalf("error %q does not name the version as the problem", err)
	}
}

// The realistic future artifact: a new version that ALSO carries a field this build
// has never heard of. That is what a format bump means.
//
// The version check therefore has to run before the strict decode, not after it —
// otherwise the unknown field is reported first and a perfectly good file from a
// newer dcctl is diagnosed as corrupt. (Found by review: the original ordering did
// exactly that, and the test above never caught it because bumping `v` alone is not
// what a real future artifact looks like.)
func TestAFutureArtifactWithNewFieldsStillReportsAsAFutureVersion(t *testing.T) {
	raw := tamperHeader(t, mustWrap(t, testKey(), "pass", "prod"), func(h map[string]any) {
		h["v"] = Version + 1
		h["kmsKeyName"] = "projects/p/locations/l/keyRings/r/cryptoKeys/k"
	})
	_, err := Decode(raw)
	if err == nil {
		t.Fatal("Decode accepted a future format version")
	}
	if !strings.Contains(err.Error(), "format version") {
		t.Fatalf("a future artifact was reported as %q rather than as a version this build "+
			"does not understand; the operator is sent to look for file corruption that is not there", err)
	}
}

// THE regression test for this format's central defect.
//
// The AAD used to be re-derived by marshalling a struct parsed back out of the file.
// encoding/json escapes invalid UTF-8 as � on the way out and re-emits it as raw
// bytes on the way back, so a single bad byte in an instance name produced an
// artifact that opened perfectly in memory and was PERMANENTLY unopenable from disk
// — discovered, if ever, during a recovery.
//
// The fix is that the AAD is the header's literal bytes, so this test is really
// asking one question: does a round trip THROUGH A FILE still open? An in-memory
// Wrap→Open would have passed throughout the bug's existence, which is exactly why
// it lasted.
func TestArtifactsWithAwkwardInstanceNamesSurviveAFileRoundTrip(t *testing.T) {
	for _, name := range []string{
		"prod",
		"prod-\xff-invalid-utf8", // the one that used to brick the artifact
		"prod\xed\xa0\x80",       // a lone surrogate
		"ünïcödé-prod",
		"prod line-separator",
		"prod\twith\ttabs",
		"prod\"with\"quotes",
		"prod<&>html-escapes",
		"日本語インスタンス",
		strings.Repeat("p", 500),
	} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			a, err := Wrap(testKey(), "pass", name, testParams(), testTime)
			if err != nil {
				t.Fatalf("Wrap: %v", err)
			}
			raw, err := a.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			back, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			key, err := Open(back, "pass")
			if err != nil {
				t.Fatalf("the artifact does not open from its own file: %v", err)
			}
			if !bytes.Equal(key, testKey()) {
				t.Fatal("the file round trip returned the wrong key")
			}
		})
	}
}

// A FROZEN artifact, written by an earlier build and committed to the repo, must
// still open.
//
// Every other test in this file wraps and opens in the same process with the same
// build, so none of them can detect that a change made yesterday's files unopenable
// — which is the one property this format exists to have. Nothing in CI covered it
// until this test, and the defect it would have caught was real: the AAD used to be
// reconstructed by re-marshalling, and the same header marshals to different bytes
// under encoding/json and GOEXPERIMENT=jsonv2. Whether a file opened five years from
// now depended on which JSON implementation the future build happened to ship.
//
// If this test fails, the format changed. That is not automatically a bug — but it
// means every artifact any operator is holding has just stopped working, so it needs
// a version bump and a migration path, not a regenerated fixture. DO NOT "fix" this
// by re-recording the golden file.
func TestAnArtifactFromAnEarlierBuildStillOpens(t *testing.T) {
	raw, err := os.ReadFile("testdata/v1-golden.escrow")
	if err != nil {
		t.Fatalf("reading the frozen artifact: %v", err)
	}

	a, err := Decode(raw)
	if err != nil {
		t.Fatalf("a committed v1 artifact no longer decodes: %v", err)
	}
	if a.Instance != goldenInstance {
		t.Errorf("instance = %q, want %q", a.Instance, goldenInstance)
	}
	key, err := Open(a, goldenPassphrase)
	if err != nil {
		t.Fatalf("a committed v1 artifact no longer OPENS: %v\n"+
			"Every escrow file written by every earlier build has just become unreadable. "+
			"This needs a format version bump and a migration path, not a regenerated fixture.", err)
	}
	if !bytes.Equal(key, testKey()) {
		t.Fatal("the frozen artifact returned the wrong key")
	}
	if !a.Matches(testKey()) {
		t.Error("the frozen artifact's fingerprint no longer matches its own key")
	}
}

const (
	goldenInstance   = "golden-prod"
	goldenPassphrase = "golden-passphrase"
)

// Two escrow blocks concatenated into one file must be refused, not silently
// resolved to the first.
//
// Appending a rotated escrow onto the old one, to keep them together, is a natural
// thing for an operator to do — and it used to produce a file that opened and
// returned the STALE key with nothing to indicate it. Whoever hits that is
// mid-recovery and cannot tell the two keys apart by looking.
func TestTwoBlocksInOneFileAreRefused(t *testing.T) {
	old, err := mustWrap(t, testKey(), "pass", "prod").Encode()
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := mustWrap(t, otherKey(), "pass", "prod").Encode()
	if err != nil {
		t.Fatal(err)
	}
	combined := append(append([]byte{}, old...), append([]byte("\n# rotated 2027-01-01, key below\n"), rotated...)...)

	_, err = Decode(combined)
	if err == nil {
		t.Fatal("Decode silently resolved a two-block file to one of the two keys")
	}
	if !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("error %q does not explain that there are two blocks", err)
	}
}

// A header missing a field decodes cleanly as a zero value and then fails at the
// AEAD, which reports "wrong passphrase" — the one thing that is not wrong. Each
// absence must be named instead.
func TestAMissingHeaderFieldIsNamedRatherThanBlamedOnThePassphrase(t *testing.T) {
	for _, field := range []string{"instance", "created", "cipher", "fp", "nonce"} {
		t.Run(field, func(t *testing.T) {
			raw := tamperHeader(t, mustWrap(t, testKey(), "pass", "prod"), func(h map[string]any) {
				delete(h, field)
			})
			_, err := Decode(raw)
			if err == nil {
				t.Fatalf("Decode accepted a header with no %q", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Errorf("error %q does not name the missing field", err)
			}
			if strings.Contains(err.Error(), "passphrase") {
				t.Errorf("error %q blames the passphrase for a malformed file", err)
			}
		})
	}
}

// A passphrase must open the artifact regardless of which Unicode normalization the
// operator's keyboard, terminal, or filesystem produced.
//
// "café" is NFC (U+00E9) on a Linux terminal and NFD (e + U+0301) from a macOS IME.
// They are visually identical and byte-different. Without normalization the artifact
// simply reports "wrong passphrase" to someone whose passphrase is correct — years
// later, on different hardware, with no other copy of the key.
func TestPassphraseNormalizationSurvivesADifferentKeyboard(t *testing.T) {
	const nfc = "café-secret"  // é as one code point
	const nfd = "café-secret" // e + combining acute
	if nfc == nfd {
		t.Fatal("the two spellings are identical; this test proves nothing")
	}

	a := mustWrap(t, testKey(), nfc, "prod")
	key, err := Open(a, nfd)
	if err != nil {
		t.Fatalf("an artifact written with an NFC passphrase does not open with its NFD spelling: %v", err)
	}
	if !bytes.Equal(key, testKey()) {
		t.Fatal("wrong key")
	}

	// Still wrong is still wrong.
	if _, err := Open(a, "cafe-secret"); err == nil {
		t.Fatal("normalization made a genuinely different passphrase work")
	}
}

// A nonce of the wrong LENGTH must be an error, not a panic. crypto/cipher panics on
// a short nonce, so without the guard a corrupt file crashes the recovery tool
// instead of reporting what is wrong with it.
//
// TestTamperedNonceFails only flips a byte, which preserves the length — so it never
// reached this path. (Found by review: deleting the guard left the whole suite
// green.)
func TestAWrongLengthNonceIsAnErrorNotAPanic(t *testing.T) {
	raw := tamperHeader(t, mustWrap(t, testKey(), "pass", "prod"), func(h map[string]any) {
		h["nonce"] = base64.StdEncoding.EncodeToString([]byte{1, 2, 3})
	})
	a, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Open panicked on a short nonce instead of returning an error: %v", r)
		}
	}()
	if _, err := Open(a, "pass"); err == nil {
		t.Fatal("Open accepted a 3-byte nonce")
	} else if !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("error %q does not name the nonce", err)
	}
}

// A header with no version at all is not a version-0 artifact, it is not an escrow
// artifact. Saying so beats deriving a key against it.
func TestHeaderWithNoVersionIsRejected(t *testing.T) {
	raw := tamperHeader(t, mustWrap(t, testKey(), "pass", "prod"), func(h map[string]any) {
		delete(h, "v")
	})
	_, err := Decode(raw)
	if err == nil {
		t.Fatal("Decode accepted a header carrying no format version")
	}
	if !strings.Contains(err.Error(), "format version") {
		t.Fatalf("error %q does not name the missing version", err)
	}
}

func TestUnknownFieldIsRejectedWithAClearError(t *testing.T) {
	raw := reencodeWith(t, mustWrap(t, testKey(), "pass", "prod"), func(m map[string]any) {
		m["kmsKeyName"] = "projects/p/locations/l"
	})
	_, err := Decode(raw)
	if err == nil {
		t.Fatal("Decode silently dropped an unknown field; the AAD would then mismatch and read as a bad passphrase")
	}
	if strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("error %q blames the passphrase for a malformed document", err)
	}
}

func TestGarbageInputIsRejected(t *testing.T) {
	for name, in := range map[string]string{
		"empty":        "",
		"prose only":   "this is just a text file",
		"no end line":  beginLine + "\nAAAA\n",
		"bad base64":   beginLine + "\n!!!!not base64!!!!\n" + endLine + "\n",
		"not our json": beginLine + "\n" + base64.StdEncoding.EncodeToString([]byte(`{"hello":"world"}`)) + "\n" + endLine + "\n",
	} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Fatalf("Decode accepted %s", name)
		}
	}
}

// The block must survive the things that happen to text files in transit: extra
// surrounding prose, CRLF line endings, trailing whitespace.
func TestDecodeToleratesRealWorldFileMangling(t *testing.T) {
	key := testKey()
	raw, err := mustWrap(t, key, "pass", "prod").Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	mangled := "Forwarded-By: ops\r\n\r\n" + strings.ReplaceAll(string(raw), "\n", "  \r\n") + "\n-- \nsent from a phone\n"

	a, err := Decode([]byte(mangled))
	if err != nil {
		t.Fatalf("Decode of a mangled but intact file: %v", err)
	}
	got, err := Open(a, "pass")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("mangled round trip returned the wrong key")
	}
}

// The preamble is the only thing a person will read. Assert it actually states the
// consequence, because prose has no compiler and this is the part that decays.
func TestEncodedFileExplainsItselfToAHuman(t *testing.T) {
	raw, err := mustWrap(t, testKey(), "pass", "prod").Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	text := string(raw)
	// Each phrase is specific enough that only the claim it stands for can satisfy
	// it. An earlier version asserted the bare substring "NOT" for "backups do not
	// contain this" — review replaced the whole explanation with "It is NOT a
	// password." and the test passed. A generic token cannot hold a claim about
	// prose; the words that carry the meaning have to be the words asserted.
	for _, want := range []string{
		`instance "prod"`,           // which instance
		"database backup",           // that backups do not contain it...
		"etcd",                      // ...and where it actually lives instead
		"permanently unrecoverable", // what losing it costs
		"will appear to succeed",    // why the loss is not noticed at restore time
		RecoveryCommand,             // how to use it
		"--restore-root-key",        // ...specifically enough to type
		Fingerprint(testKey()),      // which key, checkable without the passphrase
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("encoded artifact never mentions %q", want)
		}
	}
}

// The passphrase must not appear anywhere in the artifact, and neither must the key.
func TestEncodedFileLeaksNeitherKeyNorPassphrase(t *testing.T) {
	key := testKey()
	const pass = "a-very-distinctive-passphrase"
	raw, err := mustWrap(t, key, pass, "prod").Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	body, err := extractBlock(string(raw))
	if err != nil {
		t.Fatalf("extractBlock: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}

	// Searching for the raw bytes is NOT enough, and an earlier version of this test
	// did only that. Every []byte field in the document is JSON-marshalled to base64,
	// so a leaked key never appears as a raw byte run anywhere — not in the file, not
	// in the decoded body. Review proved it: adding a field that writes the root key
	// in cleartext left the entire suite green.
	//
	// So each secret is searched for in every ENCODING the format could carry it in.
	// This is the only test standing between the escrow artifact and being a second
	// way to leak every secret in the instance.
	for _, secret := range []struct {
		name  string
		bytes []byte
	}{
		{"root key", key},
		{"passphrase", []byte(pass)},
	} {
		for _, form := range []struct {
			encoding string
			value    []byte
		}{
			{"raw", secret.bytes},
			{"base64", []byte(base64.StdEncoding.EncodeToString(secret.bytes))},
			{"base64url", []byte(base64.URLEncoding.EncodeToString(secret.bytes))},
			{"hex", []byte(hex.EncodeToString(secret.bytes))},
		} {
			if bytes.Contains(raw, form.value) {
				t.Errorf("the artifact FILE contains the %s in %s form", secret.name, form.encoding)
			}
			if bytes.Contains(decoded, form.value) {
				t.Errorf("the artifact's decoded body contains the %s in %s form", secret.name, form.encoding)
			}
		}
	}
}

// The leak test above is only as good as its ability to see a leak, and it could not
// see one for as long as it existed. This proves it can now: it plants the key in the
// document exactly as a careless new field would, and asserts the search finds it.
//
// Without this, a future refactor that narrows the search would silently return the
// suite to the state review found it in — green, and blind.
func TestTheLeakCheckCanActuallyDetectALeak(t *testing.T) {
	key := testKey()
	a := mustWrap(t, key, "pass", "prod")

	// A leaked key reaches the document base64-encoded, because that is what
	// encoding/json does with every []byte. This is the shape the old check missed.
	planted, err := json.Marshal(map[string]any{"hdr": json.RawMessage(a.headerBytes), "ct": key})
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range [][]byte{
		[]byte(base64.StdEncoding.EncodeToString(key)),
	} {
		if !bytes.Contains(planted, form) {
			t.Fatal("a document carrying the root key verbatim does not contain it in base64 form; " +
				"the leak test's search is looking for the wrong thing")
		}
	}
}

// --- cost -------------------------------------------------------------------

// The default cost is the only thing standing between a stolen artifact and an
// offline brute-force, and it is a one-line edit away from being weakened without
// anyone noticing. Pin it.
func TestDefaultParamsAreExpensive(t *testing.T) {
	p := DefaultKDFParams()
	if p.Alg != KDFArgon2id {
		t.Fatalf("default KDF is %q, want %q", p.Alg, KDFArgon2id)
	}
	if p.Memory < 64*1024 {
		t.Fatalf("default memory cost is %d KiB, want at least 65536 (64 MiB); this artifact is attacked offline", p.Memory)
	}
	if p.Time < 3 {
		t.Fatalf("default time cost is %d, want at least 3", p.Time)
	}
}

// --- helpers ----------------------------------------------------------------

// hashHex is a bare SHA-256, for the domain-separation counterweight above.
func hashHex(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])
}

// reencodeWith re-renders an artifact's JSON body after mutating it, so tests can
// forge documents the constructors would never produce.
func reencodeWith(t *testing.T, a *Artifact, mutate func(map[string]any)) []byte {
	t.Helper()
	raw, err := a.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	body, err := extractBlock(string(raw))
	if err != nil {
		t.Fatalf("extractBlock: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(decoded, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	mutate(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return []byte(beginLine + "\n" + base64.StdEncoding.EncodeToString(out) + "\n" + endLine + "\n")
}

// tamperHeader re-encodes an artifact with the HEADER object edited — which is how a
// tamper actually happens: someone with the file opens it and changes a value.
//
// Editing the in-memory Artifact struct proves nothing any more, and that is the
// point of the format: the AAD is the header's literal bytes as read from the file,
// so only a change to the FILE can move it. Tests that mutated struct fields were
// testing the old reconstruct-the-AAD design.
func tamperHeader(t *testing.T, a *Artifact, mutate func(map[string]any)) []byte {
	t.Helper()
	return reencodeWith(t, a, func(outer map[string]any) {
		raw, err := json.Marshal(outer["hdr"])
		if err != nil {
			t.Fatalf("re-marshal header: %v", err)
		}
		var h map[string]any
		if err := json.Unmarshal(raw, &h); err != nil {
			t.Fatalf("unmarshal header: %v", err)
		}
		mutate(h)
		outer["hdr"] = h
	})
}
