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

// SecretDigest is secret's stored form under DeviceSecretKey.
func SecretDigest(secret string) string {
	d, err := DeviceSecretKey().Digest(secret)
	if err != nil {
		panic(err)
	}
	return d
}
