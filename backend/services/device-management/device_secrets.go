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

// deriveEnrollmentSecretKey derives the key provisioning profile secrets and device claim
// secrets are stored under, from the same root key as deriveDeviceSecretKey and separately
// from it.
func deriveEnrollmentSecretKey() (*credential.DeviceSecretKey, error) {
	root, err := Microservice.InstanceConfiguration.Infrastructure.Secrets.DecodedRootKey()
	if err != nil {
		return nil, fmt.Errorf("provisioning profile and device claim secrets are stored as digests under a key derived from the instance root key: %w", err)
	}
	return credential.DeriveEnrollmentSecretKey(root)
}

// deriveSecretTransit derives the key that opens the sealed credential secrets inbound
// events carry, from the same root key as the device secret key.
func deriveSecretTransit() (*credential.SecretTransit, error) {
	root, err := Microservice.InstanceConfiguration.Infrastructure.Secrets.DecodedRootKey()
	if err != nil {
		return nil, fmt.Errorf("presented device credential secrets travel sealed under a key derived from the instance root key: %w", err)
	}
	return credential.DeriveSecretTransit(root)
}

// digestStoredCredentialSecrets digests any credential secret, access token, provisioning
// profile secret or device claim secret still stored as plaintext
// (schema.DigestPlaintextCredentialSecrets, DigestPlaintextAccessTokens,
// DigestPlaintextProvisionSecrets, DigestPlaintextClaimSecrets), then reports stored digests this key did not
// make. It runs on every start, after the migration chain.
//
// Foreign digests are logged at Error and do NOT refuse the start. They mean the database
// was written under another root key, and every such MQTT_BASIC credential is refused as
// misconfigured until it is re-set; refusing to start would take every other device and
// the API down with them, and user-management's secret store already refuses a wrong root
// key outright.
func digestStoredCredentialSecrets(ctx context.Context, rdbm *rdb.RdbManager, key, enrollmentKey *credential.DeviceSecretKey) error {
	converted, err := schema.DigestPlaintextCredentialSecrets(ctx, rdbm.Database, key.Digest)
	if err != nil {
		return fmt.Errorf("could not digest stored device credential secrets: %w", err)
	}
	if converted > 0 {
		log.Info().Int("credentials", converted).
			Msg("Replaced stored device credential secrets with their keyed digests.")
	}
	tokens, err := schema.DigestPlaintextAccessTokens(ctx, rdbm.Database, credential.IsAccessTokenDigest, key.AccessTokenDigest)
	if err != nil {
		return fmt.Errorf("could not digest stored device access tokens: %w", err)
	}
	if tokens > 0 {
		log.Info().Int("credentials", tokens).
			Msg("Replaced stored device access tokens with their keyed digests.")
	}
	foreignTokens, err := schema.CountForeignAccessTokenDigests(ctx, rdbm.Database, key.AccessTokenDigestPrefix())
	if err != nil {
		return fmt.Errorf("could not check stored device access token digests: %w", err)
	}
	if foreignTokens > 0 {
		log.Error().Int64("credentials", foreignTokens).Str("keyId", key.KeyId()).
			Msg("Stored device access tokens were digested under a different key than this " +
				"instance's root key derives, so those devices cannot authenticate. The database was " +
				"most likely restored next to the wrong root key; restore the matching root key, or " +
				"set those credentials' access tokens again.")
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
	provisioned, err := schema.DigestPlaintextProvisionSecrets(ctx, rdbm.Database, enrollmentKey.Digest)
	if err != nil {
		return fmt.Errorf("could not digest stored provisioning profile secrets: %w", err)
	}
	if provisioned > 0 {
		log.Info().Int("profiles", provisioned).
			Msg("Replaced stored provisioning profile secrets with their keyed digests.")
	}
	foreignProvisioned, err := schema.CountForeignProvisionSecretDigests(ctx, rdbm.Database, "v1$"+enrollmentKey.KeyId()+"$")
	if err != nil {
		return fmt.Errorf("could not check stored provisioning profile secret digests: %w", err)
	}
	if foreignProvisioned > 0 {
		log.Error().Int64("profiles", foreignProvisioned).Str("keyId", enrollmentKey.KeyId()).
			Msg("Stored provisioning profile secrets were digested under a different key than this " +
				"instance's root key derives, so devices cannot register against those profiles. The " +
				"database was most likely restored next to the wrong root key; restore the matching " +
				"root key, or set those profiles' secrets again.")
	}
	claims, err := schema.DigestPlaintextClaimSecrets(ctx, rdbm.Database, enrollmentKey.Digest)
	if err != nil {
		return fmt.Errorf("could not digest stored device claim secrets: %w", err)
	}
	if claims > 0 {
		log.Info().Int("claims", claims).
			Msg("Replaced stored device claim secrets with their keyed digests.")
	}
	foreignClaims, err := schema.CountForeignClaimSecretDigests(ctx, rdbm.Database, "v1$"+enrollmentKey.KeyId()+"$")
	if err != nil {
		return fmt.Errorf("could not check stored device claim secret digests: %w", err)
	}
	if foreignClaims > 0 {
		log.Error().Int64("claims", foreignClaims).Str("keyId", enrollmentKey.KeyId()).
			Msg("Stored device claim secrets were digested under a different key than this " +
				"instance's root key derives, so those claims cannot be redeemed. The database was " +
				"most likely restored next to the wrong root key; restore the matching root key, or " +
				"open those claims again with a new secret.")
	}
	return nil
}
