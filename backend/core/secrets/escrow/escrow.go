// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package escrow wraps the ADR-059 instance root key into a self-describing,
// passphrase-encrypted artifact an operator stores OFF-CLUSTER, and rehydrates it
// again during a restore.
//
// WHY THIS EXISTS (ADR-020/028 A5, decision D4)
//
// The instance root key is the KEK that wraps every per-secret DEK. It is minted
// once at bootstrap and lives in exactly one durable place: the instance's
// Kubernetes Secret, i.e. etcd. Nothing backs etcd up. CNPG archives Postgres WAL;
// a Timescale backup covers Timescale. Neither contains the key.
//
// The consequence is a failure mode that passes every drill anyone would naturally
// run: restore the databases IN PLACE and everything works, because etcd still
// holds the key. Restore to a FRESH cluster — the actual disaster case — and the
// ciphertext rehydrates perfectly and is permanently undecryptable. Every stored
// secret (connector credentials, SMTP passwords, AI provider keys) is lost with no
// error at restore time; the failure surfaces later, as an unexplained decrypt
// error, long after the backup that could have helped has rotated away.
//
// So the key needs a durable copy outside the cluster it protects, and that copy
// cannot be plaintext or the escrow is just a second way to leak every secret.
// Hence: encrypted under an operator passphrase, with the KDF and its parameters
// recorded IN the artifact so a file written today still opens years from now
// against a build whose defaults have moved on.
//
// # WHAT THIS PACKAGE IS NOT
//
// It is not the KEK provider. Services never call it; they form their KEK from the
// running instance config exactly as before. This is bootstrap-time and
// restore-time tooling only, which is why it is a subpackage — nothing links argon2
// into a service binary.
package escrow

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

const (
	// Version is the artifact format version. It is checked BEFORE any decryption is
	// attempted so that an artifact from a future build fails with "unsupported
	// version" rather than an authentication error, which would read as a wrong
	// passphrase and send an operator hunting for the wrong problem at the worst
	// possible moment.
	Version = 1

	// KDFArgon2id names the key-derivation function. Recorded per-artifact: an
	// artifact written today must stay openable after the default changes.
	KDFArgon2id = "argon2id"

	// CipherAES256GCM names the artifact encryption. Same reasoning as KDFArgon2id.
	CipherAES256GCM = "AES-256-GCM"

	// RootKeySize is the ADR-059 root key length: 32 bytes → AES-256.
	RootKeySize = 32

	saltSize = 16

	// maxKDFMemory and maxKDFTime bound the cost parameters an artifact may ask this
	// build to spend. See validateParams: the floor stops a downgrade attack, the
	// ceiling stops a damaged file from turning recovery into an unbounded allocation.
	maxKDFMemory = 4 * 1024 * 1024 // KiB, i.e. 4 GiB — 64x the default
	maxKDFTime   = 64
)

// fingerprintDomain domain-separates the root-key fingerprint so the digest can
// never be confused with — or replayed as — any other hash of the same key.
const fingerprintDomain = "devicechain-rootkey-fingerprint-v1"

// RecoveryCommand is the dcctl command the artifact's own preamble tells an
// operator to run. It is a constant, and exported, so that the CLI can assert this
// package is not printing recovery instructions for a command dcctl does not have
// — see TestTheArtifactNamesARealRecoveryCommand in the dcctl module.
//
// Worth pinning because the first version of that preamble named
// `dcctl secrets rehydrate`, which was never built. A file whose entire purpose is
// to be read years later by someone with no other options must not hand them a
// command that does not exist, and nothing about writing it here can tell.
const RecoveryCommand = "dcctl bootstrap"

// PEM-style delimiters. The artifact is a text file on purpose: its whole job is to
// be recognisable years later by someone who did not write it, in a password
// manager or a safe, next to things that are not this.
const (
	beginLine = "-----BEGIN DEVICECHAIN ROOT KEY ESCROW-----"
	endLine   = "-----END DEVICECHAIN ROOT KEY ESCROW-----"
)

// KDFParams are the argon2id cost parameters. They are stored in the artifact, not
// assumed, so raising the defaults never orphans an existing escrow file.
type KDFParams struct {
	Alg string `json:"alg"`
	// Time is argon2's iteration count, Memory its working-set size in KiB, and
	// Threads its parallelism.
	Time    uint32 `json:"t"`
	Memory  uint32 `json:"m"`
	Threads uint8  `json:"p"`
	Salt    []byte `json:"salt"`
}

