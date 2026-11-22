// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package apiclient

import (
	"net/http"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// EndpointVersion binds an exact operation URL to a release read from trusted
// deployment metadata. Neither an HTTP response nor the local dcctl version is
// release evidence.
type EndpointVersion struct {
	Endpoint string
	Version  string
}

// NewTenantSession builds dcctl's schema-negotiating tenant client. Register
// every operation endpoint independently; the login URL grants no other URL a
// legacy fallback.
func NewTenantSession(httpc *http.Client, endpoint, email, password, tenant string, versions ...EndpointVersion) *userclient.TenantSession {
	s := userclient.NewTenantSession(httpc, endpoint, email, password, tenant).WithSchemaNegotiation(LegacySchema)
	for _, v := range versions {
		s.WithLegacyEndpointVersion(v.Endpoint, v.Version)
	}
	return s
}

// NewAdminSession builds dcctl's schema-negotiating identity client.
func NewAdminSession(httpc *http.Client, endpoint, email, password string, versions ...EndpointVersion) *userclient.AdminSession {
	a := userclient.NewAdminSession(httpc, endpoint, email, password).WithSchemaNegotiation(LegacySchema)
	for _, v := range versions {
		a.WithLegacyEndpointVersion(v.Endpoint, v.Version)
	}
	return a
}
