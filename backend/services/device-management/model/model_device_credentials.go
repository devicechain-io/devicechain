// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-device-management/model/identity"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// DeviceCredential is a device's credential row (identity.DeviceCredential, where its columns
// and their meaning are documented) together with the Device it belongs to.
//
// It is the one credential shape that is NOT an alias, because the association is the
// registry's: identity imports nothing of this package, so the row moved there and the Device
// field stayed here. The embedding is anonymous, which GORM flattens, so the table name, the
// columns, their order and the belongs-to on DeviceId are exactly what they were when this was
// one struct, and every column reads as a field of this type (cred.CredentialId, cred.ID).
// Only a composite literal names the row: DeviceCredential{DeviceCredential:
// identity.DeviceCredential{...}}.
type DeviceCredential struct {
	identity.DeviceCredential

	Device *Device
}

// Results for device credential search.
type DeviceCredentialSearchResults struct {
	Results    []DeviceCredential
	Pagination rdb.SearchResultsPagination
}
