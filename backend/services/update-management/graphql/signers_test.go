// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-update-management/model"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

const registerSigner = `mutation($n: String!, $k: String!) { registerArtifactSigner(name: $n, publicKeyPem: $k) {
  token name algorithm keyId state revokedAt soleSignedArtifactCount publicKeyPem } }`

const addSignature = `mutation($a: String!, $s: String!, $sig: String!) {
  addArtifactSignature(artifactToken: $a, signerToken: $s, signature: $sig) { token trustStatus } }`

func newKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	require.NoError(t, err)
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func signContent(t *testing.T, k *ecdsa.PrivateKey, content []byte) string {
	t.Helper()
	sum := sha256.Sum256(content)
	sig, err := ecdsa.SignASN1(rand.Reader, k, sum[:])
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(sig)
}

// artifact:write alone cannot appoint a signer: who may publish a binary is not who may decide
// whose signature makes it trusted. And artifact:trust alone cannot publish or sign.
func TestRegisteringAndRevokingASignerNeedsArtifactTrust(t *testing.T) {
	p := newPlane(t)
	_, pub := newKey(t)
	vars := map[string]any{"n": "release", "k": pub}

	bare := p.ctx("acme")
	writer := p.ctx("acme", auth.ArtifactRead, auth.ArtifactWrite)
	trust := p.ctx("acme", auth.ArtifactTrust)

	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(bare, registerSigner, vars)))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(writer, registerSigner, vars)))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(writer, `mutation { revokeArtifactSigner(token:"x"){ token } }`, nil)))

	resp := p.exec(trust, registerSigner, vars)
	require.Empty(t, resp.Errors)
	var reg struct {
		RegisterArtifactSigner struct {
			Token, Name, Algorithm, KeyId, State, PublicKeyPem string
			RevokedAt                                          *string
			SoleSignedArtifactCount                            int
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &reg))
	s := reg.RegisterArtifactSigner
	require.Equal(t, "ECDSA_P256", s.Algorithm)
	require.Equal(t, "ACTIVE", s.State)
	require.Nil(t, s.RevokedAt)
	require.Len(t, s.KeyId, 64)

	// artifact:trust does not let a caller publish or sign.
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(trust, createMutation, createVars("fw", []byte("img")))))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(trust, addSignature,
		map[string]any{"a": "fw", "s": s.Token, "sig": "AA=="})))
	// Reading signers is artifact:read.
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(trust, `{ artifactSigner(token:"x"){ token } }`, nil)))
	require.Empty(t, p.exec(writer, `{ artifactSigners(criteria:{pageNumber:0,pageSize:5}){ results { token state } } }`, nil).Errors)

	resp = p.exec(trust, `mutation($t: String!) { revokeArtifactSigner(token: $t){ state revokedAt } }`, map[string]any{"t": s.Token})
	require.Empty(t, resp.Errors)
	require.Contains(t, string(resp.Data), `"state":"REVOKED"`)
}

func TestAPrivateKeyIsRefusedOverGraphQLWithoutAnEcho(t *testing.T) {
	p := newPlane(t)
	k, _ := newKey(t)
	der, err := x509.MarshalECPrivateKey(k)
	require.NoError(t, err)
	priv := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))

	resp := p.exec(p.ctx("acme", auth.ArtifactTrust), registerSigner, map[string]any{"n": "oops", "k": priv})
	require.Equal(t, model.CodeSignerKeyRefused, code(resp))
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, string(raw), base64.StdEncoding.EncodeToString(der)[10:50])
	require.NotContains(t, string(raw), "BEGIN EC")
}

