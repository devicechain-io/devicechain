// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import "testing"

// NewServedManager hands the external identity_test package a Manager built the way the
// session tests build one - real issuer and validator, refresh state in a real JetStream
// bucket, a seeded tenant "acme" - with one member, pat@example.com / pw-1. It lives here
// because the Manager's collaborators are unexported, and the graphql package cannot be
// imported from package identity itself (it imports identity).
func NewServedManager(t *testing.T) *Manager {
	t.Helper()
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	return e.m
}
