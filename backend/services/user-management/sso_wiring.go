// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/identity"
)

// newIdentityManager builds the identity Manager AND gives it the HTTP client every
// external-identity-provider request goes through.
//
// It is a function of its own so the test that proves a private discovery URL is refused
// builds the Manager exactly as the service does, rather than a copy of it. That is the
// point: the egress guard lives in the client's transport, so the property "the service
// cannot be pointed at an internal address" holds only if the client is installed on the
// Manager main actually runs — and nothing else exercises main.go.
//
// An infrastructure configuration with a malformed egress allowance fails here, at startup,
// rather than yielding a guard that silently drops it.
func newIdentityManager(ms *core.Microservice, db *rdb.RdbManager, lock *messaging.DistributedLock,
	store secrets.SecretStore, accessTTL, refreshTTL time.Duration, issuerUrl string,
	bootstrap identity.BootstrapConfig, credentials *credential.Checker,
	infra mscfg.InfrastructureConfiguration) (*identity.Manager, error) {

	m := identity.NewManager(ms, db, lock, store, accessTTL, refreshTTL, issuerUrl, bootstrap, credentials)
	client, err := identity.NewSsoHTTPClient(infra)
	if err != nil {
		return nil, err
	}
	m.SetSsoHTTPClient(client)
	return m, nil
}
