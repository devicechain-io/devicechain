// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/devicechain-io/dc-microservice/core"
)

// SessionKicker ends the live broker sessions of devices whose authority has been withdrawn.
//
// 🔑 WHY THIS EXISTS: on the platform MQTT broker a device is authenticated once, at CONNECT,
// and its events carry no credential of their own (event-sources refuses one). So a credential
// disabled, rotated or deleted, or a device disabled or deleted, would otherwise leave an
// already-open session publishing until the JWT the auth callout minted expires. Ending the
// session makes the device reconnect, and the callout then re-runs against the credential it
// presents NOW, which the database (already committed) refuses if it was revoked.
//
// An implementation returns nil ONLY when it observed the sessions ended (or none open). It is
// nil on an Api whose instance runs no MQTT auth callout, where there are no sessions of ours.
type SessionKicker interface {
	KickDeviceSessions(ctx context.Context, tenant string, deviceTokens []string) error
}

// ErrSessionsNotEnded is returned by a write that committed but could not confirm the affected
// devices' live sessions ended. The CHANGE IS SAVED and new connects are refused; what is not
// known is whether a session that was already open is still publishing. The caller is told so
// and repeats the operation, which is idempotent and asks again.
var ErrSessionsNotEnded = errors.New("the change was saved, but the device's live broker sessions " +
	"could not be confirmed ended, so a session that was already open may still be publishing; " +
	"repeat the operation to try again")

// endDeviceSessions asks the wired SessionKicker to end the sessions of the given devices of
// tenant, after the write that withdrew their authority has committed. No kicker wired is a
// no-op; no tokens is a no-op; a failure is ErrSessionsNotEnded wrapping the cause.
func (api *Api) endDeviceSessions(ctx context.Context, tenant string, deviceTokens ...string) error {
	if api.SessionKicker == nil {
		return nil
	}
	tokens := make([]string, 0, len(deviceTokens))
	for _, t := range deviceTokens {
		if t != "" && !slices.Contains(tokens, t) {
			tokens = append(tokens, t)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	if tenant == "" {
		// A kick names a tenant; without one nothing can be found. Loud, never a quiet success.
		return fmt.Errorf("%w: the change named no tenant", ErrSessionsNotEnded)
	}
	if err := api.SessionKicker.KickDeviceSessions(ctx, tenant, tokens); err != nil {
		return fmt.Errorf("%w (%v)", ErrSessionsNotEnded, err)
	}
	return nil
}

// deviceTokensOf reads the tokens of the given devices of tenant, soft-deleted ones included:
// a credential deleted with its device still has sessions to end.
func (api *Api) deviceTokensOf(ctx context.Context, tenant string, deviceIds []uint) ([]string, error) {
	if len(deviceIds) == 0 {
		return nil, nil
	}
	var tokens []string
	err := api.RDB.DB(core.WithTenant(ctx, tenant)).Unscoped().Model(&Device{}).
		Where("id IN ?", deviceIds).Pluck("token", &tokens).Error
	return tokens, err
}
