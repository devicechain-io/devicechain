// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEveryRegistryAlgorithmMapsToAValidWireAlgorithm(t *testing.T) {
	want := map[string]string{AlgorithmECDSAP256: ota.AlgorithmECDSAP256SHA256, AlgorithmEd25519: ota.AlgorithmEd25519}
	require.Len(t, RegistryAlgorithms, len(want))
	for _, reg := range RegistryAlgorithms {
		wire, err := WireAlgorithm(reg)
		require.NoError(t, err, reg)
		require.Equal(t, want[reg], wire)
	}
	_, err := WireAlgorithm("RSA_2048")
	require.Error(t, err, "an unknown algorithm is an error, not a guess")
}

// lastAssignment decodes the most recent message published to the device.
func (e *updateEnv) lastAssignment(t *testing.T, device string) ota.UpdateAssignment {
	t.Helper()
	require.NotEmpty(t, e.desired.sent)
	for i := len(e.desired.sent) - 1; i >= 0; i-- {
		if e.desired.sent[i].device != device {
			continue
		}
		m, err := ota.DecodeDesiredUpdate(e.desired.sent[i].body)
		require.NoError(t, err)
		require.NotNil(t, m.Assignment)
		return *m.Assignment
	}
	t.Fatalf("nothing published to %s", device)
	return ota.UpdateAssignment{}
}

// signed returns a signed artifact and the signers that signed it, in signing order.
func (e *updateEnv) signedBy(t *testing.T, tenant, token string, content []byte, kps ...keyPair) []*ArtifactSigner {
	t.Helper()
	ctx := tenantCtx(tenant)
	out := make([]*ArtifactSigner, 0, len(kps))
	for i, kp := range kps {
		s := e.register(t, tenant, "signer-"+token+"-"+string(rune('a'+i)), kp)
		_, err := e.api.AddSignature(ctx, token, s.Token, b64(kp.sign(rawDigest(content))))
		require.NoError(t, err)
		// created_at has the clock's resolution; space the signatures so "oldest" is unambiguous.
		time.Sleep(5 * time.Millisecond)
		out = append(out, s)
	}
	return out
}

func TestAssignmentPinsTheOldestActiveSignatureAndSendsItAsSigned(t *testing.T) {
	e := newUpdateEnv(t)
	e.api.AllowUnverified = false
	content := []byte("firmware one")
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", content)
	ec, ed := newP256(t), newEd25519(t)
	signers := e.signedBy(t, "acme", "fw-1", content, ec, ed)

	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	require.NoError(t, err)
	require.True(t, v.Assignment.SignatureId.Valid)
	require.NotNil(t, v.Pin)
	require.Equal(t, signers[0].Token, v.Pin.Signer.Token, "the oldest signature wins")

	m := e.lastAssignment(t, "dev-1")
	require.Equal(t, ota.TrustSigned, m.Trust)
	require.Equal(t, signers[0].KeyId, m.KeyID)
	require.Equal(t, ota.AlgorithmECDSAP256SHA256, m.Algorithm, "the registry name is mapped to the wire name")
	require.Equal(t, b64(v.Pin.Signature.Signature), m.Signature)
	require.NoError(t, m.Validate())

	// Read back, the assignment still names the same signer.
	got, err := e.api.AssignmentByToken(tenantCtx("acme"), v.Assignment.Token)
	require.NoError(t, err)
	require.Equal(t, signers[0].Token, got.Pin.Signer.Token)
}

func TestARevokedSignersSignatureIsSkippedWhenPinning(t *testing.T) {
	e := newUpdateEnv(t)
	e.api.AllowUnverified = false
	content := []byte("firmware one")
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", content)
	signers := e.signedBy(t, "acme", "fw-1", content, newP256(t), newEd25519(t))
	_, err := e.api.RevokeSigner(tenantCtx("acme"), signers[0].Token)
	require.NoError(t, err)

	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	require.NoError(t, err)
	require.Equal(t, signers[1].Token, v.Pin.Signer.Token)
	require.Equal(t, ota.AlgorithmEd25519, e.lastAssignment(t, "dev-1").Algorithm)
}

