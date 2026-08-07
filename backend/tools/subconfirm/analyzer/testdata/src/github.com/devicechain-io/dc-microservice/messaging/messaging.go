// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package messaging is a stand-in for core's messaging package, cut down to the
// three names this analyzer knows about. See the note in the nats stub beside it.
package messaging

import "github.com/nats-io/nats.go"

// ConfirmSubscribed blocks until the server has registered every subscription this
// connection has issued so far.
func ConfirmSubscribed(nc *nats.Conn) error { return nil }

// SubscribeSynced is Subscribe that does not return until the server can route to it.
func SubscribeSynced(nc *nats.Conn, subj string, cb nats.MsgHandler) (*nats.Subscription, error) {
	return nil, nil
}
