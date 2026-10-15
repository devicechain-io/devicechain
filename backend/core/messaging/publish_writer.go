// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "fmt"

// PublishWriter names the producer a pipelined writer (NewOrderedWriter, NewAsyncWriter)
// publishes for. It is the writer label on jetstream_publish_window_full_seconds_total,
// jetstream_publish_window_full_total and jetstream_publish_duplicates_total.
//
// 🔑 THE SUFFIX ALONE CANNOT TELL WRITERS APART. event-sources runs the gateway capture
// writer, one writer per external MQTT source and the presence writer, all into
// inbound-events; labelled by suffix only they summed into one series, so a full window on
// one could not be told from a full window on another. The label is the producer, set at
// construction, and the vocabulary is fixed here so it stays bounded: a writer per external
// source shares its label rather than adding a series per source.
type PublishWriter string

const (
	// WriterResolution is device-management's resolution stage, publishing resolved and
	// failed events.
	WriterResolution PublishWriter = "resolution"
	// WriterCapture is event-sources' gateway capture writer, forwarding what devices
	// published through the broker's MQTT gateway.
	WriterCapture PublishWriter = "capture"
	// WriterExternalMqtt is an event-sources external MQTT source (one writer per source,
	// all under this label).
	WriterExternalMqtt PublishWriter = "external-mqtt"
	// WriterPresence is event-sources' broker presence tap.
	WriterPresence PublishWriter = "presence"
	// WriterSparkplug is a sparkplug-ingest source.
	WriterSparkplug PublishWriter = "sparkplug"
)

// publishWriters is the whole vocabulary; a name outside it is refused at construction.
var publishWriters = map[PublishWriter]bool{
	WriterResolution:   true,
	WriterCapture:      true,
	WriterExternalMqtt: true,
	WriterPresence:     true,
	WriterSparkplug:    true,
}

// refuseUnknownWriter refuses a writer name outside the vocabulary, the empty name included,
// so the label cannot grow without bound or arrive blank.
func refuseUnknownWriter(w PublishWriter) error {
	if !publishWriters[w] {
		return fmt.Errorf("messaging: %q is not a known publish writer name", string(w))
	}
	return nil
}
