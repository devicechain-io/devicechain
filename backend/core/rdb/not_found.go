// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import "gorm.io/gorm"

// notFound is a service's own "that record does not exist" sentinel.
type notFound struct{ msg string }

func (e *notFound) Error() string { return e.msg }

// Is makes the sentinel a miss in the store's sense, so errors.Is(err, gorm.ErrRecordNotFound)
// holds for it exactly as for the store's own sentinel. Matching the sentinel ITSELF is not
// affected: errors.Is and == compare the pointer first, so each call to NewNotFound is a
// distinct value.
func (e *notFound) Is(target error) bool { return target == gorm.ErrRecordNotFound }

// NewNotFound returns a sentinel for a record the request named that does not exist,
// worded msg (the message is served as written).
//
// 🔴 WHY A SERVICE'S OWN SENTINEL MUST BE BUILT THIS WAY. The GraphQL boundary answers a
// resolver error with extensions.code NOT_FOUND when its chain holds gorm.ErrRecordNotFound
// (core/graphql). A service that translates the store's miss into a plain errors.New sentinel
// — to give callers something of its own to match, or a better message — drops that chain, and
// the miss reaches the client with no code, so the same failure carried a code or did not
// depending on which service answered. A sentinel made here keeps its own identity and wording
// AND stays a miss, so neither the callers matching it nor the code is lost.
//
// Use it for the sentinel of a MISS only. A refusal that is not "that record is not there"
// (a taken value, an invalid one) has its own classes in core/integrity.
func NewNotFound(msg string) error { return &notFound{msg: msg} }
