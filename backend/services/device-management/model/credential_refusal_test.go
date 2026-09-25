// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"
)

// IsCredentialRefusal decides which authentication failures an operator sees at the
// default log level. A refusal is the device's wrong answer and stays quiet; everything
// else — a store failure, a stored credential that can never authenticate — must not.
//
// The wrapped rows are what tell errors.Is from ==: AuthenticateDevice's callers wrap.
func TestIsCredentialRefusal(t *testing.T) {
	refusals := []error{
		ErrCredentialNotPresented,
		ErrCredentialTypeInvalid,
		ErrCredentialNotResolved,
		ErrCredentialExpired,
		ErrCredentialSecretMismatch,
	}
	for _, err := range refusals {
		if !IsCredentialRefusal(err) {
			t.Errorf("%q is a device's wrong answer but was not classified as a refusal", err)
		}
		wrapped := fmt.Errorf("authenticating: %w", err)
		if !IsCredentialRefusal(wrapped) {
			t.Errorf("wrapped %q was not classified as a refusal: the check is not errors.Is", err)
		}
	}

	notRefusals := []error{
		// A defect in stored data, not a device's answer: an operator must see it.
		ErrCredentialMisconfigured,
		fmt.Errorf("authenticating: %w", ErrCredentialMisconfigured),
		errors.New("db down"),
		context.DeadlineExceeded,
		fmt.Errorf("looking up credential: %w", gorm.ErrInvalidDB),
	}
	for _, err := range notRefusals {
		if IsCredentialRefusal(err) {
			t.Errorf("%q was classified as a device's refusal, so it is logged at Debug and no "+
				"operator ever sees it", err)
		}
	}
}