// DefaultKDFParams returns the cost parameters used for a real escrow: argon2id at
// 64 MiB. This is deliberately expensive — the artifact is offline, so an attacker
// who obtains it can grind at their own pace, and the only defence is the unit cost
// of a guess. It costs an operator well under a second, once, at bootstrap.
func DefaultKDFParams() KDFParams {
	return KDFParams{Alg: KDFArgon2id, Time: 3, Memory: 64 * 1024, Threads: 4}
}

// header is the authenticated, cleartext part of an artifact. Every field here is
// bound as GCM additional authenticated data, so editing ANY of it — retargeting an
// artifact at a different instance, or downgrading the KDF cost to make a
// brute-force cheaper — breaks decryption instead of quietly taking effect.
type header struct {
	Version     int       `json:"v"`
	Instance    string    `json:"instance"`
	Created     time.Time `json:"created"`
	KDF         KDFParams `json:"kdf"`
	Cipher      string    `json:"cipher"`
	Fingerprint string    `json:"fp"`
	Nonce       []byte    `json:"nonce"`
}

// artifactJSON is the wire form: the header, kept as its LITERAL BYTES, plus the
// ciphertext.
//
// The header is a json.RawMessage rather than a struct on purpose, and it is the
// single most important decision in this file. The AAD must be the exact byte string
// that was sealed. An earlier version re-derived it by marshalling a struct parsed
// back out of the file, which is only sound if marshal→unmarshal→marshal is a fixed
// point for every field — and it is not:
//
//   - encoding/json escapes invalid UTF-8 as the six characters � on the way
//     out, and re-emits it as raw bytes on the way back, so one bad byte in an
//     instance name produced an artifact that verified in memory and was
//     PERMANENTLY unopenable from disk.
//   - the same header marshals to different bytes under encoding/json and under
//     GOEXPERIMENT=jsonv2. Whether a file written today opens in five years would
//     have depended on which JSON implementation the future build happened to ship,
//     which is precisely the promise this format exists to make.
//
// Capturing the bytes removes the entire class rather than either instance of it. It
// also means a duplicated or reordered key fails authentication instead of quietly
// last-winning, and it lets a future format version be opened by a build that can no
// longer construct that version's header struct.
type artifactJSON struct {
	Header     json.RawMessage `json:"hdr"`
	Ciphertext []byte          `json:"ct"`
}

// Artifact is one escrowed root key.
type Artifact struct {
	Instance    string
	Created     time.Time
	KDF         KDFParams
	Cipher      string
	Fingerprint string

	// headerBytes is the exact AAD: what Wrap sealed, or what Decode read. Never
	// reconstructed. See artifactJSON.
	headerBytes []byte
	nonce       []byte
	ciphertext  []byte
}

// Fingerprint is a domain-separated digest of a root key, safe to store and print
// in CLEARTEXT beside the ciphertext.
//
// It answers the question an operator cannot otherwise answer without the
// passphrase: "is the escrow file I am holding the one for the key this instance is
// actually running?" Without it, a stale escrow — written before a key rotation, or
// for a different instance — is indistinguishable from a good one until the day it
// is needed, which is the one day it must not be. Publishing it is safe: the key is
// 256 bits of entropy, so a digest confirms a guess nobody can make.
func Fingerprint(rootKey []byte) string {
	h := sha256.New()
	h.Write([]byte(fingerprintDomain))
	h.Write(rootKey)
	return hex.EncodeToString(h.Sum(nil))
}

