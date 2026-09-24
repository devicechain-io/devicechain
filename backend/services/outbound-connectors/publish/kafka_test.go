// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// kafkaRequest is one request frame read by the hand-written fake below.
type kafkaRequest struct {
	key, version int16
	correlation  int32
}

func readKafkaRequest(c net.Conn) (kafkaRequest, error) {
	var size [4]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		return kafkaRequest{}, err
	}
	body := make([]byte, binary.BigEndian.Uint32(size[:]))
	if _, err := io.ReadFull(c, body); err != nil {
		return kafkaRequest{}, err
	}
	return kafkaRequest{
		key:         int16(binary.BigEndian.Uint16(body[0:2])),
		version:     int16(binary.BigEndian.Uint16(body[2:4])),
		correlation: int32(binary.BigEndian.Uint32(body[4:8])),
	}, nil
}

// writeKafkaResponse frames resp as the answer to req.
func writeKafkaResponse(c net.Conn, req kafkaRequest, resp kmsg.Response) error {
	resp.SetVersion(req.version)
	out := binary.BigEndian.AppendUint32(nil, uint32(req.correlation))
	r := kmsg.RequestForKey(req.key)
	r.SetVersion(req.version)
	// ApiVersions answers with a v0 header whatever its version; every other flexible
	// response header carries an (empty) tagged-field section.
	if r.IsFlexible() && req.key != 18 {
		out = append(out, 0)
	}
	out = resp.AppendTo(out)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(out)))
	_, err := c.Write(append(frame, out...))
	return err
}

// apiVersions advertises Produce and Metadata at versions whose request headers are not
// flexible, so the fake never needs to decode a request body.
func apiVersions() *kmsg.ApiVersionsResponse {
	resp := kmsg.NewPtrApiVersionsResponse()
	resp.ApiKeys = []kmsg.ApiVersionsResponseApiKey{
		{ApiKey: 0, MinVersion: 3, MaxVersion: 7},  // Produce
		{ApiKey: 3, MinVersion: 1, MaxVersion: 7},  // Metadata
		{ApiKey: 18, MinVersion: 0, MaxVersion: 3}, // ApiVersions
	}
	return resp
}

// serveAdvertisingSeed answers ApiVersions and Metadata, advertising broker 1 at
// host:port as the leader of topic "t".
func serveAdvertisingSeed(host string, port int32) func(net.Conn) {
	return func(c net.Conn) {
		for {
			req, err := readKafkaRequest(c)
			if err != nil {
				return
			}
			var resp kmsg.Response
			switch req.key {
			case 18:
				resp = apiVersions()
			case 3:
				m := kmsg.NewPtrMetadataResponse()
				m.ControllerID = 1
				m.Brokers = []kmsg.MetadataResponseBroker{{NodeID: 1, Host: host, Port: port}}
				topic := kmsg.NewMetadataResponseTopic()
				name := "t"
				topic.Topic = &name
				p := kmsg.NewMetadataResponseTopicPartition()
				p.Partition, p.Leader, p.Replicas, p.ISR = 0, 1, []int32{1}, []int32{1}
				topic.Partitions = []kmsg.MetadataResponseTopicPartition{p}
				m.Topics = []kmsg.MetadataResponseTopic{topic}
				resp = m
			default:
				return
			}
			if writeKafkaResponse(c, req, resp) != nil {
				return
			}
		}
	}
}

func kafkaTarget(seeds ...string) connectorspec.KafkaTarget {
	return connectorspec.KafkaTarget{Seeds: seeds, Topic: "t"}
}

// The second hop is checked. A seed the operator allowed advertises a broker at an address
// that is NOT allowed; the client must be refused there too — on the broker the CLUSTER
// named, not only on the addresses the connector named — and the refusal ends the send at
// once rather than leaving it to wait out its deadline.
func TestKafkaAdvertisedBrokerIsChecked(t *testing.T) {
	second := listen(t, "tcp", "127.0.0.2:0", nil)
	secondPort, err := strconv.Atoi(second.port())
	require.NoError(t, err)
	seed := listen(t, "tcp", "127.0.0.1:0", serveAdvertisingSeed("127.0.0.2", int32(secondPort)))

	const budget = 5 * time.Second
	start := time.Now()
	err = NewSender(guardAllowing("127.0.0.1/32")).Send(sendCtx(t, budget),
		kafkaTarget("127.0.0.1:"+seed.port()), []byte("x"), "k")
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.True(t, isBlocked(err), "the advertised broker must be refused: %v", err)
	assert.GreaterOrEqual(t, seed.accepts.Load(), int32(1), "the allowed seed was reached")
	assert.Equal(t, int32(0), second.accepts.Load(), "the advertised broker must never be reached")
	assert.Less(t, elapsed, budget/2, "the first refusal cancels the send")

	// Anchor: allow the advertised broker too, and the client DOES dial it. Without this,
	// "0 accepts" above could mean the client never tried.
	_ = NewSender(guardAllowing("127.0.0.0/8")).Send(sendCtx(t, 2*time.Second),
		kafkaTarget("127.0.0.1:"+seed.port()), []byte("x"), "k")
	assert.GreaterOrEqual(t, second.accepts.Load(), int32(1), "the anchor: an allowed advertised broker is dialed")
}

