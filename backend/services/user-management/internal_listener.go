// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/http"

	"github.com/devicechain-io/dc-microservice/core"
)

// InternalAuthMux holds the endpoints that exist only for in-cluster callers: the
// service-token mint and the platform JWKS. It is a SEPARATE mux from Microservice.Mux() and
// is served by a separate listener (InternalAuthServer) on a separate port.
//
// 🔴 THIS IS THE DISTINCTION, NOT A GUARD AROUND ONE. The mint used to share the public mux
// and stay private only because an ingress rule 404'd /api/<area>/auth/*; any controller
// without that rule exposed a shared-secret-to-token exchange to the internet. Registering
// the mint on a mux the public listener never serves makes the public port answer 404 by
// construction, with no ingress in front, so no edge configuration is a security control.
//
// The OAuth metadata's jwks_uri points at the public /oauth/jwks mirror (served on the
// public mux when an issuer is configured), which is what an external verifier uses.
var (
	InternalAuthMux    *http.ServeMux
	InternalAuthServer *core.HttpServer
)

// internalAuthMux returns the internal mux, creating it on first use.
func internalAuthMux() *http.ServeMux {
	if InternalAuthMux == nil {
		InternalAuthMux = http.NewServeMux()
	}
	return InternalAuthMux
}

// startInternalAuthListener binds the internal listener on the instance-configured port. A
// zero port is refused rather than defaulted here: config.ApplyDefaults has already filled
// absence, so zero means a config that bypassed it, and binding port 0 would come up on an
// arbitrary port no peer is told about.
func startInternalAuthListener() error {
	port := Microservice.InstanceConfiguration.Infrastructure.UserManagement.InternalPort
	if port == 0 {
		return fmt.Errorf("infrastructure.userManagement.internalPort is not set; the internal auth listener has no port to bind")
	}
	if port == core.HttpPort {
		return fmt.Errorf("infrastructure.userManagement.internalPort (%d) must differ from the public port; the internal endpoints would be served on it", port)
	}
	InternalAuthServer = core.NewHttpServerForHandler(int32(port), internalAuthMux())
	return InternalAuthServer.Start()
}
