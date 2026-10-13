// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoldenDownloadRequestFixtures(t *testing.T) {
	valid, err := filepath.Glob("testdata/update/valid/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, valid)
	for _, f := range valid {
		b, err := os.ReadFile(f)
		require.NoError(t, err, f)
		r, err := DecodeDeviceReport(b)
		require.NoError(t, err, f)
		require.NotNil(t, r.DownloadRequest, f)
		assert.Equal(t, KindDownloadRequest, r.Kind)
		assert.Nil(t, r.Update)
		assert.Nil(t, r.Sync)
		assert.Nil(t, r.ConfigStatus)
	}
	invalid, err := filepath.Glob("testdata/update/invalid/*.json")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(invalid), 5)
	for _, f := range invalid {
		b, err := os.ReadFile(f)
		require.NoError(t, err, f)
		want, err := os.ReadFile(strings.TrimSuffix(f, ".json") + ".err")
		require.NoError(t, err, "every invalid fixture needs a sibling .err file: %s", f)
		_, err = DecodeDeviceReport(b)
		if assert.Error(t, err, f) {
			assert.Contains(t, err.Error(), strings.TrimSpace(string(want)), f)
		}
	}
}

// The three down messages are the things the platform ENCODES, so their golden bytes are pinned: a
// device in any language parses exactly these.
func TestDesiredUpdateFixturesAreWhatTheEncodersProduce(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/update/desired/" + name)
		require.NoError(t, err)
		return b
	}

	// UNVERIFIED stays byte-identical to what it was before signatures existed; each algorithm
	// has a SIGNED fixture beside it.
	for name, trust := range map[string]string{
		"update-assignment.json":                TrustUnverified,
		"update-assignment-signed-ecdsa.json":   TrustSigned,
		"update-assignment-signed-ed25519.json": TrustSigned,
	} {
		want := read(name)
		m, err := DecodeDesiredUpdate(want)
		require.NoError(t, err, name)
		require.NotNil(t, m.Assignment, name)
		require.Equal(t, trust, m.Assignment.Trust, name)
		var sig *AssignmentSignature
		if trust == TrustSigned {
			sig = &AssignmentSignature{Signature: m.Assignment.Signature, KeyID: m.Assignment.KeyID, Algorithm: m.Assignment.Algorithm}
		}
		got, err := EncodeUpdateAssignment(m.Assignment.AttemptID, m.Assignment.AssignmentID, m.Assignment.Component, trust, sig, m.Assignment.Artifact)
		require.NoError(t, err, name)
		assert.Equal(t, string(want), string(got), name)
	}

	want := read("download-grant.json")
	m, err := DecodeDesiredUpdate(want)
	require.NoError(t, err)
	require.NotNil(t, m.Grant)
	exp, err := m.Grant.Expiry()
	require.NoError(t, err)
	got, err := EncodeDownloadGrant(m.Grant.AssignmentID, m.Grant.URL, exp)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	want = read("update-cancel.json")
	m, err = DecodeDesiredUpdate(want)
	require.NoError(t, err)
	require.NotNil(t, m.Cancel)
	got, err = EncodeUpdateCancel(m.Cancel.AttemptID, m.Cancel.AssignmentID)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

func TestEncodersRefuseWhatADeviceCouldNotTrust(t *testing.T) {
	art := AssignmentArtifact{Version: "1", Digest: "sha256:" + strings.Repeat("a", 64), Size: 10}
	_, err := EncodeUpdateAssignment("att-1", "asg-1", "firmware", TrustUnverified, nil, art)
	require.NoError(t, err)

	bad := art
	bad.Digest = "sha256:xyz"
	_, err = EncodeUpdateAssignment("att-1", "asg-1", "firmware", TrustUnverified, nil, bad)
	assert.Error(t, err)
	bad = art
	bad.Size = 0
	_, err = EncodeUpdateAssignment("att-1", "asg-1", "firmware", TrustUnverified, nil, bad)
	assert.ErrorContains(t, err, "size")
	_, err = EncodeUpdateAssignment("att 1", "asg-1", "firmware", TrustUnverified, nil, art)
	assert.Error(t, err)
	_, err = EncodeUpdateAssignment("att-1", "asg-1", "Firmware", TrustUnverified, nil, art)
	assert.Error(t, err)

	exp := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for name, u := range map[string]string{
		"not absolute":   "/updates/download/x?cap=1",
		"wrong scheme":   "ftp://x.test/a",
		"no host":        "https:///a",
		"empty":          "",
		"over the bound": "https://x.test/" + strings.Repeat("a", MaxGrantURLBytes),
	} {
		_, err = EncodeDownloadGrant("asg-1", u, exp)
		assert.Error(t, err, name)
	}
	_, err = EncodeUpdateCancel("", "asg-1")
	assert.NoError(t, err, "a cancel for an assignment the platform does not hold names no attempt")
	_, err = EncodeUpdateCancel("", "")
	assert.Error(t, err)
}

