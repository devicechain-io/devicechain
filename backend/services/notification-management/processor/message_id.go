// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type messageIDKey struct{}

// newMessageID returns a fresh webhook message id ("msg_" plus 128 random bits).
func newMessageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on a supported platform; an unusable id would make a signed
		// delivery refuse (an empty webhook-id), which is the safe direction.
		return ""
	}
	return "msg_" + hex.EncodeToString(b)
}

// withMessageID carries the webhook-id of the delivery in progress to the adapter. It travels on
// the context because the adapter interface is shared with SMTP, which has no use for it, and one
// id has to span every retry of a single delivery.
func withMessageID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, messageIDKey{}, id)
}

// messageIDFrom reads the id withMessageID set, or "" when none was.
func messageIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(messageIDKey{}).(string)
	return id
}
