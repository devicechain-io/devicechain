// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
)

// deriveDeviceSecretKey derives the device secret key from the instance root key. A
// missing or malformed root key refuses the start: the chart requires one in every
// profile, and without it no MQTT password could be stored or checked.
func deriveDeviceSecretKey() (*credential.DeviceSecretKey, error) {
	root, err := Microservice.InstanceConfiguration.Infrastructure.Secrets.DecodedRootKey()
	if err != nil {
		return nil, fmt.Errorf("device credential secrets are stored as digests under a key derived from the instance root key: %w", err)
	}
	return credential.DeriveDeviceSecretKey(root)
}

// digestStoredCredentialSecrets digests any credential secret still stored as plaintext
// (schema.DigestPlaintextCredentialSecrets), then reports stored digests this key did not
// make. It runs on every start, after the migration chain.
//
// Foreign digests are logged at Error and do NOT refuse the start. They mean the database
// was written under another root key, and every such MQTT_BASIC credential is refused as
// misconfigured until it is re-set; refusing to start would take every other device and
// the API down with them, and user-management's secret store already refuses a wrong root
// key outright.
func digestStoredCredentialSecrets(ctx context.Context, rdbm *rdb.RdbManager, key *credential.DeviceSecretKey) error {
	converted, err := schema.DigestPlaintextCredentialSecrets(ctx, rdbm.Database, key.Digest)
	if err != nil {
		return fmt.Errorf("could not digest stored device credential secrets: %w", err)
	}
	if converted > 0 {
		log.Info().Int("credentials", converted).
			Msg("Replaced stored device credential secrets with their keyed digests.")
	}
	foreign, err := schema.CountForeignCredentialDigests(ctx, rdbm.Database, "v1$"+key.KeyId()+"$")
	if err != nil {
		return fmt.Errorf("could not check stored device credential digests: %w", err)
	}
	if foreign > 0 {
		log.Error().Int64("credentials", foreign).Str("keyId", key.KeyId()).
			Msg("Stored device credential secrets were digested under a different key than this " +
				"instance's root key derives, so those devices cannot authenticate. The database was " +
				"most likely restored next to the wrong root key; restore the matching root key, or " +
				"set those credentials' secrets again.")
	}
	return nil
}