// A broker whose response claims 50 MiB is refused on the size prefix, before the client
// allocates it.
func TestAnOversizedKafkaResponseIsRefused(t *testing.T) {
	ln := listen(t, "tcp", "127.0.0.1:0", func(c net.Conn) {
		req, err := readKafkaRequest(c)
		if err != nil {
			return
		}
		hdr := binary.BigEndian.AppendUint32(nil, 50<<20)
		hdr = binary.BigEndian.AppendUint32(hdr, uint32(req.correlation))
		_, _ = c.Write(hdr)
		_, _ = io.Copy(io.Discard, c)
	})
	var err error
	grew := heapGrowth(func() {
		err = NewSender(guardAllowing("127.0.0.1/32")).Send(sendCtx(t, 2*time.Second),
			kafkaTarget("127.0.0.1:"+ln.port()), []byte("x"), "k")
	})
	require.Error(t, err)
	assert.False(t, isBlocked(err))
	assert.Less(t, grew, uint64(20<<20), "the send allocated %d bytes", grew)
}

// Delivery through a real (in-process) Kafka: the record arrives with its value and the
// idempotency key as a header.
func TestKafkaDeliversWithIdempotencyHeader(t *testing.T) {
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "t"))
	require.NoError(t, err)
	t.Cleanup(c.Close)

	err = NewSender(guardAllowing("127.0.0.1/32")).Send(sendCtx(t, 10*time.Second),
		kafkaTarget(c.ListenAddrs()...), []byte(`{"temp":72}`), "idem-7")
	require.NoError(t, err)

	consumer, err := kgo.NewClient(kgo.SeedBrokers(c.ListenAddrs()...), kgo.ConsumeTopics("t"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	require.NoError(t, err)
	t.Cleanup(consumer.Close)
	ctx := sendCtx(t, 10*time.Second)
	var recs []*kgo.Record
	for len(recs) == 0 && ctx.Err() == nil {
		recs = append(recs, consumer.PollFetches(ctx).Records()...)
	}
	require.Len(t, recs, 1)
	assert.Equal(t, `{"temp":72}`, string(recs[0].Value))
	assert.Equal(t, []kgo.RecordHeader{{Key: "idempotency_key", Value: []byte("idem-7")}}, recs[0].Headers)
}

// The delivery contract is pinned on the option set: leader acknowledgement, no idempotent
// producer, automatic topic creation, no linger, the default client id, and the read
// bounds. There is no real-broker test in CI; this is what holds parity.
func TestKafkaOptsPinParity(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, io.EOF }
	cl, err := kgo.NewClient(kafkaOpts(kafkaTarget("b:9092"), dial)...)
	require.NoError(t, err)
	defer cl.Close()

	assert.Equal(t, kgo.LeaderAck(), cl.OptValue(kgo.RequiredAcks))
	assert.Equal(t, true, cl.OptValue(kgo.DisableIdempotentWrite))
	assert.Equal(t, true, cl.OptValue(kgo.AllowAutoTopicCreation))
	assert.Equal(t, time.Duration(0), cl.OptValue(kgo.ProducerLinger))
	assert.Equal(t, []any{"devicechain", true}, cl.OptValues(kgo.ClientID))
	assert.Equal(t, int32(1<<20), cl.OptValue(kgo.BrokerMaxReadBytes))
	assert.Equal(t, int32(1<<20), cl.OptValue(kgo.FetchMaxBytes))
	assert.Equal(t, []string{"b:9092"}, cl.OptValue(kgo.SeedBrokers))
	assert.NotNil(t, cl.OptValue(kgo.Dialer), "every broker connection goes through our dial")

	named, err := kgo.NewClient(kafkaOpts(connectorspec.KafkaTarget{Seeds: []string{"b:9092"}, Topic: "t", ClientID: "acme"}, dial)...)
	require.NoError(t, err)
	defer named.Close()
	assert.Equal(t, []any{"acme", true}, named.OptValues(kgo.ClientID))
}