// Wrap encrypts rootKey under passphrase and returns the artifact.
//
// instance is recorded for identification and authenticated; params carries the KDF
// cost (use DefaultKDFParams unless you are a test). A zero Salt is filled with
// fresh randomness — a caller-supplied salt exists only so tests can pin a vector.
func Wrap(rootKey []byte, passphrase string, instance string, params KDFParams, now time.Time) (*Artifact, error) {
	if len(rootKey) != RootKeySize {
		return nil, fmt.Errorf("escrow: root key is %d bytes, want %d", len(rootKey), RootKeySize)
	}
	if passphrase == "" {
		// An empty passphrase would produce a file that is encrypted in form and
		// public in substance. Refuse it here rather than in the CLI, so no other
		// caller can reintroduce it.
		return nil, fmt.Errorf("escrow: passphrase is empty; the artifact would be decryptable by anyone holding it")
	}
	if instance == "" {
		return nil, fmt.Errorf("escrow: instance name is empty; an unidentifiable escrow artifact cannot be matched to what it protects")
	}
	if err := validateParams(params); err != nil {
		return nil, err
	}

	switch {
	case len(params.Salt) == 0:
		params.Salt = make([]byte, saltSize)
		if _, err := io.ReadFull(rand.Reader, params.Salt); err != nil {
			return nil, fmt.Errorf("escrow: generate salt: %w", err)
		}
	case len(params.Salt) < saltSize:
		// Only tests supply a salt. A short one would silently weaken every artifact
		// they then claim to have verified.
		return nil, fmt.Errorf("escrow: caller-supplied salt is %d bytes, want at least %d", len(params.Salt), saltSize)
	}

	h := header{
		Version:     Version,
		Instance:    instance,
		Created:     now.UTC().Truncate(time.Second),
		KDF:         params,
		Cipher:      CipherAES256GCM,
		Fingerprint: Fingerprint(rootKey),
	}

	key := derive(passphrase, params)
	defer zero(key)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	h.Nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, h.Nonce); err != nil {
		return nil, fmt.Errorf("escrow: generate nonce: %w", err)
	}

	// These bytes are the AAD, AND they are what Encode writes. There is exactly one
	// marshal of the header in this package's write path, and every later read uses
	// the bytes rather than re-deriving them. See artifactJSON.
	aad, err := marshalHeader(h)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, h.Nonce, rootKey, aad)

	return &Artifact{
		Instance:    h.Instance,
		Created:     h.Created,
		KDF:         h.KDF,
		Cipher:      h.Cipher,
		Fingerprint: h.Fingerprint,
		headerBytes: aad,
		nonce:       h.Nonce,
		ciphertext:  ct,
	}, nil
}

// marshalHeader renders a header to its canonical compact bytes.
//
// Compacted explicitly even though json.Marshal already emits compact output, so
// that this and the decode side are visibly the same normalization. Compact removes
// insignificant whitespace and touches nothing inside a string literal, which is
// what lets a file survive being reflowed by a mail client while any change to an
// actual value still breaks authentication.
func marshalHeader(h header) ([]byte, error) {
	raw, err := json.Marshal(h)
	if err != nil {
		return nil, fmt.Errorf("escrow: encode header: %w", err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, fmt.Errorf("escrow: canonicalize header: %w", err)
	}
	return buf.Bytes(), nil
}

// Open decrypts an artifact and returns the root key.
//
// It fails closed on a wrong passphrase, a tampered header (including a retargeted
// instance name or a downgraded KDF cost), a tampered ciphertext, and an
// unsupported version — and, as a last check, on a plaintext whose fingerprint does
// not match the one recorded. That last one cannot trigger under AES-GCM, which is
// exactly why it is there: it is the assertion that fails if a future change swaps
// the cipher for something unauthenticated, when nothing else in this file would
// notice.
func Open(a *Artifact, passphrase string) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("escrow: nil artifact")
	}
	if passphrase == "" {
		return nil, fmt.Errorf("escrow: passphrase is empty")
	}
	if a.Cipher != CipherAES256GCM {
		return nil, fmt.Errorf("escrow: unsupported cipher %q", a.Cipher)
	}
	if err := validateParams(a.KDF); err != nil {
		return nil, err
	}
	// On the wrap side an absent salt means "generate one"; on this side it means the
	// artifact is malformed, and this is the ONLY place that says so.
	//
	// It matters for the message, not the outcome: a short salt derives a different
	// key, so the AEAD would reject it anyway — reporting "wrong passphrase, or the
	// artifact has been altered" to an operator mid-restore whose passphrase is
	// perfectly fine. Naming the real defect is the whole job here.
	if len(a.KDF.Salt) < saltSize {
		return nil, fmt.Errorf("escrow: artifact salt is %d bytes, want at least %d; the file is malformed", len(a.KDF.Salt), saltSize)
	}

	// The AAD is the bytes this artifact was built from — sealed by Wrap, or read
	// verbatim by Decode — never a reconstruction. See artifactJSON for what
	// reconstructing them cost.
	if len(a.headerBytes) == 0 {
		return nil, fmt.Errorf("escrow: artifact carries no header bytes; it was not produced by Wrap or Decode")
	}
	aad := a.headerBytes

	key := derive(passphrase, a.KDF)
	defer zero(key)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(a.nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("escrow: nonce is %d bytes, want %d", len(a.nonce), gcm.NonceSize())
	}
	rootKey, err := gcm.Open(nil, a.nonce, a.ciphertext, aad)
	if err != nil {
		// Deliberately does not distinguish "wrong passphrase" from "tampered
		// artifact": the AEAD cannot tell them apart, and guessing would be a lie in
		// whichever direction it guessed wrong.
		return nil, fmt.Errorf("escrow: cannot decrypt: wrong passphrase, or the artifact has been altered")
	}
	if len(rootKey) != RootKeySize {
		zero(rootKey)
		return nil, fmt.Errorf("escrow: decrypted key is %d bytes, want %d", len(rootKey), RootKeySize)
	}
	if subtle.ConstantTimeCompare([]byte(Fingerprint(rootKey)), []byte(a.Fingerprint)) != 1 {
		zero(rootKey)
		return nil, fmt.Errorf("escrow: decrypted key does not match the recorded fingerprint")
	}
	return rootKey, nil
}