func TestSigningThroughTheSchemaDerivesTrustAndRevocationUndoesIt(t *testing.T) {
	p := newPlane(t)
	content := []byte("image")
	writer := p.ctx("acme", auth.ArtifactRead, auth.ArtifactWrite)
	trust := p.ctx("acme", auth.ArtifactTrust, auth.ArtifactRead)
	require.Empty(t, p.exec(writer, createMutation, createVars("fw-1", content)).Errors)
	require.Empty(t, p.exec(writer, createMutation, createVars("fw-2", []byte("other"))).Errors)

	k, pub := newKey(t)
	resp := p.exec(trust, registerSigner, map[string]any{"n": "release", "k": pub})
	require.Empty(t, resp.Errors)
	var reg struct{ RegisterArtifactSigner struct{ Token string } }
	require.NoError(t, json.Unmarshal(resp.Data, &reg))
	signer := reg.RegisterArtifactSigner.Token

	// A signature over some other content is refused with its typed code.
	require.Equal(t, model.CodeSignatureInvalid, code(p.exec(writer, addSignature,
		map[string]any{"a": "fw-1", "s": signer, "sig": signContent(t, k, []byte("not the image"))})))
	resp = p.exec(writer, addSignature, map[string]any{"a": "fw-1", "s": signer, "sig": signContent(t, k, content)})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"addArtifactSignature":{"token":"fw-1","trustStatus":"SIGNED"}}`, string(resp.Data))

	list := func(filter string) string {
		resp := p.exec(writer, `{ artifacts(criteria:{pageNumber:0,pageSize:10`+filter+`}){ results { token trustStatus } } }`, nil)
		require.Empty(t, resp.Errors)
		return string(resp.Data)
	}
	require.JSONEq(t, `{"artifacts":{"results":[{"token":"fw-1","trustStatus":"SIGNED"}]}}`, list(", trustStatus: SIGNED"))
	require.JSONEq(t, `{"artifacts":{"results":[{"token":"fw-2","trustStatus":"UNVERIFIED"}]}}`, list(", trustStatus: UNVERIFIED"))

	// The revoke preview.
	resp = p.exec(trust, `query($t: String!) { artifactSigner(token: $t){ soleSignedArtifactCount } }`, map[string]any{"t": signer})
	require.JSONEq(t, `{"artifactSigner":{"soleSignedArtifactCount":1}}`, string(resp.Data))

	require.Empty(t, p.exec(trust, `mutation($t: String!) { revokeArtifactSigner(token: $t){ state } }`, map[string]any{"t": signer}).Errors)
	require.JSONEq(t, `{"artifacts":{"results":[]}}`, list(", trustStatus: SIGNED"))
	require.Equal(t, model.CodeSignerRevoked, code(p.exec(writer, addSignature,
		map[string]any{"a": "fw-2", "s": signer, "sig": signContent(t, k, []byte("other"))})))
	resp = p.exec(writer, `{ artifact(token:"fw-1"){ trustStatus } }`, nil)
	require.JSONEq(t, `{"artifact":{"trustStatus":"UNVERIFIED"}}`, string(resp.Data))
}

func TestAnotherTenantCannotSeeASignerThroughTheSchema(t *testing.T) {
	p := newPlane(t)
	_, pub := newKey(t)
	resp := p.exec(p.ctx("acme", auth.ArtifactTrust), registerSigner, map[string]any{"n": "k", "k": pub})
	require.Empty(t, resp.Errors)
	var reg struct{ RegisterArtifactSigner struct{ Token string } }
	require.NoError(t, json.Unmarshal(resp.Data, &reg))

	other := p.ctx("globex", auth.ArtifactRead, auth.ArtifactTrust)
	resp = p.exec(other, `query($t: String!) { artifactSigner(token: $t){ token }
	  artifactSigners(criteria:{pageNumber:0,pageSize:5}){ results { token } } }`, map[string]any{"t": reg.RegisterArtifactSigner.Token})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"artifactSigner":null,"artifactSigners":{"results":[]}}`, string(resp.Data))
	require.Equal(t, model.CodeSignerNotFound, code(p.exec(other,
		`mutation($t: String!) { revokeArtifactSigner(token: $t){ token } }`, map[string]any{"t": reg.RegisterArtifactSigner.Token})))
}

func TestServiceInfoReportsTheArtifactCeiling(t *testing.T) {
	p := newPlane(t)
	p.schema = gql.MustParseSchema(SchemaContent, &SchemaResolver{Area: "update-management", MaxArtifactBytes: 1 << 30})
	resp := p.exec(p.ctx("acme"), `{ updateManagementInfo { maxArtifactBytes } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateManagementInfo":{"maxArtifactBytes":"1073741824"}}`, string(resp.Data))
}

// F2: an artifact reached through an assignment carries its derived trust, on both the list path
// and the single-assignment path.
func TestAssignmentsCarryTheArtifactsDerivedTrust(t *testing.T) {
	p, _ := updatePlane(t, true)
	content := []byte("image")
	p.available("fw-1", content)
	writer := p.ctx("acme", auth.ArtifactRead, auth.ArtifactWrite)
	trust := p.ctx("acme", auth.ArtifactTrust)

	k, pub := newKey(t)
	resp := p.exec(trust, registerSigner, map[string]any{"n": "release", "k": pub})
	require.Empty(t, resp.Errors)
	var reg struct{ RegisterArtifactSigner struct{ Token string } }
	require.NoError(t, json.Unmarshal(resp.Data, &reg))
	require.Empty(t, p.exec(writer, addSignature,
		map[string]any{"a": "fw-1", "s": reg.RegisterArtifactSigner.Token, "sig": signContent(t, k, content)}).Errors)

	resp = p.exec(writer, assignMutation, map[string]any{"d": "dev-1", "a": "fw-1"})
	require.Empty(t, resp.Errors)
	var got struct{ AssignArtifact struct{ Token string } }
	require.NoError(t, json.Unmarshal(resp.Data, &got))

	resp = p.exec(writer, `{ updateAssignments(criteria:{pageNumber:0,pageSize:5}){ results { artifact { trustStatus } } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateAssignments":{"results":[{"artifact":{"trustStatus":"SIGNED"}}]}}`, string(resp.Data))

	resp = p.exec(writer, `mutation($t: String!) { cancelUpdate(assignmentToken: $t) { artifact { trustStatus } } }`,
		map[string]any{"t": got.AssignArtifact.Token})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"cancelUpdate":{"artifact":{"trustStatus":"SIGNED"}}}`, string(resp.Data))

	resp = p.exec(writer, `query($t: String!) { updateAssignment(token: $t) { artifact { trustStatus } } }`,
		map[string]any{"t": got.AssignArtifact.Token})
	require.JSONEq(t, `{"updateAssignment":{"artifact":{"trustStatus":"SIGNED"}}}`, string(resp.Data))
}
