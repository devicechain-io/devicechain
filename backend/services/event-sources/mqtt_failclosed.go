// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// storeIngest is what takes delivery from the MQTT gateway's message store: the gateway source
// over its events durable (StoreSource) and a forwarder each for command responses and device
// reports. See messaging.NewMqttMessageStoreReader.
//
// WHO RUNS IT. A pod with infrastructure.nats.mqttFailClosed on starts it with the service.
// A pod with it off starts it the first time it sees a mark of this instance on the store (at
// its start, or on a later watch pass): the store is then held fail-closed by other pods, or
// was released and still has durables, and in both cases those durables can hold the only
// copy of a device publish a pod with the setting on skipped on its direct stream. So every
// pod reads them, and a store whose holders have all gone is still drained. Once started it
// runs until the service stops; a reader whose durable has been removed waits for it quietly.
type storeIngest struct {
	mu      sync.Mutex
	started bool
	readers []*messaging.MqttStoreReader
	fwds    []*processor.StoreForwarder
}

// mqttStoreIngest is the process's storeIngest.
var mqttStoreIngest = &storeIngest{}

// errNoGatewaySourceForStore refuses mqttFailClosed with no source on the platform broker.
var errNoGatewaySourceForStore = errors.New("infrastructure.nats.mqttFailClosed is on but no event source " +
	"points at the platform broker: nothing would read device events from the MQTT message store, so it " +
	"would fill and refuse every QoS 1 device publish")

// start builds and starts the readers, once. It is safe to call from the watch goroutine.
func (s *storeIngest) start(ctx context.Context, nmgr *messaging.NatsManager) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	if StoreSource == nil {
		return errNoGatewaySourceForStore
	}
	// Everything is built before anything starts, so a failure leaves nothing running and the
	// next call builds again.
	events, err := nmgr.NewMqttMessageStoreReader(messaging.MqttFailClosedEvents)
	if err != nil {
		return err
	}
	s.readers = append(s.readers, events)
	writer, err := newGatewayWriter(nmgr)
	if err != nil {
		s.closeReaders()
		return err
	}
	var fwds []*processor.StoreForwarder
	for _, k := range []struct {
		kind   messaging.MqttFailClosedKind
		suffix string
	}{
		{messaging.MqttFailClosedResponses, messaging.SubjectCommandResponses},
		{messaging.MqttFailClosedReports, messaging.SubjectDeviceReports},
	} {
		r, err := nmgr.NewMqttMessageStoreReader(k.kind)
		if err != nil {
			s.closeReaders()
			return err
		}
		s.readers = append(s.readers, r)
		w, err := nmgr.NewWriter(k.suffix)
		if err != nil {
			s.closeReaders()
			return err
		}
		fwds = append(fwds, processor.NewStoreForwarder(Microservice, k.suffix, r, w))
	}
	StoreSource.SetReader(events)
	StoreSource.SetWriter(writer)
	if err := StoreSource.Initialize(ctx); err != nil {
		s.closeReaders()
		return err
	}
	if err := StoreSource.Start(ctx); err != nil {
		s.closeReaders()
		return err
	}
	for _, f := range fwds {
		f.Start()
	}
	s.fwds = fwds
	s.started = true
	log.Info().Msg("Reading the platform durables on the MQTT message store: device events, command responses and device reports")
	return nil
}

// stop stops what start started, before the broker connection drains.
func (s *storeIngest) stop(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return
	}
	if err := StoreSource.Stop(ctx); err != nil {
		log.Warn().Err(err).Msg("Stopping the MQTT message store's events source")
	}
	for _, f := range s.fwds {
		f.Stop(ctx)
	}
	s.closeReaders()
	s.fwds = nil
	s.started = false
}

func (s *storeIngest) closeReaders() {
	for _, r := range s.readers {
		r.Close()
	}
	s.readers = nil
}

// running reports whether start has run; read by tests.
func (s *storeIngest) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// onFailClosedPass is the watch's report to a pod: a store carrying this instance's mark
// has durables to read, so a pod with the setting off starts reading them too.
func onFailClosedPass(ctx context.Context, nmgr *messaging.NatsManager) func(marked bool) {
	return func(marked bool) {
		if !marked {
			return
		}
		if err := mqttStoreIngest.start(ctx, nmgr); err != nil {
			log.Error().Err(err).Msg("Could not start reading the platform durables on the MQTT message store; the next check tries again")
		}
	}
}
