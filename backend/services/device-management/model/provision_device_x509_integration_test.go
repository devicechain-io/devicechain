// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Registration by certificate against a REAL PostgreSQL: the locks and the races that the SQLite
// fixture silently drops (provision_device_integration_test.go says why). The anchor interleaving
// is FORCED, not raced.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'CertificateRegistration' -v
package model

import (
	"crypto/x509"
	"fmt"
	"sync"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type pgCertFixture struct {
	*pgFixture
	ca     *pkiNode
	anchor *DeviceTrustAnchor
}

func newPgCertFixture(t *testing.T, mutate ...func(*ProvisioningProfileCreateRequest)) *pgCertFixture {
	t.Helper()
	f := newPgFixture(t)
	ca := newRootCA(t, "Maker Root")
	anchor, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "maker", CertificatePem: ca.pem()})
	require.NoError(t, err)
	tok := "maker"
	req := &ProvisioningProfileCreateRequest{Token: "pp-x", ProvisionKey: "key-pp-x", Strategy: string(ProvisionAllowNew),
		DeviceTypeToken: "dt-a", Enabled: true, TrustAnchorToken: &tok}
	for _, m := range mutate {
		m(req)
	}
	_, err = f.api.CreateProvisioningProfile(f.ctx, req)
	require.NoError(t, err)
	return &pgCertFixture{pgFixture: f, ca: ca, anchor: anchor}
}

func (f *pgCertFixture) registerLeaf(leaf *pkiNode) (*ProvisionResult, error) {
	profiles, err := f.api.X509ProfileAnchors(f.ctx)
	if err != nil {
		return nil, err
	}
	verified, err := x509trust.VerifyClientChain(f.ca.cert, []*x509.Certificate{leaf.cert}, time.Now())
	if err != nil {
		return nil, err
	}
	return f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: profiles[0].Profile, Certificate: &X509Registration{
		Leaf: leaf.cert, NotAfter: verified.NotAfter, AnchorId: f.anchor.ID, AnchorFingerprint: f.anchor.FingerprintSha256}})
}

// 🔑 TWO OR TWENTY CONNECTS WITH THE SAME NEW CERTIFICATE CREATE EXACTLY ONE DEVICE: one CREATED, the
// rest REPLAYED, one credential.
func TestCertificateRegistrationTwentyParallelConnectsOfOneCertificateCreateOneDevice(t *testing.T) {
	for _, n := range []int{2, 20} {
		t.Run(fmt.Sprintf("%d connects", n), func(t *testing.T) {
			f := newPgCertFixture(t)
			leaf := f.ca.leaf(t, "SN-1")
			results := runParallel(n, func(int) regResult {
				res, err := f.registerLeaf(leaf)
				return regResult{res, err}
			})
			noTransactionAborted(t, results)
			byOutcome, byCode, other := tallyRegistrations(results)
			assert.Empty(t, other)
			assert.Empty(t, byCode)
			assert.Equal(t, map[ProvisionOutcome]int{ProvisionCreated: 1, ProvisionReplayed: n - 1}, byOutcome)
			assert.EqualValues(t, 1, f.count(&Device{}))
			assert.EqualValues(t, 1, f.count(&DeviceCredential{}))
		})
	}
}

// The same name with different certificates: one winner, the rest already provisioned, one
// credential.
func TestCertificateRegistrationTwentyCertificatesForOneNameAdmitOne(t *testing.T) {
	f := newPgCertFixture(t)
	leaves := make([]*pkiNode, 20)
	for i := range leaves {
		leaves[i] = f.ca.leaf(t, "SN-1")
	}
	results := runParallel(20, func(i int) regResult {
		res, err := f.registerLeaf(leaves[i])
		return regResult{res, err}
	})
	noTransactionAborted(t, results)
	byOutcome, byCode, other := tallyRegistrations(results)
	assert.Empty(t, other)
	assert.Equal(t, map[ProvisionOutcome]int{ProvisionCreated: 1}, byOutcome)
	assert.Equal(t, map[string]int{ProvisioningCodeAlreadyProvisioned: 19}, byCode)
	assert.EqualValues(t, 1, f.count(&DeviceCredential{}), "more than one credential was issued to one device")
}

// maxDevices is exact under a burst of distinct certificates.
func TestCertificateRegistrationMaxDevicesIsExactUnderConcurrency(t *testing.T) {
	five := int32(5)
	f := newPgCertFixture(t, func(r *ProvisioningProfileCreateRequest) { r.MaxDevices = &five })
	leaves := make([]*pkiNode, 20)
	for i := range leaves {
		leaves[i] = f.ca.leaf(t, fmt.Sprintf("SN-%d", i))
	}
	results := runParallel(20, func(i int) regResult {
		res, err := f.registerLeaf(leaves[i])
		return regResult{res, err}
	})
	noTransactionAborted(t, results)
	byOutcome, byCode, other := tallyRegistrations(results)
	assert.Empty(t, other)
	assert.Equal(t, map[ProvisionOutcome]int{ProvisionCreated: 5}, byOutcome, "the cap was not exact")
	assert.Equal(t, map[string]int{ProvisioningCodeLimitExceeded: 15}, byCode)
	assert.EqualValues(t, 5, f.count(&Device{}))
	assert.EqualValues(t, 5, f.count(&DeviceCredential{}))
}

