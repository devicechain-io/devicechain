// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
)

func measurementBody(n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"measurements":{"m%d":"%d"}}`, i, i)
	}
	return []byte(fmt.Sprintf(`{"device":"d1","eventType":"Measurement","payload":{"entries":[%s]}}`, b.String()))
}

// 🔴 THE RETIRED KEY STILL LOADS, AND IT NO LONGER CHANGES ANYTHING. maxReadingsPerMessage set
// the JSON transports' ceiling until the limit became a platform constant. A configuration
// written for that release must still start the service (the load is a strict decode, so an
// unlisted key would fail it closed and take every event source down) — and the decoder the
// service builds from it must enforce 256, not the value the document asks for. It is driven
// through createDecoder, the one production construction site, not a decoder built in the test.
func TestARetiredReadingCeilingLoadsAndIsNotHonoured(t *testing.T) {
	saved := Configuration
	t.Cleanup(func() { Configuration = saved })

	for _, v := range []int{5000, 100} {
		cfg := config.NewEventSourcesConfiguration()
		if err := core.LoadConfiguration([]byte(fmt.Sprintf(`{"maxReadingsPerMessage": %d}`, v)), cfg); err != nil {
			t.Fatalf("a document still carrying the retired key must load, got: %v", err)
		}
		Configuration = cfg
		d, err := createDecoder(config.EventSource{Id: "src", Type: "mqtt",
			Decoder: config.EventDecoder{Type: processor.DECODER_TYPE_JSON}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.Decode(measurementBody(257), time.Time{}); !errors.Is(err, model.ErrTooManyReadings) {
			t.Errorf("with maxReadingsPerMessage=%d the shipped decoder must refuse 257 readings, got %v", v, err)
		}
		if _, _, err := d.Decode(measurementBody(256), time.Time{}); err != nil {
			t.Errorf("with maxReadingsPerMessage=%d the shipped decoder must accept 256 readings, got %v", v, err)
		}
	}
}
