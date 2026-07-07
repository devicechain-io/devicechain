// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/rs/zerolog/log"
)

// rawMessage pairs an inbound transport message's payload with the tenant it
// belongs to. The tenant is derived by the source from its own addressing (an
// MQTT topic "{instanceId}/{tenant}/...", an HTTP path "/{instanceId}/{tenant}/...",
// ADR-006/ADR-048) before
// the payload is enqueued, so it travels with the payload to the producer, which
// publishes to the tenant-scoped subject.
type rawMessage struct {
	tenant  string
	payload []byte
}

// Worker used to decode event payloads.
type DecodeWorker struct {
	WorkerId    int
	SourceId    string
	Decoder     Decoder
	RawMessages <-chan rawMessage
	Callback    func(string, string, *model.UnresolvedEvent, interface{})
	Failed      func(string, string, []byte, error)
}

// Create a new decode worker.
func NewDecodeWorker(workerId int, sourceId string, decoder Decoder, rawMessages <-chan rawMessage,
	callback func(string, string, *model.UnresolvedEvent, interface{}),
	failed func(string, string, []byte, error)) *DecodeWorker {
	worker := &DecodeWorker{
		WorkerId:    workerId,
		SourceId:    sourceId,
		Decoder:     decoder,
		RawMessages: rawMessages,
		Callback:    callback,
		Failed:      failed,
	}
	return worker
}

// Processes raw payloads into decoded events.
func (wrk *DecodeWorker) Process() {
	for {
		raw, more := <-wrk.RawMessages
		if more {
			log.Debug().Msg(fmt.Sprintf("Decode handled by worker id %d", wrk.WorkerId))
			event, payload, err := wrk.Decoder.Decode(raw.payload)
			if err != nil {
				wrk.Failed(wrk.SourceId, raw.tenant, raw.payload, err)
			} else {
				wrk.Callback(wrk.SourceId, raw.tenant, event, payload)
			}
		} else {
			log.Debug().Msg("Decode worker received shutdown signal.")
			return
		}
	}
}
