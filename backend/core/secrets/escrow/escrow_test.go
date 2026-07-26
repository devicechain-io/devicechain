// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package escrow

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
func TestRetargetedInstanceFails(t *testing.T) {
	a := mustWrap(t, testKey(), "pass", "prod")
	a.Instance = "staging"
	if _, err := Open(a, "pass"); err == nil {
		t.Fatal("Open accepted an artifact whose recorded instance had been changed")
	}
}

func TestAlteredCreatedTimeFails(t *testing.T) {
	a := mustWrap(t, testKey(), "pass", "prod")
	a.Created = a.Created.Add(time.Hour)
	if _, err := Open(a, "pass"); err == nil {
		t.Fatal("Open accepted an artifact whose recorded creation time had been changed")
	}
}

// The KDF parameters travel INSIDE the file, so an attacker holding it could try to
// rewrite them down to something cheap to brute-force. Two INDEPENDENT barriers stop
// that, and each is asserted on its own — a single case would pass on either one
// alone and could not tell you which of the two you had just deleted.
func TestDowngradedKDFCostFails(t *testing.T) {
	// (1) Above the floor, so only the AAD binding can refuse it.
	t.Run("caught by the AAD binding", func(t *testing.T) {
		a, err := Wrap(testKey(), "pass", "prod", DefaultKDFParams(), testTime)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		a.KDF.Memory = 8 // legal (== the floor), but not what was sealed
		a.KDF.Time = 1
		if err := validateParams(a.KDF); err != nil {
			t.Fatalf("these params must clear the floor, or this case tests the wrong barrier: %v", err)
		}
		if _, err := Open(a, "pass"); err == nil {
			t.Fatal("Open accepted an artifact whose KDF cost had been downgraded")
		}
	})

	// (2) Below the floor, refused before any derivation is attempted.
	t.Run("caught by the cost floor", func(t *testing.T) {
		a, err := Wrap(testKey(), "pass", "prod", DefaultKDFParams(), testTime)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		a.KDF.Memory = 1
		if _, err := Open(a, "pass"); err == nil {
			t.Fatal("Open accepted an artifact whose KDF cost was below the floor")
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
	raw := reencodeWith(t, mustWrap(t, testKey(), "pass", "prod"), func(m map[string]any) {
		m["v"] = Version + 1
	})
	_, err := Decode(raw)
	if err == nil {
		t.Fatal("Decode accepted a future format version")
	}
	if !strings.Contains(err.Error(), "format version") {
		t.Fatalf("error %q does not name the version as the problem", err)
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
	for _, want := range []string{
		"prod",                      // which instance
		"NOT",                       // that backups do not contain it
		"permanently unrecoverable", // what losing it costs
		"dcctl secrets rehydrate",   // how to use it
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
	if bytes.Contains(raw, key) {
		t.Fatal("the artifact contains the raw root key")
	}
	if bytes.Contains(raw, []byte(pass)) {
		t.Fatal("the artifact contains the passphrase")
	}
	// Also check the decoded body, so a base64 layer cannot hide it.
	body, err := extractBlock(string(raw))
	if err != nil {
		t.Fatalf("extractBlock: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	if bytes.Contains(decoded, key) {
		t.Fatal("the decoded artifact body contains the raw root key")
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
