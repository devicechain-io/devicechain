// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package verify holds every decision the device makes about an image BEFORE it will run it:
// whether the bytes are the bytes that were declared, whether a key the device already trusts
// signed them, and whether this device's own policy allows this image at all.
//
// The platform's signature binds only the image digest. The version, the component and the
// product are NOT signed, so a correctly signed older image is a perfectly valid replay. Refusing
// that is the device's job, and CheckPolicy and Describe are where this device does it.
package verify

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"devicechain.io/examples/ota-device/internal/wire"
)

// Failure is a refusal that carries the result code the device reports.
type Failure struct {
	Code   string
	Detail string
}

func (f *Failure) Error() string { return f.Code + ": " + f.Detail }

func fail(code, format string, args ...any) *Failure {
	return &Failure{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Codes this device reports beyond the two the contract names.
const (
	CodeVerificationFailed = "VERIFICATION_FAILED"
	CodeWrongComponent     = "WRONG_COMPONENT"
	CodeDowngradeRefused   = "DOWNGRADE_REFUSED"
	CodeImageMismatch      = "IMAGE_MISMATCH"
)

// FileDigest returns the "sha256:<hex>" digest of the file and its length.
func FileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// CheckDigest compares the digest of the held bytes with the one the assignment declared.
func CheckDigest(path, want string) error {
	got, _, err := FileDigest(path)
	if err != nil {
		return err
	}
	if got != want {
		return fail(CodeVerificationFailed, "digest of the downloaded bytes is %s, the assignment declared %s", got, want)
	}
	return nil
}

// TrustSet is the signers this device trusts, held locally. The wire's keyId only SELECTS a key
// from this set; a key is never taken from a message.
type TrustSet struct {
	keys map[string]any // keyId -> *ecdsa.PublicKey | ed25519.PublicKey
}

// ParseTrust reads PEM "PUBLIC KEY" blocks (one or several, concatenated). Anything else in the
// input, or a key type other than ECDSA P-256 or Ed25519, is an error: a trust set that silently
// dropped a key would be an empty one that looks configured.
func ParseTrust(data []byte) (*TrustSet, error) {
	ts := &TrustSet{keys: map[string]any{}}
	rest := data
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("trust set: unexpected PEM block %q (want PUBLIC KEY)", blk.Type)
		}
		pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("trust set: %w", err)
		}
		// The id is over the key's canonical DER, as the platform computes it, not over the bytes
		// this PEM happened to carry: an encoding the key can be written in two ways must not
		// give one key two ids.
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			return nil, fmt.Errorf("trust set: %w", err)
		}
		sum := sha256.Sum256(der)
		id := hex.EncodeToString(sum[:])
		switch k := pub.(type) {
		case *ecdsa.PublicKey:
			if k.Curve != elliptic.P256() {
				return nil, fmt.Errorf("trust set: key %s is not on P-256", id)
			}
		case ed25519.PublicKey:
		default:
			return nil, fmt.Errorf("trust set: key %s is neither ECDSA P-256 nor Ed25519", id)
		}
		ts.keys[id] = pub
	}
	if len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("trust set: trailing data after the last PEM block")
	}
	if len(ts.keys) == 0 {
		return nil, errors.New("trust set: no keys")
	}
	return ts, nil
}

// LoadTrust reads a trust set from a file.
func LoadTrust(path string) (*TrustSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseTrust(b)
}

// Len is the number of keys held.
func (t *TrustSet) Len() int {
	if t == nil {
		return 0
	}
	return len(t.keys)
}

// KeyIDs lists the held keys' ids.
func (t *TrustSet) KeyIDs() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.keys))
	for id := range t.keys {
		out = append(out, id)
	}
	return out
}

// VerifySignature checks a detached signature over the RAW 32-byte SHA-256 of the image, which is
// what both algorithms sign. digest is the "sha256:<hex>" of the bytes the device holds. A keyId
// the set does not hold is UNKNOWN_SIGNER; every other refusal is SIGNATURE_INVALID.
func (t *TrustSet) VerifySignature(digest, keyID, algorithm, sigB64 string) error {
	if t == nil {
		return fail(wire.CodeUnknownSigner, "this device holds no trusted keys")
	}
	key, ok := t.keys[keyID]
	if !ok {
		return fail(wire.CodeUnknownSigner, "key %s is not in this device's trust set", keyID)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(raw) != sha256.Size {
		return fail(wire.CodeSignatureInvalid, "digest is not a SHA-256")
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(sigB64)
	if err != nil {
		return fail(wire.CodeSignatureInvalid, "signature is not base64")
	}
	switch algorithm {
	case wire.AlgECDSA:
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return fail(wire.CodeSignatureInvalid, "algorithm %s does not match the type of key %s", algorithm, keyID)
		}
		if !ecdsa.VerifyASN1(pub, raw, sig) {
			return fail(wire.CodeSignatureInvalid, "ECDSA signature does not verify")
		}
	case wire.AlgEd25519:
		pub, ok := key.(ed25519.PublicKey)
		if !ok {
			return fail(wire.CodeSignatureInvalid, "algorithm %s does not match the type of key %s", algorithm, keyID)
		}
		if !ed25519.Verify(pub, raw, sig) {
			return fail(wire.CodeSignatureInvalid, "Ed25519 signature does not verify")
		}
	default:
		return fail(wire.CodeSignatureInvalid, "unknown algorithm %q", algorithm)
	}
	return nil
}

// Policy is what this device requires of an assignment, independent of any signature.
type Policy struct {
	Component      string
	Current        string // the version compiled into the running binary
	RequireSigned  bool
	AllowDowngrade bool
}

// CheckPolicy refuses an assignment for another component, an UNVERIFIED one when signatures are
// required, and any version that is not strictly newer than the running one (an equal version is
// a replay too). A version that is not semantic versioning fails closed.
func (p Policy) CheckPolicy(a wire.Assignment) error {
	if a.Component != p.Component {
		return fail(CodeWrongComponent, "this device updates %q, the assignment is for %q", p.Component, a.Component)
	}
	if p.RequireSigned && a.Trust != wire.TrustSigned {
		return fail(wire.CodeUnknownSigner, "this device requires a signed image and the assignment is %s", a.Trust)
	}
	if p.AllowDowngrade {
		return nil
	}
	cmp, err := CompareVersions(a.Artifact.Version, p.Current)
	if err != nil {
		return fail(CodeDowngradeRefused, "cannot order %q against the running %q: %v", a.Artifact.Version, p.Current, err)
	}
	if cmp <= 0 {
		return fail(CodeDowngradeRefused, "version %s is not newer than the running %s", a.Artifact.Version, p.Current)
	}
	return nil
}

// Semver is a parsed semantic version (https://semver.org); build metadata is dropped because it
// does not take part in precedence.
type Semver struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

// ParseSemver parses a strict semantic version. A leading "v", missing components and leading
// zeros are all refused.
func ParseSemver(s string) (Semver, error) {
	var v Semver
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if err := checkIdents(s[i+1:], false); err != nil {
			return v, err
		}
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if err := checkIdents(s[i+1:], true); err != nil {
			return v, err
		}
		v.Pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("%q is not MAJOR.MINOR.PATCH", s)
	}
	nums := [3]*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := parseNumeric(p)
		if err != nil {
			return v, err
		}
		*nums[i] = n
	}
	return v, nil
}

