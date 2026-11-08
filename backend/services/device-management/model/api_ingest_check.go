// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
)

// IngestCredentialAuthenticator is the one authentication primitive CheckIngestCredential
// needs: the same call the event resolver makes for a credential in an event's body, so the
// sync answer and the async one cannot be given by different checks.
type IngestCredentialAuthenticator interface {
	AuthenticateDevice(ctx context.Context, presented *PresentedCredential, now time.Time) (*Device, error)
}

// CheckIngestCredential answers, for an HTTP ingest door that wants to refuse before it
// accepts, the question the event resolver will later ask of the same event: would the
// credential the body carries authenticate under the configured policy? It reports accepted
// false ONLY for an authentication failure (a credential that does not resolve, is expired,
// carries the wrong secret, belongs to another device than the one named, or is absent when the
// policy requires one). Anything else (the database, a stored credential that can never
// authenticate) is an error: it says nothing about the sender, and the caller must not read it
// as a refusal.
//
// It mirrors EventResolver.resolveDevice's credential half exactly, including that the
// self-asserted device token alone authenticates nothing and is only checked against the
// device a credential authenticated. The device-not-registered case is NOT decided here: with
// no credential in play it is the resolver's to record as an unknown device.
//
// 🔴 ITS VERDICT REACHES THE SENDER, which AuthenticateDevice's never did, so the caller must
// put a per-credential failure backoff in front of it. This function has none and says so.
func CheckIngestCredential(ctx context.Context, authn IngestCredentialAuthenticator, mode string,
	deviceToken string, presented *PresentedCredential, now time.Time) (accepted bool, err error) {
	if mode == config.AuthModeDisabled {
		return true, nil
	}
	if presented != nil && presented.CredentialId == "" {
		presented = nil
	}
	if presented == nil {
		return mode != config.AuthModeRequired, nil
	}
	device, err := authn.AuthenticateDevice(ctx, presented, now)
	switch {
	case err == nil:
	case errors.Is(err, ErrCredentialNotPresented), errors.Is(err, ErrCredentialTypeInvalid),
		errors.Is(err, ErrCredentialNotResolved), errors.Is(err, ErrCredentialExpired),
		errors.Is(err, ErrCredentialSecretMismatch), errors.Is(err, ErrCredentialRevoked):
		return false, nil
	default:
		return false, err
	}
	if deviceToken != "" && deviceToken != device.Token {
		return false, nil
	}
	return true, nil
}