// 🔑 AN ANCHOR DISABLE QUEUES BEHIND A REGISTRATION IN FLIGHT AND THEN KICKS THE CREDENTIAL IT
// WROTE. Without the anchor held FOR SHARE to the commit, the disable would commit first, find no
// credential to kick, and leave a session on a withdrawn CA.
func TestCertificateRegistrationAnchorDisableWaitsForARegistrationInFlight(t *testing.T) {
	f := newPgCertFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	reached, release := f.holdRegistration("SN-1")

	regDone := make(chan regResult, 1)
	go func() { res, err := f.registerLeaf(leaf); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the registration to write its credential")

	disabled := make(chan struct{})
	var disableErr error
	go func() {
		_, disableErr = f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", &DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		close(disabled)
	}()
	waitForBlockedBackend(t, f.mgr.Database)
	notDone(t, disabled, "the anchor disable")

	release()
	awaitSignal(t, disabled, "the anchor disable")
	require.NoError(t, disableErr)
	reg := <-regDone
	require.NoError(t, reg.err)
	assert.Equal(t, ProvisionCreated, reg.res.Outcome)

	// The device the registration committed first is among those the disable kicked.
	require.Eventually(t, func() bool {
		f.kicker.mu.Lock()
		defer f.kicker.mu.Unlock()
		for _, c := range f.kicker.calls {
			for _, tok := range c.tokens {
				if tok == reg.res.DeviceToken {
					return true
				}
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "the disable did not kick the device the registration created")

	// Once the anchor is off, the next registration is refused (the gate under the lock).
	_, err := f.registerLeaf(f.ca.leaf(t, "SN-2"))
	require.ErrorIs(t, err, ErrProvisioningFailed)
}

// holdAfterDevice pauses the registration of externalId right after its DEVICE row is written
// and before its credential is: the profile and anchor have been read and checked, and the
// credential (whose foreign key would itself lock the anchor row) does not exist yet. That is the
// window only the explicit FOR SHARE on the anchor covers.
func (f *pgFixture) holdAfterDevice(externalId string) (reached <-chan struct{}, release func()) {
	f.t.Helper()
	reachedCh, releaseCh := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	name := fmt.Sprintf("test:hold-after-device-%s-%d", externalId, time.Now().UnixNano())
	require.NoError(f.t, f.mgr.Database.Callback().Create().After("gorm:create").Register(name, func(db *gorm.DB) {
		d, ok := db.Statement.Dest.(*Device)
		if !ok || db.Error != nil || d.TenantId != f.tenant || !d.ExternalId.Valid || d.ExternalId.String != externalId {
			return
		}
		once.Do(func() {
			close(reachedCh)
			<-releaseCh
		})
	}))
	rel := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	f.t.Cleanup(func() {
		rel()
		_ = f.mgr.Database.Callback().Create().Remove(name)
	})
	return reachedCh, rel
}

// 🔑 THE ANCHOR IS LOCKED FROM THE MOMENT IT IS CHECKED, NOT ONLY FROM THE CREDENTIAL INSERT. A
// disable that arrives between the check and the insert must wait; otherwise the credential is
// pinned to an anchor that has just been withdrawn and nothing kicks it.
func TestCertificateRegistrationAnchorDisableWaitsBetweenTheCheckAndTheInsert(t *testing.T) {
	f := newPgCertFixture(t)
	leaf := f.ca.leaf(t, "SN-1")
	reached, release := f.holdAfterDevice("SN-1")

	regDone := make(chan regResult, 1)
	go func() { res, err := f.registerLeaf(leaf); regDone <- regResult{res, err} }()
	awaitSignal(t, reached, "the registration to write its device")

	disabled := make(chan struct{})
	var disableErr error
	go func() {
		_, disableErr = f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", &DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		close(disabled)
	}()
	waitForBlockedBackend(t, f.mgr.Database)
	notDone(t, disabled, "the anchor disable")

	release()
	awaitSignal(t, disabled, "the anchor disable")
	require.NoError(t, disableErr)
	reg := <-regDone
	require.NoError(t, reg.err)
	require.Eventually(t, func() bool {
		f.kicker.mu.Lock()
		defer f.kicker.mu.Unlock()
		for _, c := range f.kicker.calls {
			for _, tok := range c.tokens {
				if tok == reg.res.DeviceToken {
					return true
				}
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "the disable did not kick the device the registration created")
}