func parseNumeric(p string) (uint64, error) {
	if p == "" || (len(p) > 1 && p[0] == '0') {
		return 0, fmt.Errorf("%q is not a canonical number", p)
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%q is not a number", p)
		}
	}
	return strconv.ParseUint(p, 10, 63)
}

func checkIdents(s string, numericNoLeadingZero bool) error {
	if s == "" {
		return errors.New("empty identifier")
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return errors.New("empty identifier")
		}
		numeric := true
		for _, c := range id {
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
				numeric = false
			default:
				return fmt.Errorf("invalid character %q in %q", c, id)
			}
		}
		if numericNoLeadingZero && numeric && len(id) > 1 && id[0] == '0' {
			return fmt.Errorf("numeric identifier %q has a leading zero", id)
		}
	}
	return nil
}

// CompareVersions returns -1, 0 or 1 by semantic version precedence.
func CompareVersions(a, b string) (int, error) {
	va, err := ParseSemver(a)
	if err != nil {
		return 0, err
	}
	vb, err := ParseSemver(b)
	if err != nil {
		return 0, err
	}
	for _, p := range [][2]uint64{{va.Major, vb.Major}, {va.Minor, vb.Minor}, {va.Patch, vb.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case len(va.Pre) == 0 && len(vb.Pre) == 0:
		return 0, nil
	case len(va.Pre) == 0:
		return 1, nil
	case len(vb.Pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(va.Pre) && i < len(vb.Pre); i++ {
		if c := comparePre(va.Pre[i], vb.Pre[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case len(va.Pre) < len(vb.Pre):
		return -1, nil
	case len(va.Pre) > len(vb.Pre):
		return 1, nil
	}
	return 0, nil
}

func comparePre(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 63)
	bn, berr := strconv.ParseUint(b, 10, 63)
	switch {
	case aerr == nil && berr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// Identity is what an image says it is when asked.
type Identity struct {
	Product string `json:"product"`
	Version string `json:"version"`
}

// maxDescribeOutput bounds what the preflight will read from an image it has not yet trusted.
const maxDescribeOutput = 4096

// Describe asks the image who it is by running it as `<image> describe`: a scratch working
// directory, an empty environment (no credentials, no proxy, no PATH), a deadline, and bounded
// output. It must only be called on an image whose digest and signature have ALREADY verified:
// running it is running code that came off the network.
//
// This is NOT a sandbox. It does not restrict the network or the filesystem; a real device would
// run the preflight in a minimal-privilege environment (see the README).
func Describe(ctx context.Context, image string, timeout time.Duration) (Identity, error) {
	scratch, err := os.MkdirTemp("", "ota-preflight-*")
	if err != nil {
		return Identity{}, err
	}
	defer os.RemoveAll(scratch)
	if err := os.Chmod(image, 0o755); err != nil {
		return Identity{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, image, "describe")
	cmd.Dir = scratch
	cmd.WaitDelay = time.Second // a killed image that left a child holding the pipe must not hold us
	cmd.Env = []string{}
	var out limitedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return Identity{}, fmt.Errorf("image did not answer describe: %w", err)
	}
	if out.over {
		return Identity{}, errors.New("image describe output is over the size limit")
	}
	var id Identity
	dec := json.NewDecoder(strings.NewReader(out.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&id); err != nil {
		return Identity{}, fmt.Errorf("image describe output is not an identity: %w", err)
	}
	return id, nil
}

// CheckIdentity is the device-side binding the signature does not give: the verified image must
// say it is this device's product and the version the assignment named.
func CheckIdentity(got Identity, product, version string) error {
	if got.Product != product {
		return fail(CodeImageMismatch, "image is for product %q, this device is %q", got.Product, product)
	}
	if got.Version != version {
		return fail(CodeImageMismatch, "image says it is version %q, the assignment named %q", got.Version, version)
	}
	return nil
}

type limitedBuffer struct {
	b    strings.Builder
	over bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.b.Len()+len(p) > maxDescribeOutput {
		l.over = true
		return len(p), nil
	}
	return l.b.Write(p)
}

func (l *limitedBuffer) String() string { return l.b.String() }