// Matches reports whether this artifact escrows rootKey, WITHOUT the passphrase.
// This is what makes "is my escrow current?" a question dcctl can answer against a
// running instance rather than a question an operator answers by hoping.
func (a *Artifact) Matches(rootKey []byte) bool {
	if a == nil || len(rootKey) != RootKeySize {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(Fingerprint(rootKey)), []byte(a.Fingerprint)) == 1
}

// Encode renders the artifact as the text file an operator stores.
//
// The preamble is prose, not decoration. Whoever opens this file is, by definition,
// having a bad day — possibly years later, possibly having never seen one before —
// and the file has to explain what it is, what it protects, and what happens if it
// is lost, without reference to any documentation they may no longer be able to
// reach. Only the base64 block is authoritative; the preamble is a rendering, and
// Decode ignores it entirely.
func (a *Artifact) Encode() ([]byte, error) {
	if len(a.headerBytes) == 0 {
		return nil, fmt.Errorf("escrow: artifact carries no header bytes; it was not produced by Wrap or Decode")
	}
	body, err := json.Marshal(artifactJSON{
		Header:     json.RawMessage(a.headerBytes),
		Ciphertext: a.ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("escrow: encode artifact: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "DeviceChain instance root-key escrow (ADR-059)\n")
	fmt.Fprintf(&b, "==============================================\n\n")
	fmt.Fprintf(&b, "This file is the ONLY copy of the key that decrypts secrets stored by\n")
	fmt.Fprintf(&b, "DeviceChain instance %q outside that instance's own cluster. It is NOT\n", a.Instance)
	fmt.Fprintf(&b, "contained in any database backup: the key lives in the cluster's etcd, which\n")
	fmt.Fprintf(&b, "database backups do not cover.\n\n")
	fmt.Fprintf(&b, "If this file and its passphrase are lost, every secret the instance has stored\n")
	fmt.Fprintf(&b, "-- connector credentials, SMTP passwords, AI provider keys -- becomes\n")
	fmt.Fprintf(&b, "permanently unrecoverable after a restore to a new cluster. Restoring the\n")
	fmt.Fprintf(&b, "databases will appear to succeed and the secrets will not decrypt.\n\n")
	fmt.Fprintf(&b, "Store this file and its passphrase OFF-CLUSTER, and separately from each\n")
	fmt.Fprintf(&b, "other. To recover, seed a rebuilt instance from this file BEFORE restoring\n")
	fmt.Fprintf(&b, "its databases:\n\n")
	fmt.Fprintf(&b, "  %s <provider> %s --restore-root-key <this file>\n\n", RecoveryCommand, a.Instance)
	fmt.Fprintf(&b, "  instance:     %s\n", a.Instance)
	fmt.Fprintf(&b, "  created:      %s\n", a.Created.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "  kdf:          %s t=%d m=%dKiB p=%d\n", a.KDF.Alg, a.KDF.Time, a.KDF.Memory, a.KDF.Threads)
	fmt.Fprintf(&b, "  cipher:       %s\n", a.Cipher)
	fmt.Fprintf(&b, "  key id:       %s\n\n", a.Fingerprint)
	fmt.Fprintf(&b, "%s\n", beginLine)
	for _, line := range chunk(base64.StdEncoding.EncodeToString(body), 64) {
		fmt.Fprintf(&b, "%s\n", line)
	}
	fmt.Fprintf(&b, "%s\n", endLine)
	return []byte(b.String()), nil
}

// Decode parses an artifact from its text form, reading ONLY the delimited block.
func Decode(raw []byte) (*Artifact, error) {
	body, err := extractBlock(string(raw))
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("escrow: artifact body is not valid base64: %w", err)
	}

	var aj artifactJSON
	if err := strictUnmarshal(decoded, &aj); err != nil {
		return nil, fmt.Errorf("escrow: artifact body is not a valid escrow document: %w", err)
	}
	if len(aj.Header) == 0 {
		return nil, fmt.Errorf("escrow: artifact carries no header")
	}
	if len(aj.Ciphertext) == 0 {
		return nil, fmt.Errorf("escrow: artifact carries no ciphertext")
	}
	// Normalize whitespace only. These are the AAD, so nothing inside a string
	// literal may be touched — see marshalHeader.
	var hdrBuf bytes.Buffer
	if err := json.Compact(&hdrBuf, aj.Header); err != nil {
		return nil, fmt.Errorf("escrow: artifact header is not valid JSON: %w", err)
	}
	headerBytes := hdrBuf.Bytes()

	// The VERSION is read before anything stricter runs, because everything stricter
	// is version-specific. Reading it with a tolerant probe is the point: a v2
	// artifact necessarily carries fields this build has never heard of, so a strict
	// decode would reject it as malformed and never reach the version check at all —
	// which is what the previous ordering did, reporting a perfectly good future file
	// as a corrupt one.
	var probe struct {
		Version *int `json:"v"`
	}
	if err := json.Unmarshal(headerBytes, &probe); err != nil || probe.Version == nil {
		return nil, fmt.Errorf("escrow: artifact header has no format version; this does not look like a DeviceChain root-key escrow")
	}
	if *probe.Version != Version {
		return nil, fmt.Errorf("escrow: artifact is format version %d, this build understands version %d", *probe.Version, Version)
	}

	var h header
	if err := strictUnmarshal(headerBytes, &h); err != nil {
		return nil, fmt.Errorf("escrow: artifact header is not a valid version %d header: %w", Version, err)
	}
	// Named absences, because every one of these decodes cleanly as a zero value and
	// then fails at the AEAD as "wrong passphrase, or the artifact has been altered"
	// — sending an operator mid-recovery to re-check the one thing that was fine.
	for _, missing := range []struct {
		absent bool
		field  string
	}{
		{h.Instance == "", "instance"},
		{h.Created.IsZero(), "created"},
		{h.Cipher == "", "cipher"},
		{h.Fingerprint == "", "fp"},
		{len(h.Nonce) == 0, "nonce"},
		{h.KDF.Alg == "", "kdf.alg"},
	} {
		if missing.absent {
			return nil, fmt.Errorf("escrow: artifact header is missing %q; the file is malformed or truncated", missing.field)
		}
	}

	return &Artifact{
		Instance:    h.Instance,
		Created:     h.Created,
		KDF:         h.KDF,
		Cipher:      h.Cipher,
		Fingerprint: h.Fingerprint,
		headerBytes: headerBytes,
		nonce:       h.Nonce,
		ciphertext:  aj.Ciphertext,
	}, nil
}

// strictUnmarshal decodes exactly one JSON value, rejecting unknown fields and any
// trailing content.
//
// DisallowUnknownFields so a field this build does not know about is a clear parse
// error rather than a silent drop. Trailing content is refused too: without that, a
// document with a second concatenated object parses as the first and ignores the
// rest.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected trailing content after the JSON document")
	}
	return nil
}

