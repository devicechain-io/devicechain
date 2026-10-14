// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// keyPair is a signer's private key and the PEM of its public half.
type keyPair struct {
	sign func(digest []byte) []byte
	pem  string
}

func spkiPEM(t *testing.T, pub any) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func newP256(t *testing.T) keyPair {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return keyPair{pem: spkiPEM(t, &k.PublicKey), sign: func(d []byte) []byte {
		sig, err := ecdsa.SignASN1(rand.Reader, k, d)
		require.NoError(t, err)
		return sig
	}}
}

func newEd25519(t *testing.T) keyPair {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return keyPair{pem: spkiPEM(t, pub), sign: func(d []byte) []byte { return ed25519.Sign(priv, d) }}
}

func rawDigest(content []byte) []byte {
	sum := sha256.Sum256(content)
	return sum[:]
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// declare creates an artifact (UPLOADING is enough: the signature is over its declared digest).
func (e *testEnv) declare(t *testing.T, tenant, token string, content []byte) *Artifact {
	t.Helper()
	req := validRequest(token, content)
	req.Name = "fw-" + token
	a, err := e.api.CreateArtifact(tenantCtx(tenant), req)
	require.NoError(t, err)
	return a
}

func (e *testEnv) register(t *testing.T, tenant, name string, kp keyPair) *ArtifactSigner {
	t.Helper()
	s, err := e.api.RegisterSigner(tenantCtx(tenant), name, kp.pem)
	require.NoError(t, err)
	return s
}

func (e *testEnv) trustOf(t *testing.T, tenant, token string) string {
	t.Helper()
	a, err := e.api.ArtifactByToken(tenantCtx(tenant), token)
	require.NoError(t, err)
	return a.Trust
}

func TestASignatureOverTheStoredDigestMakesTheArtifactSigned(t *testing.T) {
	for name, mk := range map[string]func(*testing.T) keyPair{"ecdsa-p256": newP256, "ed25519": newEd25519} {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			content := []byte("firmware bytes")
			env.declare(t, "acme", "fw-1", content)
			kp := mk(t)
			s := env.register(t, "acme", "release key", kp)
			require.Equal(t, SignerActive, s.State)
			require.Equal(t, TrustUnverified, env.trustOf(t, "acme", "fw-1"), "no signature yet")

			a, err := env.api.AddSignature(tenantCtx("acme"), "fw-1", s.Token, b64(kp.sign(rawDigest(content))))
			require.NoError(t, err)
			require.Equal(t, TrustSigned, a.Trust)
			require.Equal(t, TrustSigned, env.trustOf(t, "acme", "fw-1"))

			// Signing again with the same signer is idempotent.
			_, err = env.api.AddSignature(tenantCtx("acme"), "fw-1", s.Token, b64(kp.sign(rawDigest(content))))
			require.NoError(t, err)
			var n int64
			require.NoError(t, env.api.RDB.DB(tenantCtx("acme")).Model(&ArtifactSignature{}).Count(&n).Error)
			require.EqualValues(t, 1, n)
		})
	}
}

func TestKeyIdIsTheSha256OfTheSPKIDER(t *testing.T) {
	env := newTestEnv(t)
	kp := newEd25519(t)
	s := env.register(t, "acme", "k", kp)
	block, _ := pem.Decode([]byte(kp.pem))
	sum := sha256.Sum256(block.Bytes)
	require.Equal(t, hex.EncodeToString(sum[:]), s.KeyId)
	require.Equal(t, AlgorithmEd25519, s.Algorithm)

	_, err := env.api.RegisterSigner(tenantCtx("acme"), "again", kp.pem)
	requireRefusal(t, err, CodeSignerExists)
}

// NEGATIVE CONTROL: a perfectly valid signature, by a registered ACTIVE signer, over a digest that
// is not this artifact's must not attach. The control fails if verification used anything but the
// stored digest.
func TestAValidSignatureOverADifferentDigestIsRefused(t *testing.T) {
	for name, mk := range map[string]func(*testing.T) keyPair{"ecdsa-p256": newP256, "ed25519": newEd25519} {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			env.declare(t, "acme", "fw-1", []byte("the real image"))
			kp := mk(t)
			s := env.register(t, "acme", "k", kp)

			_, err := env.api.AddSignature(tenantCtx("acme"), "fw-1", s.Token, b64(kp.sign(rawDigest([]byte("some other image")))))
			requireRefusal(t, err, CodeSignatureInvalid)
			require.Equal(t, TrustUnverified, env.trustOf(t, "acme", "fw-1"))

			// The same control with the RIGHT digest passes, so the refusal above is the digest.
			_, err = env.api.AddSignature(tenantCtx("acme"), "fw-1", s.Token, b64(kp.sign(rawDigest([]byte("the real image")))))
			require.NoError(t, err)
		})
	}
}

