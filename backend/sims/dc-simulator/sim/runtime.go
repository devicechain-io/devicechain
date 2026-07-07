// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"net/http"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/userclient"
)

// httpTimeout bounds a single provisioning/emit round-trip.
const httpTimeout = 15 * time.Second

// Runtime is the shared handle every Sim implementation's Bootstrap/Tick
// receives: the authenticated tenant session, the resolved endpoints, and the
// devices Bootstrap provisioned (populated once bootstrap succeeds, so Tick can
// iterate them without re-deriving anything).
type Runtime struct {
	Session    *userclient.TenantSession
	Endpoints  Endpoints
	InstanceId string
	Tenant     string
	HTTPClient *http.Client

	// Devices is the manifest's Expand()'d device set, filled in by
	// bootstrap.go's Provision once every device+credential exists. Tick reads
	// it; nothing else mutates it after Bootstrap returns.
	Devices []DeviceInstance
}

// NewRuntime builds a Runtime from a validated Handshake. No network call
// happens here — TenantSession authenticates lazily on first use.
func NewRuntime(hs *Handshake) (*Runtime, error) {
	if err := core.ValidateToken(hs.Tenant); err != nil {
		return nil, err
	}
	httpc := &http.Client{Timeout: httpTimeout}
	return &Runtime{
		Session:    userclient.NewTenantSession(httpc, hs.Endpoints.UserGraphQL, hs.SimEmail, hs.SimPassword, hs.Tenant),
		Endpoints:  hs.Endpoints,
		InstanceId: hs.InstanceId,
		Tenant:     hs.Tenant,
		HTTPClient: httpc,
	}, nil
}
