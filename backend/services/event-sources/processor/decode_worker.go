/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package processor

import (
	"fmt"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/rs/zerolog/log"
)

// Worker used to decode event payloads.
type DecodeWorker struct {
	WorkerId    int
	SourceId    string
	Decoder     Decoder
	RawMessages <-chan []byte
	Callback    func(string, *model.UnresolvedEvent, interface{})
	Failed      func(string, []byte, error)
}

// Create a new decode worker.
func NewDecodeWorker(workerId int, sourceId string, decoder Decoder, rawMessages <-chan []byte,
	callback func(string, *model.UnresolvedEvent, interface{}),
	failed func(string, []byte, error)) *DecodeWorker {
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
			event, payload, err := wrk.Decoder.Decode(raw)
			if err != nil {
				wrk.Failed(wrk.SourceId, raw, err)
			} else {
				wrk.Callback(wrk.SourceId, event, payload)
			}
		} else {
			log.Debug().Msg("Decode worker received shutdown signal.")
			return
		}
	}
}