func TestMalformedSignaturesAreRefused(t *testing.T) {
	env := newTestEnv(t)
	content := []byte("img")
	env.declare(t, "acme", "fw-1", content)
	s := env.register(t, "acme", "k", newP256(t))
	for name, sig := range map[string]string{
		"not base64": "***", "empty": "", "garbage bytes": b64([]byte("not a signature")),
		"oversized": b64(bytes.Repeat([]byte{1}, 4096)),
	} {
		_, err := env.api.AddSignature(tenantCtx("acme"), "fw-1", s.Token, sig)
		requireRefusal(t, err, CodeSignatureInvalid)
		_ = name
	}
}

func TestARevokedSignerCannotSignAndRevocationFlipsTrustWithoutAnArtifactWrite(t *testing.T) {
	env := newTestEnv(t)
	content := []byte("img")
	env.declare(t, "acme", "fw-1", content)
	env.declare(t, "acme", "fw-2", []byte("img2"))
	kp := newP256(t)
	s := env.register(t, "acme", "k", kp)
	_, err := env.api.AddSignature(tenantCtx("acme"), "fw-1", s.Token, b64(kp.sign(rawDigest(content))))
	require.NoError(t, err)
	require.Equal(t, TrustSigned, env.trustOf(t, "acme", "fw-1"))

	ctx := tenantCtx("acme")
	var before Artifact
	require.NoError(t, env.api.RDB.DB(ctx).Where("token = ?", "fw-1").First(&before).Error)

	revoked, err := env.api.RevokeSigner(ctx, s.Token)
	require.NoError(t, err)
	require.Equal(t, SignerRevoked, revoked.State)
	require.True(t, revoked.RevokedAt.Valid)
	require.Equal(t, "alice", revoked.RevokedBy)

	require.Equal(t, TrustUnverified, env.trustOf(t, "acme", "fw-1"), "derived trust drops with the signer")
	var after Artifact
	require.NoError(t, env.api.RDB.DB(ctx).Where("token = ?", "fw-1").First(&after).Error)
	require.Equal(t, before.UpdatedAt, after.UpdatedAt, "revocation writes no artifact row")
	require.Equal(t, before.TrustStatus, after.TrustStatus)
	require.Equal(t, TrustUnverified, after.TrustStatus, "the stored column is write-only UNVERIFIED")

	// Terminal: idempotent, and the signer cannot sign any more.
	again, err := env.api.RevokeSigner(ctx, s.Token)
	require.NoError(t, err)
	require.Equal(t, revoked.RevokedAt.Time.Unix(), again.RevokedAt.Time.Unix())
	_, err = env.api.AddSignature(ctx, "fw-2", s.Token, b64(kp.sign(rawDigest([]byte("img2")))))
	requireRefusal(t, err, CodeSignerRevoked)
	_, err = env.api.RevokeSigner(ctx, "nope")
	requireRefusal(t, err, CodeSignerNotFound)
}

func TestOnlyECDSAP256AndEd25519PublicKeysAreAccepted(t *testing.T) {
	env := newTestEnv(t)
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	for name, pemText := range map[string]string{
		"p384": spkiPEM(t, &p384.PublicKey), "rsa": spkiPEM(t, &rsaKey.PublicKey),
	} {
		_, err := env.api.RegisterSigner(tenantCtx("acme"), name, pemText)
		requireRefusal(t, err, CodeSignerKeyRefused)
	}
	for name, input := range map[string]string{
		"not pem": "hello", "empty": "", "wrong block type": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}})),
		"two blocks": newP256(t).pem + newP256(t).pem, "bad der": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte{1, 2}})),
	} {
		_, err := env.api.RegisterSigner(tenantCtx("acme"), "x", input)
		requireRefusal(t, err, CodeInvalidSigner)
		_ = name
	}
	_, err = env.api.RegisterSigner(tenantCtx("acme"), "", newP256(t).pem)
	requireRefusal(t, err, CodeInvalidSigner)
}

