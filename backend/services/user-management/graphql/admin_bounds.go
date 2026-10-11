// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"github.com/devicechain-io/dc-microservice/limit"
)

const (
	// maxPasswordBytes is the longest password accepted: bcrypt only reads the first 72
	// bytes, so a longer one cannot be honoured. Refused with a typed message instead of
	// surfacing the hasher's own error.
	maxPasswordBytes = 72
	// maxAdminListEntries bounds the role, authority and role-token lists an admin
	// request may carry.
	maxAdminListEntries = 100
)

// checkPassword refuses a password the hasher cannot take whole.
func checkPassword(password string) error {
	if len(password) > maxPasswordBytes {
		return limit.Exceeded("password bytes", len(password), maxPasswordBytes)
	}
	return nil
}

// checkList refuses a list over maxAdminListEntries.
func checkList(what string, list []string) error {
	if len(list) > maxAdminListEntries {
		return limit.Exceeded(what, len(list), maxAdminListEntries)
	}
	return nil
}
