// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package limit holds the typed refusal for a request that names more than a bound
// allows (too many keys, too large an input, too many result buckets).
//
// It is a leaf package so the storage helpers that enforce a bound (core/rdb) and the
// GraphQL boundary that serves its code (core/graphql) can both depend on it without
// depending on each other.
package limit

import (
	"errors"
	"fmt"
)

// Code is the GraphQL extensions.code a refused request carries.
const Code = "LIMIT_EXCEEDED"

// Error refuses a request that exceeds a bound. It is never a truncation: the caller is
// told what was asked, what is allowed, and nothing is silently dropped.
type Error struct {
	// What names the bounded thing ("lookup keys", "metadata bytes", "buckets").
	What string
	// Got is the requested size and Max the allowed one.
	Got, Max int
}

// Exceeded returns the refusal for what: got is over max.
func Exceeded(what string, got, max int) *Error {
	return &Error{What: what, Got: got, Max: max}
}

func (e *Error) Error() string {
	return fmt.Sprintf("limit exceeded for %s: %d requested, at most %d allowed", e.What, e.Got, e.Max)
}

// Extensions gives the error its wire code. graphql-go reads it only from the error a
// resolver returned directly; the GraphQL boundary sets it for a refusal found deeper in
// a wrapped chain.
func (e *Error) Extensions() map[string]any { return map[string]any{"code": Code} }

// As reports whether err's chain holds a limit refusal.
func As(err error) (*Error, bool) {
	var le *Error
	if errors.As(err, &le) {
		return le, true
	}
	return nil, false
}