// A private key is refused, and what it was is not echoed or stored. Every PEM flavour of
// private key is tried, plus a truncated paste that would not decode.
func TestAPrivateKeyIsRefusedWithoutBeingEchoedLoggedOrStored(t *testing.T) {
	env := newTestEnv(t)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	require.NoError(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ecKey)
	require.NoError(t, err)
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	edDER, err := x509.MarshalPKCS8PrivateKey(edPriv)
	require.NoError(t, err)

	full := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}))
	inputs := map[string]struct {
		text   string
		secret string // a fragment of the key material that must appear nowhere in the outcome
	}{
		"ec":                            {full, b64(ecDER)[10:50]},
		"pkcs8":                         {string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})), b64(pkcs8)[10:50]},
		"ed25519":                       {string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edDER})), b64(edDER)[10:50]},
		"truncated":                     {full[:len(full)/2], b64(ecDER)[10:30]},
		"with the public key beside it": {newP256(t).pem + full, b64(ecDER)[10:50]},
	}

	// (Logging cannot be captured here: the repository forbids a test swapping the global logger.
	// ParsePublicKey and RegisterSigner never log, and the refusal is the only output.)
	for name, in := range inputs {
		require.Contains(t, in.text, "PRIVATE KEY", name)
		_, err := env.api.RegisterSigner(tenantCtx("acme"), "oops", in.text)
		requireRefusal(t, err, CodeSignerKeyRefused)
		require.NotContains(t, err.Error(), in.secret, name)
		require.NotContains(t, err.Error(), "BEGIN", name)
	}
	var n int64
	require.NoError(t, env.api.RDB.DB(tenantCtx("acme")).Model(&ArtifactSigner{}).Count(&n).Error)
	require.EqualValues(t, 0, n, "nothing was stored")
}

func TestAnotherTenantsSignerIsInvisibleAndUnusable(t *testing.T) {
	env := newTestEnv(t)
	content := []byte("img")
	env.declare(t, "acme", "fw-1", content)
	env.declare(t, "globex", "fw-1", content)
	kp := newP256(t)
	s := env.register(t, "acme", "k", kp)

	got, err := env.api.SignerByToken(tenantCtx("globex"), s.Token)
	require.NoError(t, err)
	require.Nil(t, got)
	list, err := env.api.Signers(tenantCtx("globex"), nil, rdb.Pagination{PageSize: 10})
	require.NoError(t, err)
	require.Empty(t, list.Results)

	_, err = env.api.AddSignature(tenantCtx("globex"), "fw-1", s.Token, b64(kp.sign(rawDigest(content))))
	requireRefusal(t, err, CodeSignerNotFound)
	_, err = env.api.RevokeSigner(tenantCtx("globex"), s.Token)
	requireRefusal(t, err, CodeSignerNotFound)
	require.Equal(t, TrustUnverified, env.trustOf(t, "globex", "fw-1"))

	// The same key may be registered in the other tenant: key ids are unique per tenant.
	other, err := env.api.RegisterSigner(tenantCtx("globex"), "k", kp.pem)
	require.NoError(t, err)
	require.Equal(t, s.KeyId, other.KeyId)
	// Its signature in globex does not sign acme's artifact.
	_, err = env.api.AddSignature(tenantCtx("globex"), "fw-1", other.Token, b64(kp.sign(rawDigest(content))))
	require.NoError(t, err)
	require.Equal(t, TrustSigned, env.trustOf(t, "globex", "fw-1"))
	require.Equal(t, TrustUnverified, env.trustOf(t, "acme", "fw-1"))
}

func TestSoleSignedArtifactCountPreviewsARevoke(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	contents := map[string][]byte{"fw-1": []byte("one"), "fw-2": []byte("two"), "fw-3": []byte("three")}
	for tok, c := range contents {
		env.declare(t, "acme", tok, c)
	}
	ka, kb := newP256(t), newEd25519(t)
	a, b := env.register(t, "acme", "a", ka), env.register(t, "acme", "b", kb)
	sign := func(s *ArtifactSigner, kp keyPair, tok string) {
		_, err := env.api.AddSignature(ctx, tok, s.Token, b64(kp.sign(rawDigest(contents[tok]))))
		require.NoError(t, err)
	}
	sign(a, ka, "fw-1") // only A
	sign(a, ka, "fw-2") // A and B
	sign(b, kb, "fw-2")
	sign(b, kb, "fw-3") // only B

	count := func(s *ArtifactSigner) int64 {
		fresh, err := env.api.SignerByToken(ctx, s.Token)
		require.NoError(t, err)
		n, err := env.api.SoleSignedArtifactCount(ctx, fresh)
		require.NoError(t, err)
		return n
	}
	require.EqualValues(t, 1, count(a), "revoking A would orphan fw-1 only; fw-2 keeps B")
	require.EqualValues(t, 1, count(b), "revoking B would orphan fw-3 only")

	_, err := env.api.RevokeSigner(ctx, b.Token)
	require.NoError(t, err)
	require.EqualValues(t, 2, count(a), "with B gone A is now the sole signer of fw-1 and fw-2")
	require.EqualValues(t, 0, count(b), "a revoked signer vouches for nothing")
	require.Equal(t, TrustUnverified, env.trustOf(t, "acme", "fw-3"))
	require.Equal(t, TrustSigned, env.trustOf(t, "acme", "fw-2"), "still signed by A")
}

