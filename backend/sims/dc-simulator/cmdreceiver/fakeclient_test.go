// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmdreceiver

import (
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// fakeClient is a stand-in for the paho client so the DISCONNECT path is testable
// with no broker. Only the two methods Disconnect() drives carry behaviour; the rest
// of mqtt.Client is present because the interface requires it, and panics rather
// than returning a zero value so a test that wanders into an unimplemented method
// says so instead of quietly measuring nothing.
type fakeClient struct {
	mu sync.Mutex
	// staysConnected models a client whose teardown never completes, which is what
	// the bounded wait exists for.
	staysConnected bool
	// connectResult is what Connect() returns; nil means "this test does not attach".
	connectResult mqtt.Token
	connected     bool
	disconnects   int
	lastQuiesce   uint
}

func newFakeClient() *fakeClient { return &fakeClient{connected: true} }

func (f *fakeClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeClient) Disconnect(quiesce uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnects++
	f.lastQuiesce = quiesce
	if !f.staysConnected {
		f.connected = false
	}
}

func (f *fakeClient) calls() (int, uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disconnects, f.lastQuiesce
}

func (f *fakeClient) IsConnectionOpen() bool { return f.IsConnected() }

// Connect returns connectResult, so a test can drive the ATTACH path — the one that
// takes the device claim before dialling and must give it back if the dial fails.
// Nil keeps the original behaviour of refusing to be used by accident.
func (f *fakeClient) Connect() mqtt.Token {
	if f.connectResult == nil {
		panic("fakeClient.Connect not implemented (set connectResult)")
	}
	return f.connectResult
}

// fakeToken is a paho Token whose outcome is fixed. `done` closed ⇒ the wait
// succeeds; err is what Error() reports.
type fakeToken struct {
	err       error
	completes bool
}

func (t *fakeToken) Wait() bool                     { return t.completes }
func (t *fakeToken) WaitTimeout(time.Duration) bool { return t.completes }
func (t *fakeToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	if t.completes {
		close(ch)
	}
	return ch
}
func (t *fakeToken) Error() error { return t.err }

var _ mqtt.Token = (*fakeToken)(nil)

func (f *fakeClient) Publish(string, byte, bool, interface{}) mqtt.Token {
	panic("fakeClient.Publish not implemented")
}
func (f *fakeClient) Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token {
	panic("fakeClient.Subscribe not implemented")
}
func (f *fakeClient) SubscribeMultiple(map[string]byte, mqtt.MessageHandler) mqtt.Token {
	panic("fakeClient.SubscribeMultiple not implemented")
}
func (f *fakeClient) Unsubscribe(...string) mqtt.Token {
	panic("fakeClient.Unsubscribe not implemented")
}
func (f *fakeClient) AddRoute(string, mqtt.MessageHandler) {
	panic("fakeClient.AddRoute not implemented")
}
func (f *fakeClient) OptionsReader() mqtt.ClientOptionsReader {
	panic("fakeClient.OptionsReader not implemented")
}

var _ mqtt.Client = (*fakeClient)(nil)
