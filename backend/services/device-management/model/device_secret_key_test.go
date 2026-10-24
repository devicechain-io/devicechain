// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/credential"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// testSecretKey is the device secret key the model tests store and check secrets under.
var testSecretKey = func() *credential.DeviceSecretKey {
	k, err := credential.DeriveDeviceSecretKey([]byte("model-tests-root-key-32-bytes!!!"))
	if err != nil {
		panic(err)
	}
	return k
}()

// testEnrollmentKey is the provisioning secret key the model tests store and check profile
// secrets under. It is derived from the same root as testSecretKey and is a different key.
var testEnrollmentKey = func() *credential.DeviceSecretKey {
	k, err := credential.DeriveEnrollmentSecretKey([]byte("model-tests-root-key-32-bytes!!!"))
	if err != nil {
		panic(err)
	}
	return k
}()

// provisionSecretStr renders a stored provisioning secret digest as secretStr renders a
// credential's: the candidate it verifies under testEnrollmentKey, NullMarker for none, the
// digest itself when it verifies none.
func provisionSecretStr(d sql.NullString, candidates ...string) string {
	if !d.Valid {
		return putest.NullString(d)
	}
	for _, c := range candidates {
		if credential.VerifyDeviceSecret(testEnrollmentKey, partialUpdateTenant, d.String, c) == nil {
			return c
		}
	}
	return "unverified digest " + d.String
}

// testDigest is secret's stored form under testSecretKey, for a credential of the fixture
// tenant.
func testDigest(secret string) sql.NullString {
	d, err := testSecretKey.Digest(partialUpdateTenant, secret)
	if err != nil {
		panic(err)
	}
	return sql.NullString{String: d, Valid: true}
}

// secretStr renders a stored digest for a partial-update Read: the candidate secret it
// verifies, NullMarker for none, and the digest itself when it verifies none of them (so a
// mismatch shows up as a value no candidate equals). A digest is salted, so comparing the
// stored text to a fresh digest of the sent value would never match.
func secretStr(d sql.NullString, candidates ...string) string {
	if !d.Valid {
		return putest.NullString(d)
	}
	for _, c := range candidates {
		if verifies(d.String, c) {
			return c
		}
	}
	return "unverified digest " + d.String
}

// verifies reports whether stored is testSecretKey's digest of secret for a credential of
// the fixture tenant.
func verifies(stored, secret string) bool {
	return credential.VerifyDeviceSecret(testSecretKey, partialUpdateTenant, stored, secret) == nil
}

// testTokenDigest is an access token's stored credential_id under testSecretKey, for a
// credential of tenant: what a test that reaches into the table by credential_id matches.
func testTokenDigest(tenant, token string) string {
	d, err := testSecretKey.AccessTokenDigest(tenant, token)
	if err != nil {
		panic(err)
	}
	return d
}