func TestSearchFiltersOnDerivedTrust(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	content := []byte("one")
	env.declare(t, "acme", "fw-signed", content)
	env.declare(t, "acme", "fw-plain", []byte("two"))
	kp := newP256(t)
	s := env.register(t, "acme", "k", kp)
	_, err := env.api.AddSignature(ctx, "fw-signed", s.Token, b64(kp.sign(rawDigest(content))))
	require.NoError(t, err)

	tokens := func(status *string) []string {
		res, err := env.api.Artifacts(ctx, ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 10}, TrustStatus: status})
		require.NoError(t, err)
		out := []string{}
		for _, a := range res.Results {
			require.NotEmpty(t, a.Trust, "every listed artifact carries its derived trust")
			out = append(out, a.Token+"="+a.Trust)
		}
		return out
	}
	signed, unverified := TrustSigned, TrustUnverified
	require.ElementsMatch(t, []string{"fw-signed=SIGNED", "fw-plain=UNVERIFIED"}, tokens(nil))
	require.Equal(t, []string{"fw-signed=SIGNED"}, tokens(&signed))
	require.Equal(t, []string{"fw-plain=UNVERIFIED"}, tokens(&unverified))

	_, err = env.api.RevokeSigner(ctx, s.Token)
	require.NoError(t, err)
	require.Empty(t, tokens(&signed))
	require.Len(t, tokens(&unverified), 2)

	bad := "TRUSTED"
	_, err = env.api.Artifacts(ctx, ArtifactSearchCriteria{TrustStatus: &bad})
	requireRefusal(t, err, CodeInvalidTrustFilter)
}

// Trust for a page of artifacts costs a fixed number of queries, not one per artifact.
func TestTrustForAListIsBatched(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	kp := newP256(t)
	s := env.register(t, "acme", "k", kp)

	queries := func() int64 {
		var n atomic.Int64
		db := env.api.RDB.Database
		name := fmt.Sprintf("count-%d", len(t.Name()))
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, func(*gorm.DB) { n.Add(1) }))
		defer func() { _ = db.Callback().Query().Remove(name) }()
		_, err := env.api.Artifacts(ctx, ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 50}})
		require.NoError(t, err)
		return n.Load()
	}
	add := func(i int) {
		content := []byte(fmt.Sprintf("img-%d", i))
		env.declare(t, "acme", fmt.Sprintf("fw-%d", i), content)
		if i%2 == 0 {
			_, err := env.api.AddSignature(ctx, fmt.Sprintf("fw-%d", i), s.Token, b64(kp.sign(rawDigest(content))))
			require.NoError(t, err)
		}
	}
	for i := 0; i < 2; i++ {
		add(i)
	}
	few := queries()
	for i := 2; i < 12; i++ {
		add(i)
	}
	require.Equal(t, few, queries(), "the query count does not grow with the page")
}

func TestUnsignedArtifactIsRefusedByTheGateAndASignedOneIsNot(t *testing.T) {
	e := newUpdateEnv(t)
	e.api.AllowUnverified = false
	content := []byte("image")
	a := e.availableArtifact(t, "acme", "fw-1", "1.0.0", content)
	ctx := tenantCtx("acme")

	_, err := e.api.AssignArtifact(ctx, "dev-1", a.Token)
	requireRefusal(t, err, CodeUnverifiedRefused)

	kp := newEd25519(t)
	s := e.register(t, "acme", "k", kp)
	_, err = e.api.AddSignature(ctx, a.Token, s.Token, b64(kp.sign(rawDigest(content))))
	require.NoError(t, err)
	view, err := e.api.AssignArtifact(ctx, "dev-1", a.Token)
	require.NoError(t, err)
	require.Equal(t, TrustSigned, view.Artifact.Trust)
	require.NotNil(t, view)

	// Revoking the signer closes the gate again for the next device.
	_, err = e.api.RevokeSigner(ctx, s.Token)
	require.NoError(t, err)
	_, err = e.api.AssignArtifact(ctx, "dev-2", a.Token)
	requireRefusal(t, err, CodeUnverifiedRefused)

	// The instance flag still opens it, unchanged.
	e.api.AllowUnverified = true
	_, err = e.api.AssignArtifact(ctx, "dev-2", a.Token)
	require.NoError(t, err)
}

