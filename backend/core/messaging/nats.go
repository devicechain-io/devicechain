// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

const (
	// streamMaxAge bounds how long undelivered/retained messages live in a
	// JetStream stream. Mirrors a Kafka retention window; durable consumers
	// track their own position independently.
	streamMaxAge = 7 * 24 * time.Hour

	// fetchTimeout bounds a single pull-consumer Fetch so an idle reader can
	// periodically check for shutdown instead of blocking forever.
	fetchTimeout = 1 * time.Second
)

// NatsManager manages the lifecycle of NATS JetStream interactions for a
// microservice. It mirrors the former KafkaManager's lifecycle shape so the
// service mains change minimally.
type NatsManager struct {
	Microservice *core.Microservice

	oncreate  func(*NatsManager) error
	nc        *nats.Conn
	js        nats.JetStreamContext
	readers   []*natsReader
	writers   []*natsWriter
	lifecycle core.LifecycleManager
}

// NewNatsManager creates a new NATS manager. oncreate is invoked on Start to
// instantiate the service's readers/writers (mirrors KafkaManager).
func NewNatsManager(ms *core.Microservice, callbacks core.LifecycleCallbacks,
	oncreate func(*NatsManager) error) *NatsManager {
	nmgr := &NatsManager{
		Microservice: ms,
		oncreate:     oncreate,
		readers:      make([]*natsReader, 0),
		writers:      make([]*natsWriter, 0),
	}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "nats")
	nmgr.lifecycle = core.NewLifecycleManager(name, nmgr, callbacks)
	return nmgr
}

// NatsUrl returns the NATS connection url from instance configuration.
func (nmgr *NatsManager) NatsUrl() string {
	cfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	return fmt.Sprintf("nats://%s:%d", cfg.Hostname, cfg.Port)
}

// streamReplicas returns the configured JetStream replica count (defaulting to
// 1 when unset) so a single-node dev cluster and an HA cluster (ADR-018) share
// one code path.
func (nmgr *NatsManager) streamReplicas() int {
	r := int(nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas)
	if r < 1 {
		return 1
	}
	return r
}

// ensureStream creates the per-suffix stream if it does not already exist. The
// stream captures every tenant's subjects for the suffix via the wildcard
// subject, so a single stream backs both the scoped producers and the shared
// wildcard consumer.
func (nmgr *NatsManager) ensureStream(suffix string) (string, error) {
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	if _, err := nmgr.js.StreamInfo(name); err == nil {
		return name, nil
	} else if !errors.Is(err, nats.ErrStreamNotFound) {
		return "", err
	}
	_, err := nmgr.js.AddStream(&nats.StreamConfig{
		Name:      name,
		Subjects:  []string{WildcardSubject(nmgr.Microservice.InstanceId, suffix)},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Discard:   nats.DiscardOld,
		MaxAge:    streamMaxAge,
		Replicas:  nmgr.streamReplicas(),
	})
	if err != nil {
		return "", err
	}
	log.Info().Str("stream", name).Msg("Created JetStream stream")
	return name, nil
}

// ----------------
// Writer (producer)
// ----------------

// natsWriter publishes to a per-suffix subject, deriving the tenant-scoped
// subject from context at write time (fail-closed when no tenant is present).
type natsWriter struct {
	nmgr   *NatsManager
	suffix string
}

// NewWriter creates a producer for the given subject suffix. The stream backing
// the suffix is created if needed. The returned writer builds the fully-scoped
// subject ("{instance}.{tenant}.{suffix}") per message from the tenant in
// context.
func (nmgr *NatsManager) NewWriter(suffix string) (MessageWriter, error) {
	if _, err := nmgr.ensureStream(suffix); err != nil {
		return nil, err
	}
	w := &natsWriter{nmgr: nmgr, suffix: suffix}
	nmgr.writers = append(nmgr.writers, w)
	log.Info().Str("suffix", suffix).Msg("Added new NATS writer")
	return w, nil
}

// WriteMessages publishes each message to the writer's tenant-scoped subject.
// The tenant is taken from context and is the single source of the subject
// (fail-closed): a write with no tenant in context is rejected rather than
// published unscoped. All messages in one call share the caller's tenant, so
// the subject is derived once.
func (w *natsWriter) WriteMessages(ctx context.Context, msgs ...Message) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return core.ErrNoTenant
	}
	subject := ScopedSubject(w.nmgr.Microservice.InstanceId, tenant, w.suffix)
	for i := range msgs {
		if _, err := w.nmgr.js.Publish(subject, msgs[i].Value); err != nil {
			return err
		}
	}
	return nil
}

// HandleResponse logs the result of a write operation.
func (w *natsWriter) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Str("suffix", w.suffix).Msg("nats write operation failed")
	} else if log.Debug().Enabled() {
		log.Debug().Str("suffix", w.suffix).Msg("nats write operation successful")
	}
}

// ----------------
// Reader (consumer)
// ----------------

