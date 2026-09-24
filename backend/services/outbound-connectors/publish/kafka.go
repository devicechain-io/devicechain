// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// defaultKafkaClientID is the client id a connector with none authored presents.
const defaultKafkaClientID = "devicechain"

// maxKafkaResponseBytes bounds one broker response. franz-go checks a response's size
// prefix against it BEFORE allocating, where its default would allocate up to 100 MiB on a
// broker's word. A producer reads metadata and produce acknowledgements, far below it.
const maxKafkaResponseBytes = 1 << 20

// sendKafka produces one record and closes the client.
//
// Each send builds its own client, so each pays one metadata round trip. A broker that is
// merely down fails to dial until the send's deadline and the error is retryable; only an
// egress refusal (a seed, or a broker the cluster advertised) is blocked.
func (s *Sender) sendKafka(ctx context.Context, log *dialLog, t connectorspec.KafkaTarget, payload []byte, idempotencyKey string) error {
	if t.SASL != nil && kafkaMechanism(*t.SASL) == nil {
		// Reachable only past connectorspec.Build. Never fall through to an
		// unauthenticated connection.
		return fmt.Errorf("%w: kafka sasl mechanism %q is not supported", ErrPublishConfig, t.SASL.Mechanism)
	}
	cl, err := kgo.NewClient(kafkaOpts(t, s.dial(log))...)
	if err != nil {
		return fmt.Errorf("%w: kafka client: %v", ErrPublishConfig, err)
	}
	defer cl.Close()
	rec := &kgo.Record{Topic: t.Topic, Value: payload}
	if idempotencyKey != "" {
		rec.Headers = []kgo.RecordHeader{{Key: "idempotency_key", Value: []byte(idempotencyKey)}}
	}
	return cl.ProduceSync(ctx, rec).FirstErr()
}

// kafkaOpts is the complete client option set for one send. It is returned whole so a
// test can pin the delivery contract on it.
//
// Delivery is what it was before this client: leader acknowledgement, no idempotent
// producer (so no InitProducerID round trip and no cluster ACL it would need), automatic
// topic creation allowed, no linger. The dialer serves EVERY broker connection — the
// seeds and each broker the cluster advertises in metadata.
func kafkaOpts(t connectorspec.KafkaTarget, dial func(context.Context, string, string) (net.Conn, error)) []kgo.Opt {
	id := t.ClientID
	if id == "" {
		id = defaultKafkaClientID
	}
	d := dial
	if t.TLS {
		d = tlsWrap(dial)
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(t.Seeds...),
		kgo.Dialer(d),
		kgo.ClientID(id),
		kgo.RequiredAcks(kgo.LeaderAck()),
		kgo.DisableIdempotentWrite(),
		kgo.AllowAutoTopicCreation(),
		kgo.ProducerLinger(0),
		kgo.BrokerMaxReadBytes(maxKafkaResponseBytes),
		// franz-go requires FetchMaxBytes <= BrokerMaxReadBytes; it is a consumer setting
		// and bounds nothing for a producer.
		kgo.FetchMaxBytes(maxKafkaResponseBytes),
	}
	if t.SASL != nil {
		if m := kafkaMechanism(*t.SASL); m != nil {
			opts = append(opts, kgo.SASL(m))
		}
	}
	return opts
}

// kafkaMechanism maps the validated mechanism name to its franz-go implementation, or nil
// for a name connectorspec does not admit.
func kafkaMechanism(s connectorspec.KafkaSASL) sasl.Mechanism {
	switch s.Mechanism {
	case "PLAIN":
		return plain.Auth{User: s.User, Pass: s.Password}.AsMechanism()
	case "SCRAM-SHA-256":
		return scram.Auth{User: s.User, Pass: s.Password}.AsSha256Mechanism()
	case "SCRAM-SHA-512":
		return scram.Auth{User: s.User, Pass: s.Password}.AsSha512Mechanism()
	}
	return nil
}

// tlsWrap layers TLS over the guarded dial, verifying each broker against the host it was
// dialed by — the seed as authored, or the name the cluster advertised.
func tlsWrap(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, host string) (net.Conn, error) {
		conn, err := dial(ctx, network, host)
		if err != nil {
			return nil, err
		}
		name, _, err := net.SplitHostPort(host)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		tc := tls.Client(conn, &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tc, nil
	}
}