func TestSignedAssignmentGrammar(t *testing.T) {
	art := AssignmentArtifact{Version: "1", Digest: "sha256:" + strings.Repeat("a", 64), Size: 10}
	sig64 := base64.StdEncoding.EncodeToString(make([]byte, 64))
	good := AssignmentSignature{Signature: sig64, KeyID: strings.Repeat("a", 64), Algorithm: AlgorithmEd25519}
	enc := func(trust string, s *AssignmentSignature) error {
		_, err := EncodeUpdateAssignment("att-1", "asg-1", "firmware", trust, s, art)
		return err
	}
	require.NoError(t, enc(TrustSigned, &good))
	g := good
	g.Algorithm = AlgorithmECDSAP256SHA256
	g.Signature = base64.StdEncoding.EncodeToString(make([]byte, 70))
	require.NoError(t, enc(TrustSigned, &g))

	assert.ErrorContains(t, enc(TrustSigned, nil), "requires signature", "SIGNED without a signature is refused")
	for name, mut := range map[string]func(*AssignmentSignature){
		"no signature":  func(s *AssignmentSignature) { s.Signature = "" },
		"no keyId":      func(s *AssignmentSignature) { s.KeyID = "" },
		"no algorithm":  func(s *AssignmentSignature) { s.Algorithm = "" },
		"bad algorithm": func(s *AssignmentSignature) { s.Algorithm = "RSA_PKCS1" },
		"upper keyId":   func(s *AssignmentSignature) { s.KeyID = strings.Repeat("A", 64) },
		"short keyId":   func(s *AssignmentSignature) { s.KeyID = strings.Repeat("a", 63) },
		"not base64":    func(s *AssignmentSignature) { s.Signature = "!!!" },
		"url base64":    func(s *AssignmentSignature) { s.Signature = strings.Repeat("_-", 32) },
		"ed25519 len":   func(s *AssignmentSignature) { s.Signature = base64.StdEncoding.EncodeToString(make([]byte, 63)) },
		"oversize":      func(s *AssignmentSignature) { s.Signature = strings.Repeat("A", maxSignatureB64+4) },
	} {
		c := good
		mut(&c)
		assert.Error(t, enc(TrustSigned, &c), name)
	}
	assert.ErrorContains(t, enc(TrustUnverified, &good), "must not carry", "UNVERIFIED with a signature is refused")
	assert.ErrorContains(t, enc("VERIFIED", &good), "unknown trust")

	// The same rules hold on decode.
	body := func(extra string) string {
		return `{"v":1,"kind":"update.assignment","attemptId":"a","assignmentId":"b","component":"firmware",` +
			`"trust":"UNVERIFIED","artifact":{"version":"1","digest":"sha256:` + strings.Repeat("a", 64) + `","size":1,"requiresReboot":false}` + extra + `}`
	}
	_, err := DecodeDesiredUpdate([]byte(body("")))
	require.NoError(t, err)
	_, err = DecodeDesiredUpdate([]byte(body(`,"keyId":"` + strings.Repeat("a", 64) + `"`)))
	assert.Error(t, err)
	_, err = DecodeDesiredUpdate([]byte(strings.Replace(body(""), "UNVERIFIED", "SIGNED", 1)))
	assert.Error(t, err)
}

func TestDecodeDesiredUpdateIsStrict(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	for name, body := range map[string]string{
		"unknown field": `{"v":1,"kind":"update.cancel","assignmentId":"asg-1","extra":1}`,
		"trailing":      `{"v":1,"kind":"update.cancel","assignmentId":"asg-1"} {}`,
		"wrong trust": `{"v":1,"kind":"update.assignment","attemptId":"a","assignmentId":"b","component":"firmware",` +
			`"trust":"VERIFIED","artifact":{"version":"1","digest":"` + d + `","size":1,"requiresReboot":false}}`,
		"artifact extra field": `{"v":1,"kind":"update.assignment","attemptId":"a","assignmentId":"b","component":"firmware",` +
			`"trust":"UNVERIFIED","artifact":{"version":"1","digest":"` + d + `","size":1,"requiresReboot":false,"x":1}}`,
		"a config kind": `{"v":1,"kind":"config.revision","revision":1,"digest":"` + d + `","document":"{}"}`,
		"bad expiry":    `{"v":1,"kind":"download.grant","assignmentId":"a","url":"https://x.test/","expiresAt":"tomorrow"}`,
		"duplicate key": `{"v":1,"kind":"update.cancel","assignmentId":"a","assignmentId":"b"}`,
	} {
		_, err := DecodeDesiredUpdate([]byte(body))
		assert.Error(t, err, name)
	}
	_, err := DecodeDesiredUpdate([]byte(strings.Repeat(" ", MaxAssignmentBytes+1)))
	assert.ErrorContains(t, err, "exceeds")
}

