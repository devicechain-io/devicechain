// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

// IntegrationSealKind is the credential-type string an integration's bearer token is sealed
// and opened under (SecretTransit.SealToken / OpenToken), so a sealed token is bound to the
// tenant AND to the one integration it authenticates.
//
// It is the producer's and the resolver's shared definition, written once for the reason the
// seal's own binding exists: a token sealed for one integration must open for no other, and a
// bearer token sealed as a device's ACCESS_TOKEN credential must not open as an integration's.
// The kind is not a member of the device credential vocabulary and no device credential can
// be created with it, so the two cannot be mistaken for each other.
func IntegrationSealKind(integrationToken string) string {
	return "INTEGRATION:" + integrationToken
}