// natsReader is a durable pull-consumer over the cross-tenant wildcard subject
// for a suffix. The shared microservice consumes all tenants' messages here and
// derives the per-message tenant from the delivered subject.
type natsReader struct {
	suffix string
	sub    *nats.Subscription
}

// NewReader creates a durable pull consumer for the given subject suffix,
// subscribing to the cross-tenant wildcard so one shared pod drains every
// tenant. The durable name is scoped to the instance + functional area + suffix
// (not the tenant).
func (nmgr *NatsManager) NewReader(suffix string) (MessageReader, error) {
	stream, err := nmgr.ensureStream(suffix)
	if err != nil {
		return nil, err
	}
	subject := WildcardSubject(nmgr.Microservice.InstanceId, suffix)
	durable := DurableName(nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, suffix)
	sub, err := nmgr.js.PullSubscribe(subject, durable, nats.BindStream(stream))
	if err != nil {
		return nil, err
	}
	r := &natsReader{suffix: suffix, sub: sub}
	nmgr.readers = append(nmgr.readers, r)
	log.Info().Str("durable", durable).Str("subject", subject).Msg("Added new NATS reader")
	return r, nil
}

// ReadMessage blocks until a message is available, the context is cancelled, or
// the subscription closes. It acks on fetch (parity with the previous Kafka
// auto-commit-on-read; the in-memory channel pipeline already assumes messages
// can be lost on crash). True ack-after-persist is a hardening follow-up.
// On shutdown (ctx cancelled or subscription/connection closed) it returns
// io.EOF so the existing processor EOF handling applies.
func (r *natsReader) ReadMessage(ctx context.Context) (Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Message{}, io.EOF
		}
		msgs, err := r.sub.Fetch(1, nats.MaxWait(fetchTimeout))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				continue
			}
			if errors.Is(err, nats.ErrConnectionClosed) ||
				errors.Is(err, nats.ErrSubscriptionClosed) ||
				errors.Is(err, nats.ErrConnectionDraining) {
				return Message{}, io.EOF
			}
			return Message{}, err
		}
		if len(msgs) == 0 {
			continue
		}
		nm := msgs[0]
		if ackErr := nm.Ack(); ackErr != nil {
			log.Error().Err(ackErr).Str("subject", nm.Subject).Msg("nats ack failed")
		}
		return Message{Subject: nm.Subject, Value: nm.Data}, nil
	}
}

// HandleResponse logs the result of a read operation.
func (r *natsReader) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Str("suffix", r.suffix).Msg("nats read operation failed")
	} else if log.Debug().Enabled() {
		log.Debug().Str("suffix", r.suffix).Msg("nats read operation successful")
	}
}

// ----------------
// Lifecycle
// ----------------

// Initialize component.
func (nmgr *NatsManager) Initialize(ctx context.Context) error {
	return nmgr.lifecycle.Initialize(ctx)
}

// ExecuteInitialize connects to NATS and obtains a JetStream context.
func (nmgr *NatsManager) ExecuteInitialize(context.Context) error {
	url := nmgr.NatsUrl()
	nc, err := nats.Connect(url,
		nats.Name(nmgr.Microservice.FunctionalArea),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
	)
	if err != nil {
		return err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return err
	}
	nmgr.nc = nc
	nmgr.js = js
	log.Info().Msg(fmt.Sprintf("Verified connectivity to NATS at '%s'", url))
	return nil
}

// Start component.
func (nmgr *NatsManager) Start(ctx context.Context) error {
	return nmgr.lifecycle.Start(ctx)
}

// ExecuteStart instantiates the service's readers/writers via oncreate.
func (nmgr *NatsManager) ExecuteStart(context.Context) error {
	if err := nmgr.oncreate(nmgr); err != nil {
		return err
	}
	log.Info().Msg("NATS component creation completed successfully.")
	return nil
}

// Stop component.
func (nmgr *NatsManager) Stop(ctx context.Context) error {
	return nmgr.lifecycle.Stop(ctx)
}

// ExecuteStop unsubscribes readers and drains the connection.
func (nmgr *NatsManager) ExecuteStop(context.Context) error {
	log.Info().Msg("Shutting down NATS readers.")
	for _, r := range nmgr.readers {
		if err := r.sub.Unsubscribe(); err != nil {
			log.Error().Err(err).Str("suffix", r.suffix).Msg("Error unsubscribing NATS reader.")
		}
	}
	if nmgr.nc != nil {
		if err := nmgr.nc.Drain(); err != nil {
			log.Error().Err(err).Msg("Error draining NATS connection.")
		}
	}
	return nil
}

// Terminate component.
func (nmgr *NatsManager) Terminate(ctx context.Context) error {
	return nmgr.lifecycle.Terminate(ctx)
}

// ExecuteTerminate closes the NATS connection.
func (nmgr *NatsManager) ExecuteTerminate(context.Context) error {
	if nmgr.nc != nil && !nmgr.nc.IsClosed() {
		nmgr.nc.Close()
	}
	return nil
}