// The kinds do not blend: an update report may not carry a grant's fields, and a download
// request may not carry an update's.
func TestDownloadKindsDoNotBlend(t *testing.T) {
	_, err := DecodeDeviceReport([]byte(`{"v":1,"kind":"update.inventory","component":"firmware","bootId":"b",` +
		`"running":{"version":"1"},"url":"https://x.test/"}`))
	assert.Error(t, err)
	_, err = DecodeDeviceReport([]byte(`{"v":1,"kind":"download.request","assignmentId":"a","attemptId":"b"}`))
	assert.Error(t, err)
	// The signature fields are known to the key scan so an assignment can carry them, but an
	// UP report must still refuse them: its struct has no such field.
	for _, extra := range []string{`"signature":"AAAA"`, `"keyId":"` + strings.Repeat("a", 64) + `"`, `"algorithm":"ED25519"`} {
		_, err = DecodeDeviceReport([]byte(`{"v":1,"kind":"update.progress","attemptId":"a","assignmentId":"b","component":"firmware",` +
			`"artifactDigest":"sha256:` + strings.Repeat("a", 64) + `","seq":1,"stage":"RECEIVED","bootId":"b",` + extra + `}`))
		assert.Error(t, err, extra)
		_, err = DecodeDeviceReport([]byte(`{"v":1,"kind":"update.inventory","component":"firmware","bootId":"b",` +
			`"running":{"version":"1"},` + extra + `}`))
		assert.Error(t, err, extra)
	}
	// A down kind is not an up kind.
	_, err = DecodeDeviceReport([]byte(`{"v":1,"kind":"download.grant","assignmentId":"a","url":"https://x.test/","expiresAt":"2026-10-11T12:00:00Z"}`))
	assert.ErrorContains(t, err, "unknown report kind")
}

// The SIGNED fixtures are genuinely signed: each verifies under its own public key over the raw
// 32 bytes of the artifact digest (not the text), and its keyId is the SHA-256 of that key's DER
// SubjectPublicKeyInfo. A fixture that merely parsed would let every implementation agree on a
// signature nobody can verify.
func TestSignedFixturesVerify(t *testing.T) {
	for name, keyFile := range map[string]string{
		"update-assignment-signed-ecdsa.json":   "signer-ecdsa-p256.pub.pem",
		"update-assignment-signed-ed25519.json": "signer-ed25519.pub.pem",
	} {
		raw, err := os.ReadFile("testdata/update/desired/" + name)
		require.NoError(t, err)
		m, err := DecodeDesiredUpdate(raw)
		require.NoError(t, err, name)
		a := m.Assignment
		pemBytes, err := os.ReadFile("testdata/update/keys/" + keyFile)
		require.NoError(t, err)
		blk, _ := pem.Decode(pemBytes)
		require.NotNil(t, blk, name)
		pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
		require.NoError(t, err, name)
		der, err := x509.MarshalPKIXPublicKey(pub)
		require.NoError(t, err, name)
		id := sha256.Sum256(der)
		assert.Equal(t, hex.EncodeToString(id[:]), a.KeyID, name)
		digest, err := hex.DecodeString(strings.TrimPrefix(a.Artifact.Digest, "sha256:"))
		require.NoError(t, err)
		sig, err := base64.StdEncoding.DecodeString(a.Signature)
		require.NoError(t, err)
		switch a.Algorithm {
		case AlgorithmECDSAP256SHA256:
			assert.True(t, ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), digest, sig), name)
			assert.False(t, ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), []byte(a.Artifact.Digest), sig), "the text form is not what is signed")
		case AlgorithmEd25519:
			assert.True(t, ed25519.Verify(pub.(ed25519.PublicKey), digest, sig), name)
			assert.False(t, ed25519.Verify(pub.(ed25519.PublicKey), []byte(a.Artifact.Digest), sig), "the text form is not what is signed")
		default:
			t.Fatalf("%s: algorithm %s", name, a.Algorithm)
		}
	}
}