// extractBlock pulls the base64 body out of the delimited block.
func extractBlock(s string) (string, error) {
	start := strings.Index(s, beginLine)
	if start < 0 {
		return "", fmt.Errorf("escrow: no %q line found; this does not look like a DeviceChain root-key escrow file", beginLine)
	}
	rest := s[start+len(beginLine):]
	end := strings.Index(rest, endLine)
	if end < 0 {
		return "", fmt.Errorf("escrow: no %q line found; the file is truncated", endLine)
	}
	// Two blocks in one file is refused rather than silently resolved to the first.
	//
	// Concatenating a rotated escrow onto the old one, to "keep them together", is a
	// natural thing to do and produces a file that opens — returning the STALE key,
	// with nothing to indicate it. Whoever hits that is mid-recovery and has no way
	// to tell the two keys apart by looking.
	if strings.Contains(rest[end+len(endLine):], beginLine) {
		return "", fmt.Errorf("escrow: the file contains more than one root-key escrow block; " +
			"split them into separate files so it is unambiguous which key you are recovering")
	}
	var b strings.Builder
	for _, line := range strings.Split(rest[:end], "\n") {
		b.WriteString(strings.TrimSpace(line))
	}
	return b.String(), nil
}

// validateParams rejects KDF COST settings that are unsupported or so weak that the
// artifact's encryption would be decorative. The floor is enforced on OPEN as well
// as on wrap, which is the point: the parameters travel inside the file, so without
// a floor an attacker could rewrite them down to t=1,m=8 and grind cheaply. (The
// AAD binding already makes that edit fail, so this is the second of two
// independent barriers; neither is load-bearing alone.)
//
// It deliberately says NOTHING about the salt. What a valid salt is depends on the
// direction: absent means "generate one" when wrapping and "malformed artifact" when
// opening, so each caller checks it for itself. An earlier version validated it here
// too, which made the open-side check unreachable — one condition guarded twice is
// one guard no test can hold, and mutation testing caught exactly that.
func validateParams(p KDFParams) error {
	if p.Alg != KDFArgon2id {
		return fmt.Errorf("escrow: unsupported KDF %q, want %q", p.Alg, KDFArgon2id)
	}
	if p.Time < 1 {
		return fmt.Errorf("escrow: KDF time cost is %d, want at least 1", p.Time)
	}
	if p.Memory < 8 {
		return fmt.Errorf("escrow: KDF memory cost is %d KiB, want at least 8", p.Memory)
	}
	if p.Threads < 1 {
		return fmt.Errorf("escrow: KDF parallelism is %d, want at least 1", p.Threads)
	}
	// A CEILING as well as a floor, because these parameters travel inside the file
	// and argon2 allocates Memory KiB up front. One corrupted byte in the recorded
	// cost turns `m` into billions and the recovery tool into an unbounded allocation
	// — an OOM kill or a hang, at the exact moment an operator needs a legible error
	// about a damaged file. The bound is far above any honest setting: the default is
	// 64 MiB and this permits 4 GiB.
	if p.Memory > maxKDFMemory {
		return fmt.Errorf("escrow: KDF memory cost is %d KiB, above the %d KiB ceiling; "+
			"the artifact is damaged, or was written by something that is not DeviceChain", p.Memory, maxKDFMemory)
	}
	if p.Time > maxKDFTime {
		return fmt.Errorf("escrow: KDF time cost is %d, above the %d ceiling; "+
			"the artifact is damaged, or was written by something that is not DeviceChain", p.Time, maxKDFTime)
	}
	return nil
}

// derive turns a passphrase into the KEK.
//
// The passphrase is NFC-normalized first. "café" typed on macOS (NFC, U+00E9) and on
// Linux (NFD, e + U+0301) are visually identical, byte-different, and would derive
// different keys — so an artifact written on one machine could not be opened on the
// other, reporting only "wrong passphrase" to someone whose passphrase is correct.
// For a file explicitly designed to be opened years later, possibly by a different
// person on different hardware, that is a permanent-loss path with no diagnostic.
func derive(passphrase string, p KDFParams) []byte {
	return argon2.IDKey([]byte(norm.NFC.String(passphrase)), p.Salt, p.Time, p.Memory, p.Threads, RootKeySize)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("escrow: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("escrow: new GCM: %w", err)
	}
	return gcm, nil
}

func chunk(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// zero overwrites b in place — best-effort, same caveat as core/secrets.zero: it
// clears the live buffer but cannot reach copies the runtime may have made.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