func TestUnverifiedAssignmentUnderTheFlagPinsNothingAndStaysUnverified(t *testing.T) {
	e := newUpdateEnv(t) // AllowUnverified is on
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))

	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	require.NoError(t, err)
	require.False(t, v.Assignment.SignatureId.Valid)
	require.Nil(t, v.Pin)
	m := e.lastAssignment(t, "dev-1")
	require.Equal(t, ota.TrustUnverified, m.Trust)
	require.Empty(t, m.Signature)
}

// A revoke after the pin does not change what an in-flight attempt is told, and a new assignment
// of the same artifact is refused.
func TestARevokeAfterThePinLeavesTheInFlightWireIdenticalAndRefusesNewAssignments(t *testing.T) {
	e := newUpdateEnv(t)
	e.api.AllowUnverified = false
	content := []byte("firmware one")
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", content)
	signers := e.signedBy(t, "acme", "fw-1", content, newEd25519(t))
	ctx := tenantCtx("acme")

	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	first := e.desired.sent[len(e.desired.sent)-1].body

	_, err = e.api.RevokeSigner(ctx, signers[0].Token)
	require.NoError(t, err)

	views, err := e.api.CurrentUpdates(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, TrustUnverified, views[0].Artifact.Trust, "the artifact's derived trust did fall")
	e.api.PublishSyncAnswer(ctx, "dev-1", views)
	again := e.desired.sent[len(e.desired.sent)-1].body
	require.Equal(t, string(first), string(again), "the re-synced message is byte-identical")
	require.Equal(t, signers[0].Token, views[0].Pin.Signer.Token)
	require.Equal(t, v.Assignment.Token, views[0].Assignment.Token)

	_, err = e.api.AssignArtifact(ctx, "dev-2", "fw-1")
	requireRefusal(t, err, CodeUnverifiedRefused)
}

// The gate read the artifact's trust outside the assignment transaction. A signer revoked between
// that read and the pin is caught by the pin itself.
func TestTheTransactionRefusesWhenTheOnlySignerWasRevokedAfterTheGate(t *testing.T) {
	e := newUpdateEnv(t)
	e.api.AllowUnverified = false
	content := []byte("firmware one")
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", content)
	signers := e.signedBy(t, "acme", "fw-1", content, newEd25519(t))
	ctx := tenantCtx("acme")
	stale, err := e.api.ArtifactByToken(ctx, "fw-1")
	require.NoError(t, err)
	require.Equal(t, TrustSigned, stale.Trust)

	_, err = e.api.RevokeSigner(ctx, signers[0].Token)
	require.NoError(t, err)

	err = e.api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := e.api.assignInTx(ctx, tx, "dev-1", stale)
		return err
	})
	requireRefusal(t, err, CodeUnverifiedRefused)
}

// If the platform decided an assignment is signed and can no longer read what it pinned, it sends
// nothing. It never falls back to UNVERIFIED.
func TestAnUnreadablePinIsNeverDowngradedToUnverified(t *testing.T) {
	for name, damage := range map[string]func(e *updateEnv, signer *ArtifactSigner){
		"the signer row is gone": func(e *updateEnv, s *ArtifactSigner) {
			require.NoError(t, e.api.RDB.DB(tenantCtx("acme")).Delete(&ArtifactSigner{}, s.ID).Error)
		},
		"the signature row is gone": func(e *updateEnv, s *ArtifactSigner) {
			require.NoError(t, e.api.RDB.DB(tenantCtx("acme")).Where("signer_id = ?", s.ID).Delete(&ArtifactSignature{}).Error)
		},
		"the signer algorithm is unknown": func(e *updateEnv, s *ArtifactSigner) {
			require.NoError(t, e.api.RDB.DB(tenantCtx("acme")).Model(&ArtifactSigner{ID: s.ID, TokenReference: s.TokenReference}).
				Update("algorithm", "RSA_2048").Error)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newUpdateEnv(t) // the flag is ON: even so, a pinned assignment is not downgraded
			content := []byte("firmware one")
			e.availableArtifact(t, "acme", "fw-1", "1.0.0", content)
			signers := e.signedBy(t, "acme", "fw-1", content, newEd25519(t))
			ctx := tenantCtx("acme")
			_, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
			require.NoError(t, err)
			require.Len(t, e.desired.sent, 1)

			damage(e, signers[0])

			views, err := e.api.CurrentUpdates(ctx, "dev-1")
			require.NoError(t, err)
			require.Len(t, views, 1)
			require.True(t, views[0].Assignment.SignatureId.Valid)
			e.api.PublishSyncAnswer(ctx, "dev-1", views)
			require.Len(t, e.desired.sent, 1, "nothing further was sent")
		})
	}
}

