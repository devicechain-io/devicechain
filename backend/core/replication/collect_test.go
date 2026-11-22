// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import (
	"errors"
	"reflect"
	"testing"

	nats "github.com/nats-io/nats.go"
)

// Embedding the interface makes unexpected broker calls fail the test.
type collectJetStream struct {
	nats.JetStreamContext
	streams   []*nats.StreamInfo
	consumers map[string][]*nats.ConsumerInfo
}

func (js collectJetStream) AccountInfo(...nats.JSOpt) (*nats.AccountInfo, error) {
	info := &nats.AccountInfo{}
	info.Streams = len(js.streams)
	return info, nil
}

func (js collectJetStream) StreamsInfo(...nats.JSOpt) <-chan *nats.StreamInfo {
	ch := make(chan *nats.StreamInfo, len(js.streams))
	for _, si := range js.streams {
		ch <- si
	}
	close(ch)
	return ch
}

func (js collectJetStream) ConsumersInfo(stream string, _ ...nats.JSOpt) <-chan *nats.ConsumerInfo {
	items := js.consumers[stream]
	ch := make(chan *nats.ConsumerInfo, len(items))
	for _, ci := range items {
		ch <- ci
	}
	close(ch)
	return ch
}

func TestCollectConsumerListingCompleteness(t *testing.T) {
	durable := &nats.ConsumerInfo{Name: "store", Config: nats.ConsumerConfig{Durable: "store"}}
	ephemeral := &nats.ConsumerInfo{Name: "reader"}
	for _, tc := range []struct {
		name     string
		reported int
		listed   []*nats.ConsumerInfo
		wantErr  bool
	}{
		{"short page", 2, []*nats.ConsumerInfo{durable}, true},
		{"aborted before first page", 2, nil, true},
		{"nil entry is not a consumer", 2, []*nats.ConsumerInfo{durable, nil}, true},
		{"concurrent creation", 1, []*nats.ConsumerInfo{durable, ephemeral}, true},
		{"complete", 2, []*nats.ConsumerInfo{durable, nil, ephemeral}, false},
		{"complete empty", 0, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			js := collectJetStream{
				streams: []*nats.StreamInfo{{
					Config: nats.StreamConfig{Name: "i_events", Replicas: 1},
					State:  nats.StreamState{Consumers: tc.reported},
				}},
				consumers: map[string][]*nats.ConsumerInfo{"i_events": tc.listed},
			}
			snap, err := Collect(js, Expectation{Prefixes: []string{"i_"}})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted a listing of %d entries when the stream reports %d consumers: %+v", len(tc.listed), tc.reported, snap)
				}
				var listingErr *ConsumerListingError
				if !errors.As(err, &listingErr) {
					t.Fatalf("want a typed consumer listing error, got %T: %v", err, err)
				}
				listed := 0
				for _, ci := range tc.listed {
					if ci != nil {
						listed++
					}
				}
				if listingErr.Stream != "i_events" || listingErr.Expected != tc.reported || listingErr.Listed != listed {
					t.Fatalf("wrong listing error details: %+v", listingErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("complete listing refused: %v", err)
			}
			want := Snapshot{Objects: []Object{{Name: "i_events", Replicas: 1}}}
			if tc.reported != 0 {
				want.Consumers = []Consumer{
					{Stream: "i_events", Name: "store", Durable: true},
					{Stream: "i_events", Name: "reader"},
				}
			}
			if !reflect.DeepEqual(snap, want) {
				t.Fatalf("complete listing changed: got %+v, want %+v", snap, want)
			}
		})
	}
}

func TestCollectConsumerCountsArePerStream(t *testing.T) {
	js := collectJetStream{
		streams: []*nats.StreamInfo{
			{Config: nats.StreamConfig{Name: "i_first"}, State: nats.StreamState{Consumers: 1}},
			{Config: nats.StreamConfig{Name: "i_second"}, State: nats.StreamState{Consumers: 1}},
		},
		consumers: map[string][]*nats.ConsumerInfo{
			"i_first":  {{Name: "store"}},
			"i_second": nil,
		},
	}
	if snap, err := Collect(js, Expectation{Prefixes: []string{"i_"}}); err == nil {
		t.Fatalf("accepted a short second listing after a complete first listing: %+v", snap)
	}
}
