// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package nats is a stand-in for github.com/nats-io/nats.go, cut down to the API
// surface this analyzer reads. analysistest loads its packages in GOPATH mode, so
// the real library is not reachable from here.
//
// 🔴 What this file has to keep faithful is narrow and exact: the import PATH, the
// TYPE names, and the METHOD names. Those three are the whole of what the analyzer
// matches on, so a stub that gets them right is not a simplification of the subject
// under test — it IS the subject. What it must never become is a stub that makes the
// analyzer's job easier than the real library does, which is why JetStreamContext is
// here at all: the real discrimination is "same method name, different receiver",
// and a stub with only Conn in it could not measure that.
//
// The counterweight to any drift between this and the real library is that the
// analyzer also runs over the real tree, against the real nats.go, in CI — see
// hack/check-subscribe-confirmed.sh.
package nats

// Msg is a message delivered to a subscription.
type Msg struct {
	Subject string
	Data    []byte
}

// MsgHandler is the callback form of delivery.
type MsgHandler func(*Msg)

// Subscription is a live interest registration.
type Subscription struct{}

// Unsubscribe removes the interest.
func (s *Subscription) Unsubscribe() error { return nil }

// Conn is the client connection. Every Subscribe below is asynchronous.
type Conn struct{}

func (c *Conn) Subscribe(subj string, cb MsgHandler) (*Subscription, error) { return nil, nil }

func (c *Conn) SubscribeSync(subj string) (*Subscription, error) { return nil, nil }

func (c *Conn) QueueSubscribe(subj, queue string, cb MsgHandler) (*Subscription, error) {
	return nil, nil
}

func (c *Conn) QueueSubscribeSync(subj, queue string) (*Subscription, error) { return nil, nil }

func (c *Conn) QueueSubscribeSyncWithChan(subj, queue string, ch chan *Msg) (*Subscription, error) {
	return nil, nil
}

func (c *Conn) ChanSubscribe(subj string, ch chan *Msg) (*Subscription, error) { return nil, nil }

func (c *Conn) ChanQueueSubscribe(subj, queue string, ch chan *Msg) (*Subscription, error) {
	return nil, nil
}

func (c *Conn) Publish(subj string, data []byte) error { return nil }

func (c *Conn) FlushTimeout(d int64) error { return nil }

// JetStreamContext creates its consumer through a request to the JetStream API, so
// its Subscribe is confirmed by construction. It is spelled exactly like the broken
// one, which is the reason the analyzer cannot be a grep.
type JetStreamContext interface {
	Subscribe(subj string, cb MsgHandler) (*Subscription, error)
	QueueSubscribe(subj, queue string, cb MsgHandler) (*Subscription, error)
}
