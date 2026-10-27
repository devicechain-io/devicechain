// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import "github.com/devicechain-io/dc-microservice/messaging"

// ReaderOptions are the options the device-commands consumer is built with.
//
//   - ReaderWithDeliverNew: a durable created without it replays the stream's retained history and
//     publishes already-expired commands to nodes as live actuations.
//   - ReaderWithTermGate(held): it hands out nothing unless this replica is serving a leadership term,
//     including a message already sitting in its fetch buffer. The dispatcher only reads while its
//     term is live, so this is a second fence, not the first.
//   - ReaderWithReleaseOnPark: what it has buffered is given back (Nak'd once) when the gate closes,
//     so a batch fetched across a handover is redelivered to the new leader at once instead of after
//     the ack wait. The dispatcher holds no ordered state a dropped buffer could reorder.
func ReaderOptions(held func() bool) []messaging.ReaderOption {
	return []messaging.ReaderOption{
		messaging.ReaderWithDeliverNew(),
		messaging.ReaderWithTermGate(held),
		messaging.ReaderWithReleaseOnPark(),
	}
}
