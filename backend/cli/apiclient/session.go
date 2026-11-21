// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package apiclient

import (
	"net/http"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// NewTenantSession builds dcctl's schema-negotiating tenant client.
func NewTenantSession(httpc *http.Client, endpoint, email, password, tenant string) *userclient.TenantSession {
	return userclient.NewTenantSession(httpc, endpoint, email, password, tenant).WithSchemaNegotiation(LegacySchema)
}

// NewAdminSession builds dcctl's schema-negotiating identity client.
func NewAdminSession(httpc *http.Client, endpoint, email, password string) *userclient.AdminSession {
	return userclient.NewAdminSession(httpc, endpoint, email, password).WithSchemaNegotiation(LegacySchema)
}