// Assigning the same artifact again while its update is in flight returns the existing assignment:
// the same pin, and the message re-sent is byte-identical to the first.
func TestReassigningTheSameArtifactKeepsThePinAndTheMessage(t *testing.T) {
	e := newUpdateEnv(t)
	e.api.AllowUnverified = false
	content := []byte("firmware one")
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", content)
	first := e.signedBy(t, "acme", "fw-1", content, newEd25519(t))
	ctx := tenantCtx("acme")

	v1, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	original := e.desired.sent[len(e.desired.sent)-1].body

	// A second signer arrives afterwards; it must not move the pin.
	e.signedBy(t, "acme", "fw-1", content, newP256(t))
	v2, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	require.Equal(t, v1.Assignment.Token, v2.Assignment.Token)
	require.Equal(t, v1.Assignment.SignatureId, v2.Assignment.SignatureId)
	require.NotNil(t, v2.Pin)
	require.Equal(t, first[0].Token, v2.Pin.Signer.Token)
	require.Len(t, e.desired.sent, 2)
	require.Equal(t, string(original), string(e.desired.sent[1].body))
}

// An unsigned assignment made while the flag was on is not (re)sent once the flag is off.
func TestAnUnpinnedAssignmentIsNotSentWhileTheInstanceRefusesUnverifiedArtifacts(t *testing.T) {
	e := newUpdateEnv(t) // flag on
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	_, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	require.Len(t, e.desired.sent, 1)
	require.Zero(t, e.api.Undelivered.Load())

	e.api.AllowUnverified = false
	views, err := e.api.CurrentUpdates(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, views, 1)
	logs := logSink.Capture(t)
	e.api.PublishSyncAnswer(ctx, "dev-1", views)
	require.Len(t, e.desired.sent, 1, "nothing was sent")
	require.EqualValues(t, 1, e.api.Undelivered.Load())
	require.Contains(t, logs.String(), "not delivered")

	// Flag back on: it is delivered again, unchanged.
	e.api.AllowUnverified = true
	e.api.PublishSyncAnswer(ctx, "dev-1", views)
	require.Len(t, e.desired.sent, 2)
}

// A pin naming a signature of some OTHER artifact is unreadable, whatever signer it names.
func TestAPinNamingAnotherArtifactsSignatureIsNotSent(t *testing.T) {
	e := newUpdateEnv(t)
	e.api.AllowUnverified = false
	c1, c2 := []byte("firmware one"), []byte("firmware two")
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", c1)
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", c2)
	e.signedBy(t, "acme", "fw-1", c1, newEd25519(t))
	other := e.signedBy(t, "acme", "fw-2", c2, newEd25519(t))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	require.Len(t, e.desired.sent, 1)

	var foreign ArtifactSignature
	require.NoError(t, e.api.RDB.DB(ctx).Where("signer_id = ?", other[0].ID).First(&foreign).Error)
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAssignment{ID: v.Assignment.ID, Token: v.Assignment.Token}).
		Update("signature_id", foreign.ID).Error)

	views, err := e.api.CurrentUpdates(ctx, "dev-1")
	require.NoError(t, err)
	require.Nil(t, views[0].Pin)
	e.api.PublishSyncAnswer(ctx, "dev-1", views)
	require.Len(t, e.desired.sent, 1)
}
