// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

const (
	// MaxUndecodedSampleHex bounds the stored sample: 512 bytes of frame, hex encoded.
	MaxUndecodedSampleHex = 1024
	// authFailureMarkerInterval is the least time between two writes of lastAuthFailureAt on
	// one integration. The marker is for an operator to read, and a sender spraying bad tokens
	// must not be able to make the journal grow in proportion.
	authFailureMarkerInterval = time.Minute
)

// RecordIntegrationUndecoded adds count uplinks to an integration's undecoded tally and keeps
// the latest sample of the frame (hex, at most MaxUndecodedSampleHex characters). The ingest
// path batches these and calls at most about once a minute per integration.
//
// It authenticates with the integration's own bearer token like resolveIntegrationDevice does,
// so a service token alone cannot write another sender's markers. A disabled integration is
// refused like a wrong token: nothing is written.
func (api *Api) RecordIntegrationUndecoded(ctx context.Context, integrationToken, presented string, count int64, sampleHex string) error {
	integration, err := api.AuthenticateIntegration(ctx, integrationToken, presented)
	if err != nil {
		return err
	}
	if count < 1 {
		return invalidIntegration("count must be at least 1")
	}
	if len(sampleHex) > MaxUndecodedSampleHex {
		sampleHex = sampleHex[:MaxUndecodedSampleHex]
	}
	if sampleHex != "" {
		if _, err := hex.DecodeString(sampleHex); err != nil {
			return invalidIntegration("sample must be hexadecimal")
		}
	}
	update := map[string]any{
		"undecoded_count":   gorm.Expr("undecoded_count + ?", count),
		"last_undecoded_at": time.Now().UTC(),
	}
	if sampleHex != "" {
		update["last_undecoded_sample"] = sql.NullString{String: sampleHex, Valid: true}
	}
	// The loaded row is handed to the statement so the journal names the integration.
	return api.RDB.DB(ctx).Model(integration).UpdateColumns(update).Error
}

// RecordIntegrationAuthFailure stamps lastAuthFailureAt on the integration with this token, at
// most once a minute, and does nothing for a token that names no integration. It reads no
// secret: the caller has just seen a bearer token fail against it.
func (api *Api) RecordIntegrationAuthFailure(ctx context.Context, integrationToken string) error {
	found, err := api.IntegrationsByToken(ctx, []string{integrationToken})
	if err != nil || len(found) == 0 {
		return err
	}
	row := found[0]
	now := time.Now().UTC()
	if row.LastAuthFailureAt.Valid && now.Sub(row.LastAuthFailureAt.Time) < authFailureMarkerInterval {
		return nil
	}
	return api.RDB.DB(ctx).Model(row).UpdateColumn("last_auth_failure_at", sql.NullTime{Time: now, Valid: true}).Error
}
