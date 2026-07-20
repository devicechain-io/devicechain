// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/secrets"
)

// fakeSecretStore is a minimal in-memory SecretStore for tests: only Resolve is exercised. A handle
// present in values resolves to its bytes; an absent handle returns ErrSecretNotFound; failRef (if
// matched by name) returns a transient error so fail-closed behavior can be tested.
type fakeSecretStore struct {
	values  map[string][]byte
	failRef string
	err     error
	calls   int
	mu      sync.Mutex
}

func (s *fakeSecretStore) Resolve(_ context.Context, ref secrets.SecretRef) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failRef != "" && ref.Name == s.failRef {
		if s.err != nil {
			return nil, s.err
		}
		return nil, secrets.ErrSecretNotFound
	}
	if v, ok := s.values[ref.Name]; ok {
		return v, nil
	}
	return nil, secrets.ErrSecretNotFound
}

func (s *fakeSecretStore) resolveCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeSecretStore) Put(context.Context, secrets.SecretRef, []byte) error    { return nil }
func (s *fakeSecretStore) Rotate(context.Context, secrets.SecretRef, []byte) error { return nil }
func (s *fakeSecretStore) Delete(context.Context, secrets.SecretRef) error         { return nil }
func (s *fakeSecretStore) Exists(context.Context, secrets.SecretRef) (bool, error) { return false, nil }

// fakeAck captures the disposition of one consumed message. There is no Nak: a
// transient failure is retried by leaving the message UNACKED (acked stays false),
// so acked==false means "left for AckWait-paced redelivery" (ADR-030).
type fakeAck struct {
	acked bool
}

func (a *fakeAck) Ack() error { a.acked = true; return nil }

// fakeReader yields a fixed queue of messages then blocks until the context is cancelled (mirroring a
// live durable reader that has drained its backlog and awaits new messages).
type fakeReader struct {
	msgs []messaging.Message
	i    int
	mu   sync.Mutex
}

func (r *fakeReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.mu.Lock()
	if r.i < len(r.msgs) {
		m := r.msgs[r.i]
		r.i++
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}

func (r *fakeReader) HandleResponse(error) {}

// fakeWriter captures messages written to the dead-letter subject, recording the tenant the writer
// would scope the subject to (taken from context, as the real writer does).
type fakeWriter struct {
	mu       sync.Mutex
	messages []messaging.Message
	tenants  []string
	fail     error
}

func (w *fakeWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail != nil {
		return w.fail
	}
	tenant, _ := core.TenantFromContext(ctx)
	for _, m := range msgs {
		w.messages = append(w.messages, m)
		w.tenants = append(w.tenants, tenant)
	}
	return nil
}

// WriteToDevice satisfies MessageWriter. This producer is not per-device, so it
// records exactly like an ordinary write.
func (w *fakeWriter) WriteToDevice(ctx context.Context, deviceToken string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *fakeWriter) HandleResponse(error) {}

func (w *fakeWriter) written() []messaging.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]messaging.Message(nil), w.messages...)
}
