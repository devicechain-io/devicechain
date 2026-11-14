// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorwire

// ConnectorSecretPrefix separates connector credentials from authored HTTP credentials.
const ConnectorSecretPrefix = "connector/"

// HTTPCredentialPrefix is the secret-store namespace for httpCall Bearer credentials.
const HTTPCredentialPrefix = "http-credential/"

// HTTPCredentialSecretName maps an authored handle to its HTTP credential secret name.
func HTTPCredentialSecretName(handle string) string { return HTTPCredentialPrefix + handle }
