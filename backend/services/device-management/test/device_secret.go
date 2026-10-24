// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"github.com/devicechain-io/dc-microservice/credential"
)

// DeviceSecretKey is a fixed device secret key for tests outside the model package: what a
// test Api stores credential secrets under and a test Checker compares them with.
func DeviceSecretKey() *credential.DeviceSecretKey {
	k, err := credential.DeriveDeviceSecretKey([]byte("device-management-test-root-key!"))
	if err != nil {
		panic(err)
	}
	return k
}

// EnrollmentSecretKey is the fixed provisioning secret key for tests: what a test Api stores
// provisioning profile secrets under and a test Checker compares them with. It is derived
// from the same root as DeviceSecretKey, and is a different key.
func EnrollmentSecretKey() *credential.DeviceSecretKey {
	k, err := credential.DeriveEnrollmentSecretKey([]byte("device-management-test-root-key!"))
	if err != nil {
		panic(err)
	}
	return k
}

// SecretDigest is tenant's secret's stored form under DeviceSecretKey.
func SecretDigest(tenant, secret string) string {
	d, err := DeviceSecretKey().Digest(tenant, secret)
	if err != nil {
		panic(err)
	}
	return d
}
