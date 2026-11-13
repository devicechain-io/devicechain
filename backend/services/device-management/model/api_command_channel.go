// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"
)

// CommandChannelAccess is what the HTTP command endpoints learn about a device that presented an
// access token: who it is and how its type delivers commands.
type CommandChannelAccess struct {
	DeviceToken     string
	DeviceTypeToken string
	// CommandChannel is the type's command channel, empty for the default.
	CommandChannel string
}

// AuthenticateCommandChannel proves an access token on behalf of the HTTP command endpoints
// (the pull and the acknowledgement a device sends back) and names the device it belongs to. It
// returns nil with a nil error for a device's wrong answer (an unknown, revoked or expired token,
// or a device an operator has disabled), which the caller turns into one 401 for all of them, and
// an error for anything that is the platform's own failure.
//
// 🔴 IT READS THE DATABASE EVERY TIME, THROUGH AuthenticateDeviceConnect AND NEVER THE CACHED
// CHECK. A poll that is answered with a command is an actuation, so a revoked token must stop
// receiving commands on the next request on every replica, not when a cached copy ages out.
func (api *Api) AuthenticateCommandChannel(ctx context.Context, credentialId string,
	now time.Time) (*CommandChannelAccess, error) {
	device, err := api.AuthenticateDeviceConnect(ctx, &PresentedCredential{
		CredentialType: string(CredentialAccessToken), CredentialId: credentialId,
	}, now)
	if err != nil {
		if IsCredentialRefusal(err) {
			return nil, nil
		}
		return nil, err
	}
	if device.IsDisabled() {
		return nil, nil
	}
	types, err := api.DeviceTypesById(ctx, []uint{device.DeviceTypeId})
	if err != nil {
		return nil, err
	}
	if len(types) == 0 {
		return nil, errors.New("the authenticated device's type could not be read")
	}
	return &CommandChannelAccess{
		DeviceToken:     device.Token,
		DeviceTypeToken: types[0].Token,
		CommandChannel:  types[0].CommandChannel.String,
	}, nil
}

// maxPullDeviceTokens bounds one commandPullDevices question, as the dispatch schedule's is.
const maxPullDeviceTokens = 2000

// ErrTooManyPullDevices is returned when one question names more devices than the limit.
var ErrTooManyPullDevices = errors.New("too many devices in one command channel question")

// PullDevices returns the subset of the named devices that exist, are enabled and whose type
// delivers commands over HTTP_PULL. A device that does not exist or is disabled is absent: the
// caller dispatches by the default rules for it, and a disabled device is gated elsewhere.
func (api *Api) PullDevices(ctx context.Context, deviceTokens []string) ([]string, error) {
	if len(deviceTokens) > maxPullDeviceTokens {
		return nil, ErrTooManyPullDevices
	}
	ordered := dedupeTokens(deviceTokens)
	out := make([]string, 0)
	typeIsPull := make(map[uint]bool)
	for start := 0; start < len(ordered); start += deviceLookupChunk {
		end := min(start+deviceLookupChunk, len(ordered))
		found, err := api.DevicesByToken(ctx, ordered[start:end])
		if err != nil {
			return nil, err
		}
		for _, d := range found {
			if d == nil || d.IsDisabled() {
				continue
			}
			pull, known := typeIsPull[d.DeviceTypeId]
			if !known {
				types, err := api.DeviceTypesById(ctx, []uint{d.DeviceTypeId})
				if err != nil {
					return nil, err
				}
				pull = len(types) > 0 && types[0].CommandChannel.String == CommandChannelHTTPPull
				typeIsPull[d.DeviceTypeId] = pull
			}
			if pull {
				out = append(out, d.Token)
			}
		}
	}
	return out, nil
}
