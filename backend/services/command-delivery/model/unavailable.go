// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// UnavailableCode is the GraphQL `extensions.code` of a request the service could not DECIDE:
// the enqueue gate, the maintenance-window schedule or a group's membership could not be read.
// It is the same code the sign-in path answers when it cannot check a credential, so a client
// that already branches on it for one needs nothing new for the other.
const UnavailableCode = "UNAVAILABLE"

// UnavailableError is a failure to answer, as opposed to an answer. Nothing was created and
// nothing was decided about the request, so a caller may send it again unchanged; that is what
// separates it from every rejection code except HELD_CEILING_EXCEEDED, which is also temporary
// but IS a verdict (it comes back as a `rejection`, never as an error).
//
// 🔴 IT IS DELIBERATELY AN ERROR AND NOT A REJECTION. A rejection asserts something about the
// caller's command that nobody checked, and the detail behind an availability failure names
// in-cluster topology, so the message stays the same sanitized sentence it always was and only
// the machine-readable code is new. graphql-go copies a resolver error's Extensions() only from
// the error the resolver returns directly, so a caller of this type must not wrap it.
type UnavailableError struct{ msg string }

func errUnavailable(msg string) error { return &UnavailableError{msg: msg} }

// unavailableOr is the error for a gate that failed with err: UNAVAILABLE when err means the
// peer could not be reached or could not serve right now (see svcclient.IsUnavailable), and the
// same sentence, uncoded, for any other failure. A peer that ANSWERED with a refusal, a response
// too large to read or a body that would not decode is not an outage, and a client told to retry
// one would retry for ever.
func unavailableOr(err error, msg string) error {
	if svcclient.IsUnavailable(err) {
		return errUnavailable(msg)
	}
	return errors.New(msg)
}

func (e *UnavailableError) Error() string { return e.msg }

// Extensions makes the GraphQL error machine-readable.
func (e *UnavailableError) Extensions() map[string]any {
	return map[string]any{"code": UnavailableCode}
}