func TestSignerNameAndStoredKeyAreBounded(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.api.RegisterSigner(tenantCtx("acme"), strings.Repeat("n", 129), newP256(t).pem)
	requireRefusal(t, err, CodeInvalidSigner)
	_, err = env.api.RegisterSigner(tenantCtx("acme"), "k", strings.Repeat("A", maxPEMBytes+1))
	requireRefusal(t, err, CodeInvalidSigner)
}

// F1: revocation does not free the key. A revoked signer still holds its (tenant, key) slot, so
// the same key cannot be registered again as a fresh ACTIVE signer.
func TestARevokedKeyCannotBeRegisteredAgain(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	kp := newP256(t)
	s := env.register(t, "acme", "k", kp)
	_, err := env.api.RevokeSigner(ctx, s.Token)
	require.NoError(t, err)

	_, err = env.api.RegisterSigner(ctx, "k again", kp.pem)
	requireRefusal(t, err, CodeSignerExists)

	var rows []ArtifactSigner
	require.NoError(t, env.api.RDB.DB(ctx).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, SignerRevoked, rows[0].State)
}

// F3: forged signature rows - written straight into the table, as a bug or a bad restore might -
// never make another tenant's artifact SIGNED, in the read or in the trustStatus filter.
func TestForgedCrossTenantSignatureRowsDoNotSignAnArtifact(t *testing.T) {
	env := newTestEnv(t)
	ctx := tenantCtx("acme")
	content := []byte("img")
	art := env.declare(t, "acme", "fw-1", content)
	acmeSigner := env.register(t, "acme", "a", newP256(t))
	globexSigner := env.register(t, "globex", "g", newEd25519(t))
	db := env.api.RDB.Database

	insert := func(tenant string, signerID uint) {
		require.NoError(t, db.Exec(`INSERT INTO artifact_signatures (tenant_id, artifact_id, signer_id, signature)
			VALUES (?, ?, ?, x'01')`, tenant, art.ID, signerID).Error)
	}
	check := func(what string) {
		require.Equal(t, TrustUnverified, env.trustOf(t, "acme", "fw-1"), what)
		signed := TrustSigned
		res, err := env.api.Artifacts(ctx, ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 10}, TrustStatus: &signed})
		require.NoError(t, err)
		require.Empty(t, res.Results, what+" (filter)")
		unv := TrustUnverified
		res, err = env.api.Artifacts(ctx, ArtifactSearchCriteria{Pagination: rdb.Pagination{PageSize: 10}, TrustStatus: &unv})
		require.NoError(t, err)
		require.Len(t, res.Results, 1, what+" (filter)")
	}

	// A row stamped with another tenant, naming this tenant's artifact, by that tenant's active signer.
	insert("globex", globexSigner.ID)
	check("globex-stamped signature")
	require.NoError(t, db.Exec(`DELETE FROM artifact_signatures`).Error)

	// A row stamped with this tenant but by another tenant's active signer.
	insert("acme", globexSigner.ID)
	check("signature by another tenant's signer")
	require.NoError(t, db.Exec(`DELETE FROM artifact_signatures`).Error)

	// A row stamped with another tenant that names this tenant's own active signer.
	insert("globex", acmeSigner.ID)
	check("globex-stamped signature by an acme signer")
	require.NoError(t, db.Exec(`DELETE FROM artifact_signatures`).Error)

	// Control: the same shape with this tenant's own active signer does count.
	insert("acme", acmeSigner.ID)
	require.Equal(t, TrustSigned, env.trustOf(t, "acme", "fw-1"))
}

// The artifact a download resolves to carries its derived trust, like every other read path.
func TestTheDownloadTargetCarriesDerivedTrust(t *testing.T) {
	e := newUpdateEnv(t)
	content := []byte("firmware one")
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", content)
	ctx := tenantCtx("acme")
	kp := newP256(t)
	s := e.register(t, "acme", "k", kp)
	_, err := e.api.AddSignature(ctx, "fw-1", s.Token, b64(kp.sign(rawDigest(content))))
	require.NoError(t, err)
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	c := &Capability{Tenant: "acme", DeviceToken: "dev-1", AssignmentID: v.Assignment.Token,
		Digest: v.Attempt.TargetDigest, ArtifactTok: "fw-1", Expires: e.clock.Add(1e9)}
	target, err := e.api.ResolveDownload(ctx, c)
	require.NoError(t, err)
	require.Equal(t, TrustSigned, target.Artifact.Trust)
}
